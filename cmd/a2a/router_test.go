package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// newTestAgent builds the router exactly as run() does (newAgent over
// depsOver), over in-memory TEST DOUBLES holding one ASN. The memory adapters
// are imported by this _test file only, never by the binary
// (TestRuntimeBinaryHasNoInMemoryAdapters). The Postgres wiring is proven in
// main_integration_test.go.
func newTestAgent(t *testing.T) http.Handler {
	t.Helper()
	asns := memory.NewAsnRepo()
	a, _, err := asn.Register("ASN-1", "ACME", time.Time{}, []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}}, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := asns.Save(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	return newAgent(depsOver(asns, memory.NewAppointmentRepo(), memory.NewReceiptRepo()), testAgentURL, "test")
}

func TestRouter_HealthzServesOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestAgent(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: got %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("GET /healthz body = %q, want {\"status\":\"ok\"}", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /healthz Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestAgent(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET agent card: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"inbound-receiving"`, testAgentURL, `"asn_receiving_status"`, `"dock_schedule"`} {
		if !strings.Contains(body, want) {
			t.Errorf("agent card lacks %s: %s", want, body)
		}
	}
}

// TestRouter_BothMountsReachTheJSONRPCHandler pins the fleet's
// <CTX>_A2A_ENDPOINT convention: the endpoint answers at "/" and at "/a2a",
// the use cases behind it are the wired ones (the seeded ASN is read), and no
// credentials are needed, whatever Authorization header is (or is not) sent.
func TestRouter_BothMountsReachTheJSONRPCHandler(t *testing.T) {
	h := newTestAgent(t)
	for _, path := range []string{"/", "/a2a"} {
		for _, auth := range []string{"", "Bearer nonsense"} {
			t.Run(path+" auth="+auth, func(t *testing.T) {
				body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"asn_receiving_status","arguments":{"asnNumber":"ASN-1"}}}]}}}`
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("A2A-Version", "1.0")
				if auth != "" {
					req.Header.Set("Authorization", auth)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				raw, _ := io.ReadAll(rec.Body)
				if rec.Code != http.StatusOK || !strings.Contains(string(raw), `"jsonrpc"`) {
					t.Fatalf("POST %s: %d %q does not look like a JSON-RPC reply", path, rec.Code, raw)
				}
				if !strings.Contains(string(raw), `"supplierRef":"ACME"`) {
					t.Fatalf("POST %s: reply %q lacks the seeded ASN read through the wired use case", path, raw)
				}
			})
		}
	}
}

// TestRouter_ACardFetchedFromTheBinaryIsDialable runs the discovery flow a
// delegating agent runs against the real router: fetch the card, build a
// client from it, call a skill.
func TestRouter_ACardFetchedFromTheBinaryIsDialable(t *testing.T) {
	srv := httptest.NewServer(newTestAgent(t))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	// The card advertises the configured URL, not the test server's: point the
	// client at the test server the way a gateway-rewritten route would.
	card.SupportedInterfaces[0].URL = srv.URL + "/a2a"
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": "dock_schedule", "arguments": map[string]any{}}))})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T", resp)
	}
	if text, _ := msg.Parts[1].Content.(a2a.Text); !strings.HasPrefix(string(text), "0 dock appointments") {
		t.Fatalf("summary = %q", text)
	}
}
