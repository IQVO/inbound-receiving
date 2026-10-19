package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/mcp"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestServer wires the read use cases (depsOver, the same function run()
// uses) over the in-memory TEST DOUBLES, behind the real router. The Postgres
// wiring is proven in main_integration_test.go.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	deps := depsOver(memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo(), memory.NewDockDoors())
	srv := httptest.NewServer(newRouter(inboundmcp.Handler(inboundmcp.NewServer(deps))))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouter_HealthzIsOpenAndServesOK(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// Both mount points speak Streamable HTTP, with no credentials, and a real
// client can list the seven read-only tools and call two of them end to end.
func TestStreamableHTTP_RootAndMCPPathsServeTools(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/", "/mcp"} {
		t.Run(path, func(t *testing.T) { exerciseEndpoint(t, srv.URL+path) })
	}
}

// exerciseEndpoint connects a real Streamable HTTP client to endpoint, lists
// the tools (seven, all read-only) and calls two of them.
func exerciseEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 7 {
		t.Fatalf("tools = %d, want 7", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not read-only", tool.Name)
		}
	}

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_asns", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_asns: err=%v res=%+v", err, res)
	}
	res, err = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "get_asn", Arguments: map[string]any{"asn_number": "ASN-404"}})
	if err != nil || !res.IsError {
		t.Fatalf("get_asn on an empty store: err=%v res=%+v, want an isError result", err, res)
	}
}

// The binary reads no API key: requests carrying (or lacking) any
// Authorization header behave identically.
func TestNoAuthIsRequired(t *testing.T) {
	srv := newTestServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for _, auth := range []string{"", "Bearer nonsense"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("initialize with Authorization=%q = %d, want 200", auth, resp.StatusCode)
		}
	}
}

// An unreachable database is a boot error after the bounded retries, never
// a silent fallback.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// There is no in-memory runtime: an empty DATABASE_URL is a boot error from
// both loadConfig (before anything opens) and buildDeps.
func TestNoDatabaseURLIsABootError(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("loadConfig(empty) = %v, want errMissingDatabaseURL", err)
	}
	if _, _, err := buildDeps(context.Background(), quietLogger(), "", ""); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("buildDeps(\"\") = %v, want errMissingDatabaseURL", err)
	}
}

// DOCK_DOOR_MODE only labels list_docks: unset/kafka pass, permissive/off/
// unknown are boot errors naming the variable.
func TestLoadConfig_DockDoorMode(t *testing.T) {
	envWith := func(mode string) func(string) string {
		return func(k string) string {
			switch k {
			case "DATABASE_URL":
				return "postgres://u@db/x"
			case "DOCK_DOOR_MODE":
				return mode
			}
			return ""
		}
	}
	for _, ok := range []string{"", "kafka"} {
		if url, err := loadConfig(envWith(ok)); err != nil || url != "postgres://u@db/x" {
			t.Errorf("DOCK_DOOR_MODE=%q: url=%q err=%v, want accepted", ok, url, err)
		}
	}
	for _, bad := range []string{"permissive", "off", "strict"} {
		if _, err := loadConfig(envWith(bad)); err == nil || !strings.Contains(err.Error(), "DOCK_DOOR_MODE") {
			t.Errorf("DOCK_DOOR_MODE=%q: err=%v, want a boot error naming DOCK_DOOR_MODE", bad, err)
		}
	}
}

// list_docks always reports the kafka mode: the runtime has no other.
func TestDepsOver_ReportsKafkaDockMode(t *testing.T) {
	deps := depsOver(memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo(), memory.NewDockDoors())
	list, err := deps.ListDocks.Handle(context.Background())
	if err != nil || list.Mode != usecases.ModeKafka {
		t.Fatalf("docks = %+v, %v; want mode kafka", list, err)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("IR_MCP_TEST_SET", "v")
	t.Setenv("IR_MCP_TEST_EMPTY", "")
	if got := getenv("IR_MCP_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("IR_MCP_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
