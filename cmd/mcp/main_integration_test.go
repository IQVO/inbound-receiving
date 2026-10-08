//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inboundmcp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/mcp"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_test"),
		tcpostgres.WithUsername("inbound_receiving_test"),
		tcpostgres.WithPassword("inbound_receiving_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return url
}

// seedThroughWriteUseCases writes an ASN, an appointment and a closed
// receipt the way cmd/api's REST adapter does (same use cases, Postgres
// adapters, transactional outbox), so the MCP binary is proven to read what
// the api wrote in the shared database.
func seedThroughWriteUseCases(t *testing.T, url string) (receiptID string) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	w := usecases.Writer{
		Asns: postgres.NewAsnRepo(pool), Appointments: postgres.NewAppointmentRepo(pool), Receipts: postgres.NewReceiptRepo(pool),
		Outbox: postgres.NewOutboxRepo(pool), Encoder: outboundkafka.NewEncoder(), UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
	if err := postgres.NewDockDoors(pool).Upsert(ctx, repository.DockDoor{Code: appointment.DoorCode("DOOR-1"), Flow: repository.DockFlowInbound}); err != nil {
		t.Fatalf("seed dock door: %v", err)
	}
	if _, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-1", SupplierRef: "ACME", ExpectedArrival: time.Now().Add(24 * time.Hour),
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	}); err != nil {
		t.Fatalf("register asn: %v", err)
	}
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	if _, err := (&usecases.BookAppointment{Writer: w}).Handle(ctx, usecases.BookAppointmentCommand{
		DoorCode: "DOOR-1", Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(2 * time.Hour), AsnNumbers: []string{"ASN-1"},
	}); err != nil {
		t.Fatalf("book appointment: %v", err)
	}
	if _, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-2", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-2", ExpectedQty: 3}},
	}); err != nil {
		t.Fatalf("register asn 2: %v", err)
	}
	r, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2"})
	if err != nil {
		t.Fatalf("open receipt: %v", err)
	}
	return string(r.ID())
}

func countOutbox(t *testing.T, url string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
		t.Fatalf("count outbox_events: %v", err)
	}
	return n
}

// The binary migrates a FRESH testcontainers database itself (before any api
// ran), then reads through MCP tools exactly what the write use cases
// persisted, and its reads add no outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url, usecases.ModeKafka)
	if err != nil {
		t.Fatalf("buildDeps on a fresh database: %v", err)
	}
	t.Cleanup(closeFn)

	receiptID := seedThroughWriteUseCases(t, url)
	before := countOutbox(t, url)
	if before < 4 {
		t.Fatalf("outbox rows after seeding = %d, want the ASN, appointment and receipt events", before)
	}

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session, receiptID)

	if after := countOutbox(t, url); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession, receiptID string) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}

	if a := call("get_asn", map[string]any{"asn_number": "ASN-1"}); a["supplier_ref"] != "ACME" || a["state"] != "Registered" || a["version"] != 1.0 {
		t.Fatalf("get_asn = %v", a)
	}
	// Opening the walk-in receipt moved ASN-2 to Receiving; ASN-1 is still Registered.
	if page := call("list_asns", map[string]any{"state": "Registered"}); fmt.Sprint(page["items"].([]any)[0].(map[string]any)["asn_number"]) != "ASN-1" || len(page["items"].([]any)) != 1 {
		t.Fatalf("list_asns state=Registered = %v", page)
	}
	if page := call("list_asns", map[string]any{"state": "Receiving"}); fmt.Sprint(page["items"].([]any)[0].(map[string]any)["asn_number"]) != "ASN-2" || len(page["items"].([]any)) != 1 {
		t.Fatalf("list_asns state=Receiving = %v", page)
	}
	appts := call("list_appointments", map[string]any{"door": "DOOR-1", "state": "Booked"})
	items := appts["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list_appointments = %v", appts)
	}
	id := items[0].(map[string]any)["appointment_id"].(string)
	if a := call("get_appointment", map[string]any{"appointment_id": id}); a["door_code"] != "DOOR-1" || fmt.Sprint(a["asn_numbers"]) != "[ASN-1]" {
		t.Fatalf("get_appointment = %v", a)
	}
	if r := call("get_receipt", map[string]any{"receipt_id": receiptID}); r["asn_number"] != "ASN-2" || r["state"] != "Open" {
		t.Fatalf("get_receipt = %v", r)
	}
	if page := call("list_receipts", map[string]any{"asn_number": "ASN-2", "state": "Open"}); fmt.Sprint(page["items"].([]any)[0].(map[string]any)["receipt_id"]) != receiptID {
		t.Fatalf("list_receipts = %v", page)
	}
	if docks := call("list_docks", map[string]any{}); docks["mode"] != "kafka" || fmt.Sprint(docks["items"]) != "[map[dock_flow:Inbound door_code:DOOR-1]]" {
		t.Fatalf("list_docks = %v", docks)
	}
}
