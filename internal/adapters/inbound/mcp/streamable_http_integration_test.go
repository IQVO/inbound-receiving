//go:build integration

// Integration tests for the MCP inbound adapter over the REAL Streamable
// HTTP transport: mcp.Handler(server) mounted on an httptest.Server, driven
// by the SDK's own client (mcp.NewClient + StreamableClientTransport), with
// the REAL Postgres-backed use cases behind it — exactly the deployment
// shape Kong exposes (ADR-0005: read-only surface, Streamable HTTP only).
// This proves the wire contract (initialize, tools/list, tools/call)
// end-to-end, not the tool handlers in isolation.
//
// Postgres comes from testcontainers: one container per package run, one
// private database per test, migrated once. Never an external DATABASE_URL,
// never t.Skip.
package mcp_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/inbound/mcp"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// One Postgres container serves the whole package, migrated once into a
// template database; each test gets a private clone (milliseconds). See
// internal/application/usecases/usecases_wiring_integration_test.go for the
// same pattern's rationale. Never an external DATABASE_URL, never t.Skip.
const templateDB = "mcp_migrated_template"

var (
	sharedBaseURL string
	dbSeq         atomic.Uint64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_mcp"),
		tcpostgres.WithUsername("inbound"),
		tcpostgres.WithPassword("inbound"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}()

	var err2 error
	sharedBaseURL, err2 = container.ConnectionString(ctx, "sslmode=disable")
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err2)
		return 1
	}
	if err := createDatabase(ctx, templateDB); err != nil {
		fmt.Fprintf(os.Stderr, "create template database: %v\n", err)
		return 1
	}
	tmplDB := withDB(sharedBaseURL, templateDB)
	if err := postgres.RunMigrations(tmplDB); err != nil {
		fmt.Fprintf(os.Stderr, "migrate template: %v\n", err)
		return 1
	}
	return m.Run()
}

// withDB rewrites the path of a connection URL to the named database.
func withDB(baseURL, name string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// createDatabase creates an empty database inside the shared container.
func createDatabase(ctx context.Context, name string) error {
	conn, err := pgx.Connect(ctx, sharedBaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// migratedDB hands the test a connection URL to its own private database
// cloned from the migrated template.
func migratedDB(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("mcp_%d", dbSeq.Add(1))
	conn, err := pgx.Connect(context.Background(), sharedBaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		"CREATE DATABASE %q WITH TEMPLATE %q", name, templateDB)); err != nil {
		t.Fatalf("clone database: %v", err)
	}
	return withDB(sharedBaseURL, name)
}

// mcpHarness wires the REAL production stack — Postgres repos, UnitOfWork,
// write use cases for seeding, read use cases, mcp.NewServer,
// mcp.Handler — and serves it over HTTP, returning a connected SDK client
// session plus the ids of the seeded appointment and receipt. The test
// drives tools/list and tools/call exactly like a model host would.
type mcpHarness struct {
	session       *sdkmcp.ClientSession
	appointmentID string
	receiptID     string
}

// newMCPHarness seeds one full inbound flow (two ASNs, a booked
// appointment, an open walk-in receipt) on a fresh private database and
// serves the real MCP stack over Streamable HTTP.
func newMCPHarness(t *testing.T) *mcpHarness {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, migratedDB(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	w := usecases.Writer{
		Asns:         postgres.NewAsnRepo(pool),
		Appointments: postgres.NewAppointmentRepo(pool),
		Receipts:     postgres.NewReceiptRepo(pool),
		Outbox:       postgres.NewOutboxRepo(pool),
		Encoder:      outboundkafka.NewEncoder(),
		UoW:          postgres.NewUnitOfWork(pool),
		Clock:        clock.System{},
		IDs:          idgen.UUID{},
	}

	// Seed: ASN-1 stays Registered behind its booked appointment; ASN-2
	// moves to Receiving by opening a walk-in receipt against it.
	if _, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-1", SupplierRef: "ACME",
		ExpectedArrival: time.Now().Add(24 * time.Hour),
		Lines:           []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	}); err != nil {
		t.Fatalf("seed register ASN-1: %v", err)
	}
	if _, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-2", SupplierRef: "ACME",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-2", ExpectedQty: 3}},
	}); err != nil {
		t.Fatalf("seed register ASN-2: %v", err)
	}
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	appt, err := (&usecases.BookAppointment{Writer: w}).Handle(ctx, usecases.BookAppointmentCommand{
		DoorCode: "DOOR-1", Carrier: "ACME Freight",
		WindowStart: start, WindowEnd: start.Add(2 * time.Hour), AsnNumbers: []string{"ASN-1"},
	})
	if err != nil {
		t.Fatalf("seed book appointment: %v", err)
	}
	rcpt, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{
		AsnNumber: "ASN-2"})
	if err != nil {
		t.Fatalf("seed open receipt: %v", err)
	}

	server := mcp.NewServer(mcp.Deps{
		GetAsn:           &usecases.GetAsn{Asns: postgres.NewAsnRepo(pool)},
		ListAsns:         &usecases.ListAsns{Asns: postgres.NewAsnRepo(pool)},
		GetAppointment:   &usecases.GetAppointment{Appointments: postgres.NewAppointmentRepo(pool)},
		ListAppointments: &usecases.ListAppointments{Appointments: postgres.NewAppointmentRepo(pool)},
		GetReceipt:       &usecases.GetReceipt{Receipts: postgres.NewReceiptRepo(pool)},
		ListReceipts:     &usecases.ListReceipts{Receipts: postgres.NewReceiptRepo(pool)},
		ListDocks:        &usecases.ListDocks{Doors: postgres.NewDockDoors(pool), Mode: usecases.ModePermissive},
	})

	hs := httptest.NewServer(mcp.Handler(server))
	t.Cleanup(hs.Close)

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "itcov-test-host", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: hs.URL, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &mcpHarness{
		session:       session,
		appointmentID: string(appt.ID()),
		receiptID:     string(rcpt.ID()),
	}
}

// callTool runs tools/call and fails on a transport error; the caller
// decides whether a tool error (res.IsError) is the expected outcome.
func (h *mcpHarness) callTool(t *testing.T, name string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	return res
}

// structured returns the tool's structured content as a map.
func (h *mcpHarness) structured(t *testing.T, res *sdkmcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %+v", res)
	}
	return m
}

// TestMCPListToolsExposesTheContract pins the whole read-only tool surface
// (docs/adr/0005) over the real transport: every registered tool answers
// tools/list, and each carries the read-only annotation a host gates on.
func TestMCPListToolsExposesTheContract(t *testing.T) {
	h := newMCPHarness(t)

	list, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s must carry ReadOnlyHint=true", tool.Name)
		}
	}
	for _, want := range []string{
		"get_asn", "list_asns", "get_appointment", "list_appointments",
		"get_receipt", "list_receipts", "list_docks",
	} {
		if !names[want] {
			t.Fatalf("tools/list must expose %q, got %v", want, names)
		}
	}
	if len(list.Tools) != 7 {
		t.Fatalf("tool count = %d, want 7 (tools: %v)", len(list.Tools), names)
	}
}

// TestMCPCallToolsRoundTripThroughPostgres drives every read tool against
// the seeded data: the SDK client's JSON arguments cross the Streamable
// HTTP transport, the real use cases hit real Postgres, and the structured
// content comes back with the domain's snake_case wire shape.
func TestMCPCallToolsRoundTripThroughPostgres(t *testing.T) {
	h := newMCPHarness(t)

	// get_asn: one ASN with its lines and version.
	asn := h.structured(t, h.callTool(t, "get_asn", map[string]any{"asn_number": "ASN-1"}))
	if asn["supplier_ref"] != "ACME" || asn["state"] != "Registered" || asn["version"] != 1.0 {
		t.Fatalf("get_asn = %v", asn)
	}

	// list_asns with a state filter: ASN-1 is the only Registered one.
	page := h.structured(t, h.callTool(t, "list_asns", map[string]any{"state": "Registered"}))
	items, _ := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["asn_number"] != "ASN-1" {
		t.Fatalf("list_asns state=Registered = %v", page)
	}

	// get_appointment: the seeded booking, door and covered ASN intact.
	appt := h.structured(t, h.callTool(t, "get_appointment", map[string]any{
		"appointment_id": h.appointmentID}))
	if appt["door_code"] != "DOOR-1" || appt["state"] != "Booked" {
		t.Fatalf("get_appointment = %v", appt)
	}
	if fmt.Sprint(appt["asn_numbers"]) != "[ASN-1]" {
		t.Fatalf("get_appointment asn_numbers = %v", appt["asn_numbers"])
	}

	// list_appointments filtered by door and state finds the same one.
	appts := h.structured(t, h.callTool(t, "list_appointments", map[string]any{
		"door": "DOOR-1", "state": "Booked"}))
	aItems, _ := appts["items"].([]any)
	if len(aItems) != 1 || aItems[0].(map[string]any)["appointment_id"] != h.appointmentID {
		t.Fatalf("list_appointments = %v", appts)
	}

	// get_receipt / list_receipts: the walk-in receipt of ASN-2 is Open.
	// (appointment_id and door_code are omitempty on a walk-in: absent.)
	rcpt := h.structured(t, h.callTool(t, "get_receipt", map[string]any{
		"receipt_id": h.receiptID}))
	if rcpt["asn_number"] != "ASN-2" || rcpt["state"] != "Open" {
		t.Fatalf("get_receipt = %v", rcpt)
	}
	if _, has := rcpt["appointment_id"]; has {
		t.Fatalf("walk-in receipt must omit appointment_id: %v", rcpt)
	}
	receipts := h.structured(t, h.callTool(t, "list_receipts", map[string]any{
		"asn_number": "ASN-2", "state": "Open"}))
	rItems, _ := receipts["items"].([]any)
	if len(rItems) != 1 || rItems[0].(map[string]any)["receipt_id"] != h.receiptID {
		t.Fatalf("list_receipts = %v", receipts)
	}

	// list_docks: the permissive local copy (no dock registry consumer ran).
	docks := h.structured(t, h.callTool(t, "list_docks", map[string]any{}))
	if docks["mode"] != "permissive" {
		t.Fatalf("list_docks = %v", docks)
	}
}

// TestMCPCallToolSurfacesDomainAndInputErrorsAsToolErrors proves the two
// rejection paths a host relies on: a domain not-found and an invalid input
// both come back as tool errors (res.IsError), never as transport or
// protocol failures.
func TestMCPCallToolSurfacesDomainAndInputErrorsAsToolErrors(t *testing.T) {
	h := newMCPHarness(t)

	// Domain rejection: an ASN number nothing registered.
	if res := h.callTool(t, "get_asn", map[string]any{"asn_number": "ASN-NOPE"}); !res.IsError {
		t.Fatal("get_asn of an unknown ASN must surface a tool error, not success")
	}

	// Invalid input: an empty asn_number fails the domain's shape check.
	if res := h.callTool(t, "get_asn", map[string]any{"asn_number": ""}); !res.IsError {
		t.Fatal("get_asn with an empty asn_number must surface a tool error")
	}

	// Invalid list query: a state outside the enum.
	if res := h.callTool(t, "list_asns", map[string]any{"state": "Nonsense"}); !res.IsError {
		t.Fatal("list_asns with an invalid state must surface a tool error")
	}
}
