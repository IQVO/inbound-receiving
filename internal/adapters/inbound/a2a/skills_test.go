package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// startOfDay is the fixture's fixed "now".
var startOfDay = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// fixture holds in-memory repositories (test doubles: the shipped binary runs
// on Postgres) and the WRITE use cases that seed them the way the REST adapter
// would. The A2A surface itself has no write use case.
type fixture struct {
	asns     *memory.AsnRepo
	appts    *memory.AppointmentRepo
	receipts *memory.ReceiptRepo
	outbox   *memory.OutboxRepo
	clock    *testClock
	w        usecases.Writer
}

func newFixture() *fixture {
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	clock := &testClock{now: startOfDay}
	return &fixture{
		asns: asns, appts: appts, receipts: receipts, outbox: ob, clock: clock,
		w: usecases.Writer{
			Asns: asns, Appointments: appts, Receipts: receipts, Outbox: ob,
			Encoder: outboundkafka.NewEncoder(), UoW: memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed),
			Clock: clock, IDs: idgen.UUID{},
		},
	}
}

func (f *fixture) deps() Deps {
	return Deps{
		GetAsn:           &usecases.GetAsn{Asns: f.asns},
		ListReceipts:     &usecases.ListReceipts{Receipts: f.receipts},
		ListAppointments: &usecases.ListAppointments{Appointments: f.appts},
	}
}

func (f *fixture) seedAsn(t *testing.T, number string) {
	t.Helper()
	_, err := (&usecases.RegisterAsn{Writer: f.w}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", ExpectedArrival: startOfDay.Add(24 * time.Hour),
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5}},
	})
	if err != nil {
		t.Fatalf("register %s: %v", number, err)
	}
}

func (f *fixture) seedAppointment(t *testing.T, door string, hour int, asnNumber string) string {
	t.Helper()
	start := startOfDay.Add(time.Duration(hour) * time.Hour)
	a, err := (&usecases.BookAppointment{Writer: f.w}).Handle(context.Background(), usecases.BookAppointmentCommand{
		DoorCode: door, Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(2 * time.Hour), AsnNumbers: []string{asnNumber},
	})
	if err != nil {
		t.Fatalf("book %s: %v", door, err)
	}
	return string(a.ID())
}

func (f *fixture) openReceipt(t *testing.T, number, appointmentID string) string {
	t.Helper()
	r, err := (&usecases.OpenReceipt{Writer: f.w}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: number, AppointmentID: appointmentID})
	if err != nil {
		t.Fatalf("open receipt for %s: %v", number, err)
	}
	return string(r.ID())
}

// seedReceivedAsn leaves ASN-1 Closed after a receipt via an appointment with
// a Short line (38 of 40 good) and a Damaged line (5 damaged), and returns the
// receipt id.
func (f *fixture) seedReceivedAsn(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	f.seedAsn(t, "ASN-1")
	appt := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	f.clock.now = startOfDay.Add(time.Hour)
	if _, err := (&usecases.CheckInAppointment{Writer: f.w}).Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: appt}); err != nil {
		t.Fatalf("check in: %v", err)
	}
	id := f.openReceipt(t, "ASN-1", appt)
	for _, cmd := range []usecases.ReceiveLineCommand{
		{ReceiptID: id, LineNo: 1, Quantity: 38, Condition: "Good"},
		{ReceiptID: id, LineNo: 2, Quantity: 5, Condition: "Damaged"},
	} {
		if _, err := (&usecases.ReceiveLine{Writer: f.w}).Handle(ctx, cmd); err != nil {
			t.Fatalf("receive line %d: %v", cmd.LineNo, err)
		}
	}
	if _, err := (&usecases.CloseReceipt{Writer: f.w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id}); err != nil {
		t.Fatalf("close receipt: %v", err)
	}
	return id
}

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  struct {
		AsnReceivingStatus *wireStatus `json:"asnReceivingStatus"`
		DockSchedule       *struct {
			Appointments []wireAppointment `json:"appointments"`
			Truncated    bool              `json:"truncated"`
		} `json:"dockSchedule"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

type wireReceipt struct {
	ReceiptID     string `json:"receiptId"`
	AppointmentID string `json:"appointmentId"`
	DoorCode      string `json:"doorCode"`
	State         string `json:"state"`
	OpenedAt      string `json:"openedAt"`
	ClosedAt      string `json:"closedAt"`
	Lines         []struct {
		LineNo          int    `json:"lineNo"`
		SKU             string `json:"sku"`
		ExpectedQty     int64  `json:"expectedQty"`
		ReceivedGood    int64  `json:"receivedGood"`
		ReceivedDamaged int64  `json:"receivedDamaged"`
	} `json:"lines"`
	Discrepancies []struct {
		LineNo      int    `json:"lineNo"`
		SKU         string `json:"sku"`
		Kind        string `json:"kind"`
		ExpectedQty int64  `json:"expectedQty"`
		ReceivedQty int64  `json:"receivedQty"`
		DamagedQty  int64  `json:"damagedQty"`
	} `json:"discrepancies"`
}

type wireAppointment struct {
	AppointmentID string   `json:"appointmentId"`
	DoorCode      string   `json:"doorCode"`
	Carrier       string   `json:"carrier"`
	WindowStart   string   `json:"windowStart"`
	WindowEnd     string   `json:"windowEnd"`
	AsnNumbers    []string `json:"asnNumbers"`
	State         string   `json:"state"`
}

type wire struct {
	client *a2aclient.Client
}

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope and the
// one-line text summary of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d problem, got data: %+v", status, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != problemTypeBase+"/"+slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
}

// wireStatus is the asn_receiving_status payload of wireEnvelope.
type wireStatus = struct {
	Asn struct {
		AsnNumber       string `json:"asnNumber"`
		SupplierRef     string `json:"supplierRef"`
		ExpectedArrival string `json:"expectedArrival"`
		State           string `json:"state"`
		Lines           []struct {
			LineNo      int    `json:"lineNo"`
			SKU         string `json:"sku"`
			ExpectedQty int64  `json:"expectedQty"`
		} `json:"lines"`
	} `json:"asn"`
	Receipts     []wireReceipt `json:"receipts"`
	MoreReceipts bool          `json:"moreReceipts"`
}

func assertClosedAsn(t *testing.T, s *wireStatus) {
	t.Helper()
	a := s.Asn
	if a.AsnNumber != "ASN-1" || a.SupplierRef != "ACME" || a.State != "Closed" ||
		a.ExpectedArrival != "2026-10-10T07:00:00Z" || len(a.Lines) != 2 || a.Lines[0].SKU != "SKU-1" || a.Lines[0].ExpectedQty != 40 {
		t.Fatalf("asn = %+v", a)
	}
}

func assertClosedReceipt(t *testing.T, r wireReceipt, receiptID string) {
	t.Helper()
	if r.ReceiptID != receiptID || r.State != "Closed" || r.DoorCode != "DOOR-1" || !strings.HasPrefix(r.AppointmentID, "appt-") ||
		r.OpenedAt != "2026-10-09T08:00:00Z" || r.ClosedAt != "2026-10-09T08:00:00Z" {
		t.Fatalf("receipt = %+v", r)
	}
	if len(r.Lines) != 2 || r.Lines[0].ReceivedGood != 38 || r.Lines[1].ReceivedDamaged != 5 {
		t.Fatalf("receipt lines = %+v", r.Lines)
	}
	kinds := map[string]int{}
	for _, d := range r.Discrepancies {
		kinds[d.Kind]++
	}
	if !reflect.DeepEqual(kinds, map[string]int{"Short": 1, "Damaged": 1}) {
		t.Fatalf("discrepancies = %+v", r.Discrepancies)
	}
}

func TestWireAsnReceivingStatusReadsAClosedAsnWithItsDiscrepancies(t *testing.T) {
	f := newFixture()
	receiptID := f.seedReceivedAsn(t)
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1"})
	s := env.Data.AsnReceivingStatus
	if env.Problem != nil || s == nil || env.Skill != skillAsnReceivingStatus {
		t.Fatalf("envelope = %+v", env)
	}
	assertClosedAsn(t, s)
	if s.MoreReceipts || len(s.Receipts) != 1 {
		t.Fatalf("receipts = %+v", s.Receipts)
	}
	assertClosedReceipt(t, s.Receipts[0], receiptID)
	if text != "ASN-1 (ACME): Closed, 2 lines; 1 receipts (0 open, 1 closed); 2 discrepancies" {
		t.Fatalf("summary = %q", text)
	}
}

func TestWireAsnReceivingStatusOfAnAsnNobodyStartedReceiving(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-9")
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-9"})
	s := env.Data.AsnReceivingStatus
	if env.Problem != nil || s == nil || s.Asn.State != "Registered" || s.Receipts == nil || len(s.Receipts) != 0 || s.MoreReceipts {
		t.Fatalf("envelope = %+v, want a Registered ASN with an empty (non-null) receipts array", env)
	}
	if !strings.Contains(text, "0 receipts (0 open, 0 closed); 0 discrepancies") {
		t.Fatalf("summary = %q", text)
	}
}

func TestWireAsnReceivingStatusShowsAnOpenWalkInReceipt(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-2")
	id := f.openReceipt(t, "ASN-2", "")
	w := newWire(t, f.deps())

	env, _ := w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-2"})
	s := env.Data.AsnReceivingStatus
	if env.Problem != nil || s == nil || s.Asn.State != "Receiving" || len(s.Receipts) != 1 {
		t.Fatalf("envelope = %+v", env)
	}
	if r := s.Receipts[0]; r.ReceiptID != id || r.State != "Open" || r.AppointmentID != "" || r.DoorCode != "" || r.ClosedAt != "" {
		t.Fatalf("a walk-in open receipt must omit appointment, door and closedAt: %+v", r)
	}
}

// scheduleIDs asks dock_schedule and returns the appointment ids it answers.
func scheduleIDs(t *testing.T, w *wire, args map[string]any) []string {
	t.Helper()
	env, _ := w.skill(t, skillDockSchedule, args)
	if env.Problem != nil || env.Data.DockSchedule == nil {
		t.Fatalf("args %v: envelope = %+v", args, env)
	}
	var out []string
	for _, ap := range env.Data.DockSchedule.Appointments {
		out = append(out, ap.AppointmentID)
	}
	return out
}

func TestWireDockScheduleFiltersAndOrders(t *testing.T) {
	f := newFixture()
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		f.seedAsn(t, n)
	}
	a := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	b := f.seedAppointment(t, "DOOR-2", 1, "ASN-2")
	c := f.seedAppointment(t, "DOOR-1", 5, "ASN-3")
	if _, err := (&usecases.CancelAppointment{Writer: f.w}).Handle(context.Background(), usecases.AppointmentActionCommand{AppointmentID: c, Reason: "carrier no-show"}); err != nil {
		t.Fatal(err)
	}
	w := newWire(t, f.deps())

	if all := scheduleIDs(t, w, map[string]any{}); len(all) != 3 || all[2] != c {
		t.Fatalf("all = %v, want the two 08:00 windows then C (window-start order)", all)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want []string
	}{
		"door":                   {map[string]any{"door": "DOOR-1"}, []string{a, c}},
		"state":                  {map[string]any{"state": "Cancelled"}, []string{c}},
		"door and state":         {map[string]any{"door": "DOOR-1", "state": "Booked"}, []string{a}},
		"from keeps later ones":  {map[string]any{"from": "2026-10-09T11:00:00Z"}, []string{c}},
		"offset from normalised": {map[string]any{"from": "2026-10-09T08:00:00-03:00"}, []string{c}},
		"limit":                  {map[string]any{"limit": 1, "door": "DOOR-2"}, []string{b}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := scheduleIDs(t, w, tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%v = %v, want %v", tc.args, got, tc.want)
			}
		})
	}

	env, text := w.skill(t, skillDockSchedule, map[string]any{"door": "DOOR-1", "state": "Booked"})
	ap := env.Data.DockSchedule.Appointments[0]
	if ap.DoorCode != "DOOR-1" || ap.Carrier != "ACME Freight" || ap.WindowStart != "2026-10-09T08:00:00Z" || ap.WindowEnd != "2026-10-09T10:00:00Z" ||
		ap.State != "Booked" || !reflect.DeepEqual(ap.AsnNumbers, []string{"ASN-1"}) {
		t.Fatalf("appointment = %+v", ap)
	}
	if text != "1 dock appointments (door DOOR-1, state Booked)" {
		t.Fatalf("summary = %q", text)
	}
}

func TestWireDockScheduleSaysWhenItIsTruncated(t *testing.T) {
	f := newFixture()
	for i, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		f.seedAsn(t, n)
		f.seedAppointment(t, "DOOR-"+string(rune('1'+i)), 1, n)
	}
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillDockSchedule, map[string]any{"limit": 2})
	d := env.Data.DockSchedule
	if env.Problem != nil || d == nil || len(d.Appointments) != 2 || !d.Truncated {
		t.Fatalf("envelope = %+v, want 2 appointments and truncated", env)
	}
	if !strings.HasSuffix(text, "; more exist, narrow the range") {
		t.Fatalf("summary = %q", text)
	}
	env, _ = w.skill(t, skillDockSchedule, map[string]any{"limit": 3})
	if d := env.Data.DockSchedule; len(d.Appointments) != 3 || d.Truncated {
		t.Fatalf("a page that holds everything is not truncated: %+v", d)
	}
}

func TestWireEmptyScheduleIsAnEmptyListNotAProblem(t *testing.T) {
	w := newWire(t, newFixture().deps())

	env, _ := w.skill(t, skillDockSchedule, map[string]any{})
	d := env.Data.DockSchedule
	if env.Problem != nil || d == nil || d.Appointments == nil || len(d.Appointments) != 0 || d.Truncated {
		t.Fatalf("envelope = %+v, want an empty appointments array", env)
	}
}

// Domain rejections are answered in-band with the same status and slug the
// REST adapter gives the same request.
func TestWireDomainRejectionsAreAnsweredInBand(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")
	w := newWire(t, f.deps())

	cases := map[string]struct {
		skill  string
		args   map[string]any
		status int
		slug   string
	}{
		"unknown asn":          {skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-404"}, 404, "asn-not-found"},
		"malformed asn number": {skillAsnReceivingStatus, map[string]any{"asnNumber": "has a space"}, 400, "invalid-asn-number"},
		"unknown state":        {skillDockSchedule, map[string]any{"state": "Bogus"}, 400, "invalid-query"},
		"bad from":             {skillDockSchedule, map[string]any{"from": "yesterday"}, 400, "invalid-query"},
		"bad to":               {skillDockSchedule, map[string]any{"to": "2026-10-09"}, 400, "invalid-query"},
		"malformed door":       {skillDockSchedule, map[string]any{"door": "DOOR/1"}, 400, "invalid-query"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, tc.status, tc.slug)
			if text == "" || !strings.Contains(text, env.Problem.Title) {
				t.Fatalf("text summary = %q, want the problem line", text)
			}
		})
	}

	env, _ := w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-404"})
	if env.Problem.Detail != `no ASN exists with asnNumber "ASN-404"` {
		t.Fatalf("detail = %q", env.Problem.Detail)
	}
}

// failing* make a read fail, standing in for a database outage.
type failingAsns struct{ ports.AsnRepository }

var errBoom = errors.New("pgx: connection refused to 10.0.0.7:5432 password=hunter2")

func (failingAsns) Get(context.Context, asn.Number) (*asn.Asn, error) { return nil, errBoom }

type failingReceipts struct{ ports.ReceiptRepository }

func (failingReceipts) List(context.Context, repository.ReceiptFilter, receipt.ID, int) ([]*receipt.Receipt, error) {
	return nil, errBoom
}

type failingAppointments struct{ ports.AppointmentRepository }

func (failingAppointments) List(context.Context, repository.AppointmentFilter, repository.AppointmentCursor, int) ([]*appointment.DockAppointment, error) {
	return nil, errBoom
}

// An unexpected failure is answered as a 500 problem, never a JSON-RPC error,
// and its raw text (which may carry infrastructure detail) is not echoed. The
// receipts read failing AFTER the ASN was found is covered too.
func TestWireUnexpectedFailureIs500AndDoesNotLeakTheCause(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")

	cases := map[string]struct {
		deps  Deps
		skill string
		args  map[string]any
	}{
		"asn read": {
			Deps{GetAsn: &usecases.GetAsn{Asns: failingAsns{f.asns}}, ListReceipts: &usecases.ListReceipts{Receipts: f.receipts}},
			skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1"},
		},
		"receipts read": {
			Deps{GetAsn: &usecases.GetAsn{Asns: f.asns}, ListReceipts: &usecases.ListReceipts{Receipts: failingReceipts{f.receipts}}},
			skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1"},
		},
		"appointments read": {
			Deps{ListAppointments: &usecases.ListAppointments{Appointments: failingAppointments{f.appts}}},
			skillDockSchedule, map[string]any{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, text := newWire(t, tc.deps).skill(t, tc.skill, tc.args)
			wantProblem(t, env, 500, "internal-error")
			for _, s := range []string{env.Problem.Detail, env.Problem.Title, text} {
				if strings.Contains(s, "hunter2") || strings.Contains(s, "10.0.0.7") {
					t.Fatalf("a 500 answer leaks the underlying error: %q", s)
				}
			}
		})
	}
}

func TestWireArgumentValidation(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")
	w := newWire(t, f.deps())
	cases := map[string]struct {
		skill string
		args  map[string]any
	}{
		"status: missing asnNumber":    {skillAsnReceivingStatus, map[string]any{}},
		"status: blank asnNumber":      {skillAsnReceivingStatus, map[string]any{"asnNumber": "   "}},
		"status: asnNumber wrong type": {skillAsnReceivingStatus, map[string]any{"asnNumber": 7}},
		"status: unknown argument":     {skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1", "force": true}},
		"status: typo'd argument":      {skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1", "asnNumbr": "ASN-1"}},
		"status: snake_case argument":  {skillAsnReceivingStatus, map[string]any{"asn_number": "ASN-1"}},
		"schedule: unknown argument":   {skillDockSchedule, map[string]any{"doorr": "DOOR-1"}},
		"schedule: snake_case limit":   {skillDockSchedule, map[string]any{"next_cursor": "x"}},
		"schedule: cursor not offered": {skillDockSchedule, map[string]any{"cursor": "x"}},
		"schedule: wrong-skill arg":    {skillDockSchedule, map[string]any{"asnNumber": "ASN-1"}},
		"schedule: limit a string":     {skillDockSchedule, map[string]any{"limit": "5"}},
		"schedule: limit zero":         {skillDockSchedule, map[string]any{"limit": 0}},
		"schedule: limit negative":     {skillDockSchedule, map[string]any{"limit": -1}},
		"schedule: limit over the cap": {skillDockSchedule, map[string]any{"limit": 101}},
		"schedule: door wrong type":    {skillDockSchedule, map[string]any{"door": 3}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, _ := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 400, "invalid-argument")
		})
	}
}

func TestWireRequestShapeProblems(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")
	w := newWire(t, f.deps())

	env, _ := w.call(t, a2a.NewTextPart("not json at all"))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"skill": skillDockSchedule, "arguments": map[string]any{}, "extra": 1}))
	wantProblem(t, env, 400, "malformed-skill-request")

	// Trailing data after the JSON document is refused too.
	env, _ = w.call(t, a2a.NewTextPart(`{"skill":"dock_schedule","arguments":{}} {"skill":"asn_receiving_status"}`))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.skill(t, "launch_rocket", map[string]any{})
	wantProblem(t, env, 400, "unknown-skill")

	// A text part holding the same JSON is accepted (documented contract).
	env, _ = w.call(t, a2a.NewTextPart(`{"skill":"asn_receiving_status","arguments":{"asnNumber":"ASN-1"}}`))
	if env.Problem != nil || env.Data.AsnReceivingStatus == nil || env.Data.AsnReceivingStatus.Asn.AsnNumber != "ASN-1" {
		t.Fatalf("a text part holding the skill JSON must be accepted, got %+v", env)
	}
}

// The writes of the REST surface (and the MCP tool names) are not offered here:
// asking for one is an unknown skill, answered in-band, and nothing is written.
func TestWireOffersNoWriteSkill(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")
	rows := len(f.outbox.Messages())
	w := newWire(t, f.deps())

	for _, skill := range []string{"register_asn", "cancel_asn", "book_appointment", "check_in_appointment", "cancel_appointment", "open_receipt", "receive_line", "close_receipt", "get_asn", "list_docks"} {
		env, _ := w.skill(t, skill, map[string]any{"asnNumber": "ASN-NEW"})
		wantProblem(t, env, 400, "unknown-skill")
	}
	if _, err := f.asns.Get(context.Background(), "ASN-NEW"); err == nil {
		t.Fatal("a write skill name must not write anything")
	}
	// Reading adds nothing to the outbox either.
	w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1"})
	w.skill(t, skillDockSchedule, map[string]any{})
	if got := len(f.outbox.Messages()); got != rows {
		t.Fatalf("outbox rows %d -> %d: a read-only skill wrote an event", rows, got)
	}
}

// TestCardIsServedAndDialable proves discovery end to end: the card handler
// serves the card at the well-known path and a client built from the FETCHED
// card reaches the agent.
func TestCardIsServedAndDialable(t *testing.T) {
	f := newFixture()
	f.seedAsn(t, "ASN-1")
	mux := http.NewServeMux()
	mux.Handle("/", Handler(NewExecutor(f.deps())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(Card(Config{AgentURL: srv.URL + "/", Version: "test"})))

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{client: client}
	env, _ := w.skill(t, skillAsnReceivingStatus, map[string]any{"asnNumber": "ASN-1"})
	if env.Data.AsnReceivingStatus == nil {
		t.Fatalf("a client built from the served card could not call the agent: %+v", env)
	}
}
