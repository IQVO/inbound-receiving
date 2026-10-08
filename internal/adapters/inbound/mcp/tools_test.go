package mcp_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const (
	missingAppointment = "appt-00000000-0000-4000-8000-0000000000ff"
	missingReceipt     = "rcpt-00000000-0000-4000-8000-0000000000ff"
)

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestGetAsn(t *testing.T) {
	h := newHarness(t)
	h.seedAsn(t, "ASN-1001")

	got := h.ok(t, "get_asn", map[string]any{"asn_number": "ASN-1001"})
	if got["asn_number"] != "ASN-1001" || got["supplier_ref"] != "ACME" || got["state"] != "Registered" ||
		got["version"] != 1.0 || got["expected_arrival"] != "2026-10-10T07:00:00Z" {
		t.Fatalf("asn = %v", got)
	}
	lines, _ := got["lines"].([]any)
	want := []any{
		map[string]any{"line_no": 1.0, "sku": "SKU-1", "expected_qty": 40.0},
		map[string]any{"line_no": 2.0, "sku": "SKU-2", "expected_qty": 5.0},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

func TestGetAsn_OmitsExpectedArrivalWhenNone(t *testing.T) {
	h := newHarness(t)
	if _, err := h.register.Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: "ASN-NOETA", SupplierRef: "ACME",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	got := h.ok(t, "get_asn", map[string]any{"asn_number": "ASN-NOETA"})
	if _, has := got["expected_arrival"]; has {
		t.Fatalf("expected_arrival must be omitted when unset: %v", got)
	}
}

func TestGetAsn_Errors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_asn", map[string]any{"asn_number": "ASN-404"}, "asn-not-found")
	failWith(t, h.session, "get_asn", map[string]any{"asn_number": ""}, "invalid-asn-number")
	failWith(t, h.session, "get_asn", map[string]any{"asn_number": "not valid!"}, "invalid-asn-number")
}

func TestListAsns_FiltersAndPages(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"ASN-A", "ASN-B", "ASN-C"} {
		h.seedAsn(t, n)
	}
	if _, err := h.cancel.Handle(context.Background(), usecases.CancelAsnCommand{AsnNumber: "ASN-C", Reason: "supplier withdrew"}); err != nil {
		t.Fatal(err)
	}

	all := h.ok(t, "list_asns", map[string]any{})
	if got := ids(all, "asn_number"); !reflect.DeepEqual(got, []string{"ASN-A", "ASN-B", "ASN-C"}) {
		t.Fatalf("all = %v", got)
	}
	if _, has := all["next_cursor"]; has {
		t.Fatalf("a last page must omit next_cursor: %v", all)
	}

	first := h.ok(t, "list_asns", map[string]any{"limit": 2})
	if got := ids(first, "asn_number"); !reflect.DeepEqual(got, []string{"ASN-A", "ASN-B"}) || first["next_cursor"] == nil {
		t.Fatalf("first page = %v (cursor %v)", got, first["next_cursor"])
	}
	second := h.ok(t, "list_asns", map[string]any{"limit": 2, "cursor": first["next_cursor"]})
	if got := ids(second, "asn_number"); !reflect.DeepEqual(got, []string{"ASN-C"}) {
		t.Fatalf("second page = %v", got)
	}

	if got := ids(h.ok(t, "list_asns", map[string]any{"state": "Cancelled"}), "asn_number"); !reflect.DeepEqual(got, []string{"ASN-C"}) {
		t.Fatalf("state=Cancelled = %v", got)
	}
	if got := ids(h.ok(t, "list_asns", map[string]any{"state": "Registered"}), "asn_number"); !reflect.DeepEqual(got, []string{"ASN-A", "ASN-B"}) {
		t.Fatalf("state=Registered = %v", got)
	}
}

func TestListAsns_EmptyIsAnEmptyArray(t *testing.T) {
	got := newHarness(t).ok(t, "list_asns", map[string]any{"state": "Closed"})
	if items, isSlice := got["items"].([]any); !isSlice || len(items) != 0 {
		t.Fatalf("items = %#v, want []", got["items"])
	}
}

func TestListAsns_MalformedQueries(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string]map[string]any{
		"unknown state":  {"state": "Bogus"},
		"limit too big":  {"limit": 501},
		"negative limit": {"limit": -1},
		"bad cursor":     {"cursor": "!!not-base64!!"},
	} {
		t.Run(name, func(t *testing.T) {
			failWith(t, h.session, "list_asns", args, "invalid-query")
		})
	}
}

// seedAppointments books A (DOOR-1 08:00), B (DOOR-2 08:00) and C (DOOR-1
// 12:00, then cancelled) for ASN-1, ASN-2 and ASN-3.
func seedAppointments(t *testing.T, h *harness) (a, b, c string) {
	t.Helper()
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		h.seedAsn(t, n)
	}
	a = h.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	b = h.seedAppointment(t, "DOOR-2", 1, "ASN-2")
	c = h.seedAppointment(t, "DOOR-1", 5, "ASN-3")
	if _, err := h.cancelAp.Handle(context.Background(), usecases.AppointmentActionCommand{AppointmentID: c, Reason: "carrier no-show"}); err != nil {
		t.Fatal(err)
	}
	return a, b, c
}

func TestGetAppointment(t *testing.T) {
	h := newHarness(t)
	a, _, _ := seedAppointments(t, h)

	got := h.ok(t, "get_appointment", map[string]any{"appointment_id": a})
	if got["appointment_id"] != a || got["door_code"] != "DOOR-1" || got["carrier"] != "ACME Freight" ||
		got["window_start"] != "2026-10-09T08:00:00Z" || got["window_end"] != "2026-10-09T10:00:00Z" ||
		got["state"] != "Booked" || got["version"] != 1.0 || !reflect.DeepEqual(got["asn_numbers"], []any{"ASN-1"}) {
		t.Fatalf("appointment = %v", got)
	}
}

func TestGetAppointment_Errors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_appointment", map[string]any{"appointment_id": missingAppointment}, "appointment-not-found")
	failWith(t, h.session, "get_appointment", map[string]any{"appointment_id": "nope"}, "invalid-appointment-id")
	failWith(t, h.session, "get_appointment", map[string]any{"appointment_id": ""}, "invalid-appointment-id")
}

func TestListAppointments_FiltersAndPages(t *testing.T) {
	h := newHarness(t)
	a, b, c := seedAppointments(t, h)

	all := ids(h.ok(t, "list_appointments", map[string]any{}), "appointment_id")
	if len(all) != 3 || all[2] != c || !reflect.DeepEqual(sorted(all[:2]), sorted([]string{a, b})) {
		t.Fatalf("all = %v, want [A B in either order, then C] (window start order)", all)
	}

	if got := ids(h.ok(t, "list_appointments", map[string]any{"door": "DOOR-1"}), "appointment_id"); !reflect.DeepEqual(got, []string{a, c}) {
		t.Fatalf("door=DOOR-1 = %v, want [%s %s]", got, a, c)
	}
	if got := ids(h.ok(t, "list_appointments", map[string]any{"state": "Cancelled"}), "appointment_id"); !reflect.DeepEqual(got, []string{c}) {
		t.Fatalf("state=Cancelled = %v", got)
	}
	if got := ids(h.ok(t, "list_appointments", map[string]any{"door": "DOOR-1", "state": "Booked"}), "appointment_id"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("door=DOOR-1&state=Booked = %v", got)
	}
	// from: window ends after it. 11:00 excludes the 08:00-10:00 windows.
	if got := ids(h.ok(t, "list_appointments", map[string]any{"from": "2026-10-09T11:00:00Z"}), "appointment_id"); !reflect.DeepEqual(got, []string{c}) {
		t.Fatalf("from=11:00 = %v", got)
	}
	// to: window starts before it. 09:00 excludes the 12:00 window.
	if got := ids(h.ok(t, "list_appointments", map[string]any{"to": "2026-10-09T09:00:00Z"}), "appointment_id"); !reflect.DeepEqual(sorted(got), sorted([]string{a, b})) {
		t.Fatalf("to=09:00 = %v", got)
	}
	// An offset instant is normalised.
	if got := ids(h.ok(t, "list_appointments", map[string]any{"from": "2026-10-09T08:00:00-03:00"}), "appointment_id"); !reflect.DeepEqual(got, []string{c}) {
		t.Fatalf("from=08:00-03:00 (11:00Z) = %v", got)
	}

	first := h.ok(t, "list_appointments", map[string]any{"limit": 2})
	second := h.ok(t, "list_appointments", map[string]any{"limit": 2, "cursor": first["next_cursor"]})
	if len(ids(first, "appointment_id")) != 2 || first["next_cursor"] == nil ||
		!reflect.DeepEqual(ids(second, "appointment_id"), []string{c}) {
		t.Fatalf("pages = %v / %v", first, second)
	}
	if _, has := second["next_cursor"]; has {
		t.Fatalf("the last page must omit next_cursor: %v", second)
	}
}

func TestListAppointments_MalformedQueries(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		args map[string]any
		slug string
	}{
		"unknown state":  {map[string]any{"state": "Bogus"}, "invalid-query"},
		"bad from":       {map[string]any{"from": "yesterday"}, "invalid-query"},
		"bad to":         {map[string]any{"to": "2026-10-09"}, "invalid-query"},
		"limit too big":  {map[string]any{"limit": 501}, "invalid-query"},
		"bad cursor":     {map[string]any{"cursor": "!!not-base64!!"}, "invalid-query"},
		"malformed door": {map[string]any{"door": "DOOR/1"}, "invalid-query"},
	} {
		t.Run(name, func(t *testing.T) {
			failWith(t, h.session, "list_appointments", tc.args, tc.slug)
		})
	}
}

// seedReceipts closes a receipt with a short line and a damaged line (via an
// appointment, checked in at its window start) and leaves a walk-in one open.
func seedReceipts(t *testing.T, h *harness) (closed, open string) {
	t.Helper()
	ctx := context.Background()
	a, _, _ := seedAppointments(t, h)
	h.clock.set(startOfDay.Add(time.Hour))
	if _, err := h.checkIn.Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: a}); err != nil {
		t.Fatalf("check in: %v", err)
	}
	closed = h.seedReceipt(t, "ASN-1", a)
	for _, cmd := range []usecases.ReceiveLineCommand{
		{ReceiptID: closed, LineNo: 1, Quantity: 38, Condition: "Good"},
		{ReceiptID: closed, LineNo: 2, Quantity: 5, Condition: "Damaged"},
	} {
		if _, err := h.receive.Handle(ctx, cmd); err != nil {
			t.Fatalf("receive line %d: %v", cmd.LineNo, err)
		}
	}
	if _, err := h.closeRc.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: closed}); err != nil {
		t.Fatalf("close: %v", err)
	}
	open = h.seedReceipt(t, "ASN-2", "")
	return closed, open
}

func TestGetReceipt_ClosedWithDiscrepancies(t *testing.T) {
	h := newHarness(t)
	closed, _ := seedReceipts(t, h)

	got := h.ok(t, "get_receipt", map[string]any{"receipt_id": closed})
	if got["receipt_id"] != closed || got["asn_number"] != "ASN-1" || got["door_code"] != "DOOR-1" ||
		got["state"] != "Closed" || got["opened_at"] != "2026-10-09T08:00:00Z" || got["closed_at"] != "2026-10-09T08:00:00Z" {
		t.Fatalf("receipt = %v", got)
	}
	if appt, _ := got["appointment_id"].(string); !strings.HasPrefix(appt, "appt-") {
		t.Fatalf("appointment_id = %v", got["appointment_id"])
	}
	lines, _ := got["lines"].([]any)
	wantLines := []any{
		map[string]any{"line_no": 1.0, "sku": "SKU-1", "expected_qty": 40.0, "received_good": 38.0, "received_damaged": 0.0},
		map[string]any{"line_no": 2.0, "sku": "SKU-2", "expected_qty": 5.0, "received_good": 0.0, "received_damaged": 5.0},
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("lines = %v, want %v", lines, wantLines)
	}
	ds, _ := got["discrepancies"].([]any)
	if len(ds) == 0 {
		t.Fatalf("a closed receipt with a short and a damaged line must report discrepancies: %v", got)
	}
	kinds := map[string]bool{}
	for _, d := range ds {
		dm := d.(map[string]any)
		kinds[dm["kind"].(string)] = true
		for _, key := range []string{"line_no", "sku", "kind", "expected_qty", "received_qty", "damaged_qty"} {
			if _, has := dm[key]; !has {
				t.Fatalf("discrepancy %v lacks %s", dm, key)
			}
		}
	}
	if !kinds["Short"] || !kinds["Damaged"] {
		t.Fatalf("discrepancy kinds = %v, want Short and Damaged", kinds)
	}
}

func TestGetReceipt_OpenWalkInOmitsOrigin(t *testing.T) {
	h := newHarness(t)
	_, open := seedReceipts(t, h)

	got := h.ok(t, "get_receipt", map[string]any{"receipt_id": open})
	if got["state"] != "Open" || got["asn_number"] != "ASN-2" {
		t.Fatalf("receipt = %v", got)
	}
	for _, absent := range []string{"appointment_id", "door_code", "closed_at"} {
		if _, has := got[absent]; has {
			t.Fatalf("%s must be omitted for an open walk-in receipt: %v", absent, got)
		}
	}
	// Discrepancies are computed as the receipt stands, like the REST body: an
	// open receipt with nothing received yet is Short on every line.
	if ds, isSlice := got["discrepancies"].([]any); !isSlice || len(ds) != 2 {
		t.Fatalf("discrepancies = %#v, want the 2 provisional Short entries", got["discrepancies"])
	}
}

func TestGetReceipt_Errors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_receipt", map[string]any{"receipt_id": missingReceipt}, "receipt-not-found")
	failWith(t, h.session, "get_receipt", map[string]any{"receipt_id": "nope"}, "invalid-receipt-id")
	failWith(t, h.session, "get_receipt", map[string]any{"receipt_id": ""}, "invalid-receipt-id")
}

func TestListReceipts_Filters(t *testing.T) {
	h := newHarness(t)
	closed, open := seedReceipts(t, h)

	all := ids(h.ok(t, "list_receipts", map[string]any{}), "receipt_id")
	if !reflect.DeepEqual(sorted(all), sorted([]string{closed, open})) {
		t.Fatalf("all = %v", all)
	}
	if got := ids(h.ok(t, "list_receipts", map[string]any{"state": "Open"}), "receipt_id"); !reflect.DeepEqual(got, []string{open}) {
		t.Fatalf("state=Open = %v", got)
	}
	if got := ids(h.ok(t, "list_receipts", map[string]any{"asn_number": "ASN-1"}), "receipt_id"); !reflect.DeepEqual(got, []string{closed}) {
		t.Fatalf("asn_number=ASN-1 = %v", got)
	}
	if got := ids(h.ok(t, "list_receipts", map[string]any{"asn_number": "ASN-1", "state": "Open"}), "receipt_id"); len(got) != 0 {
		t.Fatalf("asn_number=ASN-1&state=Open = %v, want none", got)
	}

	first := h.ok(t, "list_receipts", map[string]any{"limit": 1})
	second := h.ok(t, "list_receipts", map[string]any{"limit": 1, "cursor": first["next_cursor"]})
	if len(ids(first, "receipt_id")) != 1 || first["next_cursor"] == nil || len(ids(second, "receipt_id")) != 1 ||
		ids(first, "receipt_id")[0] == ids(second, "receipt_id")[0] {
		t.Fatalf("pages = %v / %v", first, second)
	}
}

func TestListReceipts_MalformedQueries(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "list_receipts", map[string]any{"state": "Bogus"}, "invalid-query")
	failWith(t, h.session, "list_receipts", map[string]any{"limit": 501}, "invalid-query")
	failWith(t, h.session, "list_receipts", map[string]any{"cursor": "!!not-base64!!"}, "invalid-query")
	failWith(t, h.session, "list_receipts", map[string]any{"asn_number": "not valid!"}, "invalid-query")
}

func TestListDocks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, d := range []repository.DockDoor{
		{Code: "DOOR-2", Flow: repository.DockFlowBoth},
		{Code: "DOOR-1", Flow: repository.DockFlowInbound},
	} {
		if err := h.doors.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got := h.ok(t, "list_docks", map[string]any{})
	want := []any{
		map[string]any{"door_code": "DOOR-1", "dock_flow": "Inbound"},
		map[string]any{"door_code": "DOOR-2", "dock_flow": "Both"},
	}
	if got["mode"] != "kafka" || !reflect.DeepEqual(got["items"], want) {
		t.Fatalf("docks = %v, want mode kafka and %v", got, want)
	}
	// A client that sends no arguments at all gets the same answer.
	res := call(t, h.session, "list_docks", nil)
	if res.IsError || !reflect.DeepEqual(res.StructuredContent, got) {
		t.Fatalf("list_docks without arguments = %+v (%s), want %v", res.StructuredContent, text(res), got)
	}
}

func TestListDocks_PermissiveAndEmpty(t *testing.T) {
	session := connectSession(t, depsFor(memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo(),
		memory.NewDockDoors(), usecases.ModePermissive))
	res := call(t, session, "list_docks", map[string]any{})
	got, _ := res.StructuredContent.(map[string]any)
	if res.IsError || got["mode"] != "permissive" {
		t.Fatalf("docks = %+v (%s)", res.StructuredContent, text(res))
	}
	if items, isSlice := got["items"].([]any); !isSlice || len(items) != 0 {
		t.Fatalf("items = %#v, want []", got["items"])
	}
}

// Reading never writes: no outbox row is added and no version moves, so the
// MCP surface can never publish an event (docs/adr/0005).
func TestToolsNeverWrite(t *testing.T) {
	h := newHarness(t)
	closed, open := seedReceipts(t, h)
	before := len(h.outbox.Messages())

	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"get_asn", map[string]any{"asn_number": "ASN-1"}},
		{"list_asns", map[string]any{}},
		{"get_appointment", map[string]any{"appointment_id": missingAppointment}},
		{"list_appointments", map[string]any{}},
		{"get_receipt", map[string]any{"receipt_id": closed}},
		{"list_receipts", map[string]any{}},
		{"list_docks", map[string]any{}},
		{"get_asn", map[string]any{"asn_number": "ASN-404"}},
	} {
		call(t, h.session, c.name, c.args)
	}
	if after := len(h.outbox.Messages()); after != before {
		t.Fatalf("outbox rows %d -> %d: a read tool wrote an event", before, after)
	}
	if got := h.ok(t, "get_receipt", map[string]any{"receipt_id": open}); got["version"] != 1.0 {
		t.Fatalf("version moved: %v", got["version"])
	}
}

// failing repositories answer every read with an infrastructure error
// carrying a would-be secret.
var errSecret = errors.New("postgres dsn secret unreachable")

type failingAsns struct{ *memory.AsnRepo }

func (failingAsns) Get(context.Context, asn.Number) (*asn.Asn, error) { return nil, errSecret }
func (failingAsns) List(context.Context, repository.AsnFilter, asn.Number, int) ([]*asn.Asn, error) {
	return nil, errSecret
}

type failingAppointments struct{ *memory.AppointmentRepo }

func (failingAppointments) Get(context.Context, appointment.ID) (*appointment.DockAppointment, error) {
	return nil, errSecret
}

func (failingAppointments) List(context.Context, repository.AppointmentFilter, repository.AppointmentCursor, int) ([]*appointment.DockAppointment, error) {
	return nil, errSecret
}

type failingReceipts struct{ *memory.ReceiptRepo }

func (failingReceipts) Get(context.Context, receipt.ID) (*receipt.Receipt, error) {
	return nil, errSecret
}
func (failingReceipts) List(context.Context, repository.ReceiptFilter, receipt.ID, int) ([]*receipt.Receipt, error) {
	return nil, errSecret
}

type failingDoors struct{ *memory.DockDoors }

func (failingDoors) List(context.Context) ([]repository.DockDoor, error) { return nil, errSecret }

func TestUnexpectedErrorsAreGenericAndLeakNothing(t *testing.T) {
	session := connectSession(t, depsFor(
		failingAsns{memory.NewAsnRepo()}, failingAppointments{memory.NewAppointmentRepo()},
		failingReceipts{memory.NewReceiptRepo()}, failingDoors{memory.NewDockDoors()}, usecases.ModePermissive))
	for name, args := range map[string]map[string]any{
		"get_asn":           {"asn_number": "ASN-1"},
		"list_asns":         {},
		"get_appointment":   {"appointment_id": missingAppointment},
		"list_appointments": {},
		"get_receipt":       {"receipt_id": missingReceipt},
		"list_receipts":     {},
		"list_docks":        {},
	} {
		res := call(t, session, name, args)
		if !res.IsError || strings.Contains(text(res), "secret") || !strings.HasPrefix(text(res), "internal-error: ") {
			t.Fatalf("%s: %q, want a generic internal-error", name, text(res))
		}
	}
}
