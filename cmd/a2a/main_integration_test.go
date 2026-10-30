//go:build integration

// Integration test for cmd/a2a over REAL Postgres: buildDeps (the same
// migrations + pool + ping the binary runs at boot) behind newAgent, the same
// composition run() serves, driven by the SDK's own client through the real
// router. It proves the A2A skills answer from Postgres-backed use cases, end
// to end, and that the read-only agent leaves the outbox untouched (the write
// use cases below play the REST adapter that fed it).
//
// Postgres comes from testcontainers; never an external DATABASE_URL, never
// t.Skip.
package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

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

// seedThroughWriteUseCases writes an ASN with a booked appointment and a
// walk-in ASN with an open receipt the way cmd/api's REST adapter does (same
// use cases, Postgres adapters, transactional outbox), so the A2A binary is
// proven to read what the api wrote in the shared database.
func seedThroughWriteUseCases(t *testing.T, url string) {
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
	if _, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2"}); err != nil {
		t.Fatalf("open receipt: %v", err)
	}
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
// ran), then answers through A2A exactly what the write use cases persisted,
// and its reads add no outbox row.
func TestA2AAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps on a fresh database: %v", err)
	}
	t.Cleanup(closeFn)

	seedThroughWriteUseCases(t, url)
	before := countOutbox(t, url)
	if before < 3 {
		t.Fatalf("outbox rows after seeding = %d, want the ASN, appointment and receipt events", before)
	}

	srv := httptest.NewServer(newAgent(deps, testAgentURL, "itest"))
	t.Cleanup(srv.Close)
	client := clientFor(t, srv.URL+"/a2a")

	text := send(t, client, map[string]any{"skill": "asn_receiving_status", "arguments": map[string]any{"asnNumber": "ASN-1"}})
	if !strings.HasPrefix(text, "ASN-1 (ACME): Registered, 1 lines; 0 receipts") {
		t.Fatalf("asn_receiving_status ASN-1 = %q", text)
	}
	text = send(t, client, map[string]any{"skill": "asn_receiving_status", "arguments": map[string]any{"asnNumber": "ASN-2"}})
	if !strings.HasPrefix(text, "ASN-2 (ACME): Receiving, 1 lines; 1 receipts (1 open, 0 closed)") {
		t.Fatalf("asn_receiving_status ASN-2 = %q", text)
	}
	text = send(t, client, map[string]any{"skill": "dock_schedule", "arguments": map[string]any{"door": "DOOR-1", "state": "Booked"}})
	if text != "1 dock appointments (door DOOR-1, state Booked)" {
		t.Fatalf("dock_schedule = %q", text)
	}
	text = send(t, client, map[string]any{"skill": "asn_receiving_status", "arguments": map[string]any{"asnNumber": "ASN-404"}})
	if !strings.Contains(text, "(404)") {
		t.Fatalf("unknown ASN = %q, want an in-band 404 problem", text)
	}

	if after := countOutbox(t, url); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
	}
}

func clientFor(t *testing.T, endpoint string) *a2aclient.Client {
	t.Helper()
	card := &a2a.AgentCard{
		Name:                "inbound-receiving",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC)},
	}
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func send(t *testing.T, client *a2aclient.Client, req map[string]any) string {
	t.Helper()
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(req))})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		raw, _ := json.Marshal(resp)
		t.Fatalf("reply = %s", raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return string(text)
}
