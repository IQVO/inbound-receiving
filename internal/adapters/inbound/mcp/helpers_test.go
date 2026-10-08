package mcp_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/mcp"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// startOfDay is the fixture's fixed "now".
var startOfDay = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

type mutableClock struct{ now atomic.Pointer[time.Time] }

func (c *mutableClock) Now() time.Time  { return *c.now.Load() }
func (c *mutableClock) set(t time.Time) { c.now.Store(&t) }

// harness is a connected in-memory MCP client over the real server wired to
// in-memory repos, exactly as cmd/mcp wires them (minus Postgres). The write
// use cases exist ONLY here, to seed data the way the REST adapter would:
// the MCP surface itself has none.
type harness struct {
	session *sdk.ClientSession
	clock   *mutableClock
	outbox  *memory.OutboxRepo
	doors   *memory.DockDoors

	register *usecases.RegisterAsn
	cancel   *usecases.CancelAsn
	book     *usecases.BookAppointment
	checkIn  *usecases.CheckInAppointment
	cancelAp *usecases.CancelAppointment
	open     *usecases.OpenReceipt
	receive  *usecases.ReceiveLine
	closeRc  *usecases.CloseReceipt
}

func depsFor(asns ports.AsnRepository, appts ports.AppointmentRepository, receipts ports.ReceiptRepository,
	doors ports.DockDoorDirectory, mode usecases.Mode,
) inboundmcp.Deps {
	return inboundmcp.Deps{
		GetAsn:           &usecases.GetAsn{Asns: asns},
		ListAsns:         &usecases.ListAsns{Asns: asns},
		GetAppointment:   &usecases.GetAppointment{Appointments: appts},
		ListAppointments: &usecases.ListAppointments{Appointments: appts},
		GetReceipt:       &usecases.GetReceipt{Receipts: receipts},
		ListReceipts:     &usecases.ListReceipts{Receipts: receipts},
		ListDocks:        &usecases.ListDocks{Doors: doors, Mode: mode},
	}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed)
	clock := &mutableClock{}
	clock.set(startOfDay)
	w := usecases.Writer{
		Asns: asns, Appointments: appts, Receipts: receipts, Outbox: ob,
		Encoder: outboundkafka.NewEncoder(), UoW: uow, Clock: clock, IDs: idgen.UUID{},
	}
	return &harness{
		session: connectSession(t, depsFor(asns, appts, receipts, doors, usecases.ModeKafka)),
		clock:   clock, outbox: ob, doors: doors,
		register: &usecases.RegisterAsn{Writer: w},
		cancel:   &usecases.CancelAsn{Writer: w},
		book:     &usecases.BookAppointment{Writer: w},
		checkIn:  &usecases.CheckInAppointment{Writer: w},
		cancelAp: &usecases.CancelAppointment{Writer: w},
		open:     &usecases.OpenReceipt{Writer: w},
		receive:  &usecases.ReceiveLine{Writer: w},
		closeRc:  &usecases.CloseReceipt{Writer: w},
	}
}

// seedAsn registers an ASN with two lines (SKU-1 x40, SKU-2 x5).
func (h *harness) seedAsn(t *testing.T, number string) {
	t.Helper()
	_, err := h.register.Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", ExpectedArrival: startOfDay.Add(24 * time.Hour),
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5}},
	})
	if err != nil {
		t.Fatalf("register %s: %v", number, err)
	}
}

// seedAppointment books a 2 h window starting hour hours after the fixture
// clock and returns the appointment id.
func (h *harness) seedAppointment(t *testing.T, door string, hour int, asns ...string) string {
	t.Helper()
	start := startOfDay.Add(time.Duration(hour) * time.Hour)
	a, err := h.book.Handle(context.Background(), usecases.BookAppointmentCommand{
		DoorCode: door, Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(2 * time.Hour), AsnNumbers: asns,
	})
	if err != nil {
		t.Fatalf("book %s: %v", door, err)
	}
	return string(a.ID())
}

// seedReceipt opens a receipt for number (walk-in when appointmentID is "").
func (h *harness) seedReceipt(t *testing.T, number, appointmentID string) string {
	t.Helper()
	r, err := h.open.Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: number, AppointmentID: appointmentID})
	if err != nil {
		t.Fatalf("open receipt for %s: %v", number, err)
	}
	return string(r.ID())
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// ids returns the string field key of every item of a list result, in order.
func ids(page map[string]any, key string) []string {
	var out []string
	items, _ := page["items"].([]any)
	for _, item := range items {
		out = append(out, item.(map[string]any)[key].(string))
	}
	return out
}
