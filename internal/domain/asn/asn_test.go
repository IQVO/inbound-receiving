package asn

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)

func twoLines() []LineInput {
	return []LineInput{
		{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40},
		{LineNo: 2, SKU: "SKU-2", ExpectedQty: 5},
	}
}

func registered(t *testing.T) *Asn {
	t.Helper()
	a, _, err := Register("ASN-1001", "ACME", t0.Add(24*time.Hour), twoLines(), t0)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return a
}

func onlyEvent(t *testing.T, events []Event) Event {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	return events[0]
}

func TestRegister(t *testing.T) {
	local := t0.In(time.FixedZone("BRT", -3*3600))
	arrival := time.Date(2026, 10, 9, 8, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	a, events, err := Register("ASN-1001", "ACME", arrival, twoLines(), local)
	if err != nil {
		t.Fatal(err)
	}
	if a.Number() != "ASN-1001" || a.SupplierRef() != "ACME" || a.State() != Registered || a.Version() != 1 {
		t.Fatalf("asn = %+v", a)
	}
	if !a.ExpectedArrival().Equal(arrival) || a.ExpectedArrival().Location() != time.UTC {
		t.Fatalf("expected arrival = %v", a.ExpectedArrival())
	}
	assertTwoLines(t, a.Lines())
	e, ok := onlyEvent(t, events).(ASNRegistered)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "ASNRegistered", t0)
	if e.SupplierRef != "ACME" || !e.ExpectedArrival.Equal(arrival) || len(e.Lines) != 2 {
		t.Fatalf("event = %+v", e)
	}
}

func assertTwoLines(t *testing.T, lines []Line) {
	t.Helper()
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	want := twoLines()
	for i, l := range lines {
		if l.LineNo() != want[i].LineNo || string(l.SKU()) != want[i].SKU || l.ExpectedQty() != want[i].ExpectedQty {
			t.Fatalf("line %d = %+v, want %+v", i, l, want[i])
		}
	}
}

func assertHeader(t *testing.T, e Event, name string, at time.Time) {
	t.Helper()
	if e.EventName() != name || e.AsnNumber() != "ASN-1001" || !e.OccurredAt().Equal(at) || e.OccurredAt().Location() != time.UTC {
		t.Fatalf("event = %s asn=%s at=%v (%v), want %s at %v UTC", e.EventName(), e.AsnNumber(), e.OccurredAt(), e.OccurredAt().Location(), name, at)
	}
}

func TestRegisterWithoutExpectedArrival(t *testing.T) {
	a, events, err := Register("ASN-1", "ACME", time.Time{}, twoLines(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !a.ExpectedArrival().IsZero() || !events[0].(ASNRegistered).ExpectedArrival.IsZero() {
		t.Fatal("a zero expected arrival must stay unset")
	}
}

func TestRegisterRejects(t *testing.T) {
	long := strings.Repeat("x", MaxNumberLength+1)
	cases := []struct {
		name     string
		number   string
		supplier string
		lines    []LineInput
		want     error
	}{
		{"bad number", "ASN 1", "ACME", twoLines(), ErrInvalidNumber},
		{"empty number", "", "ACME", twoLines(), ErrInvalidNumber},
		{"long number", long, "ACME", twoLines(), ErrInvalidNumber},
		{"blank supplier", "ASN-1", "  ", twoLines(), ErrInvalidSupplierRef},
		{"empty supplier", "ASN-1", "", twoLines(), ErrInvalidSupplierRef},
		{"long supplier", "ASN-1", strings.Repeat("s", MaxSupplierRefLength+1), twoLines(), ErrInvalidSupplierRef},
		{"control supplier", "ASN-1", "AC\nME", twoLines(), ErrInvalidSupplierRef},
		{"leading control supplier", "ASN-1", "\x01ACME", twoLines(), ErrInvalidSupplierRef},
		{"no lines", "ASN-1", "ACME", nil, ErrNoLines},
		{"line numbers start at 2", "ASN-1", "ACME", []LineInput{{LineNo: 2, SKU: "A", ExpectedQty: 1}}, ErrInvalidLineNo},
		{"line number 0", "ASN-1", "ACME", []LineInput{{LineNo: 0, SKU: "A", ExpectedQty: 1}}, ErrInvalidLineNo},
		{"line numbers skip", "ASN-1", "ACME", []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: 1}, {LineNo: 3, SKU: "B", ExpectedQty: 1}}, ErrInvalidLineNo},
		{"line numbers out of order", "ASN-1", "ACME", []LineInput{{LineNo: 2, SKU: "A", ExpectedQty: 1}, {LineNo: 1, SKU: "B", ExpectedQty: 1}}, ErrInvalidLineNo},
		{"bad sku", "ASN-1", "ACME", []LineInput{{LineNo: 1, SKU: "bad sku", ExpectedQty: 1}}, shared.ErrInvalidSKU},
		{"zero quantity", "ASN-1", "ACME", []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: 0}}, shared.ErrInvalidQuantity},
		{"negative quantity", "ASN-1", "ACME", []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: -3}}, shared.ErrInvalidQuantity},
		{"duplicate sku", "ASN-1", "ACME", []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: 1}, {LineNo: 2, SKU: "A", ExpectedQty: 2}}, ErrDuplicateSKU},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, events, err := Register(tc.number, tc.supplier, time.Time{}, tc.lines, t0)
			if !errors.Is(err, tc.want) || a != nil || events != nil {
				t.Fatalf("Register = %v %v %v; want %v", a, events, err, tc.want)
			}
		})
	}
}

func TestRegisterAcceptsBoundaries(t *testing.T) {
	number := strings.Repeat("N", MaxNumberLength)
	supplier := strings.Repeat("s", MaxSupplierRefLength)
	lines := []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: shared.MaxQuantity}}
	if _, _, err := Register(number, supplier, time.Time{}, lines, t0); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	if _, _, err := Register("a", "s", time.Time{}, []LineInput{{LineNo: 1, SKU: "A", ExpectedQty: 1}}, t0); err != nil {
		t.Fatalf("minimal values rejected: %v", err)
	}
}

func TestNewNumberMatchesDocumentedAlphabet(t *testing.T) {
	oracle := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	for c := 0; c < 256; c++ {
		value := string([]byte{byte(c)})
		got, err := NewNumber(value)
		want := oracle.MatchString(value)
		if (err == nil) != want {
			t.Errorf("NewNumber(%q): err=%v, oracle says valid=%v", value, err, want)
		}
		if err == nil && got.String() != value {
			t.Errorf("NewNumber(%q) = %q", value, got)
		}
	}
	if _, err := NewNumber("ok-ünï"); !errors.Is(err, ErrInvalidNumber) {
		t.Errorf("non-ASCII accepted: %v", err)
	}
}

func TestLinesReturnsACopy(t *testing.T) {
	a := registered(t)
	lines := a.Lines()
	lines[0] = Line{}
	if a.Lines()[0].SKU() != "SKU-1" {
		t.Fatal("Lines() exposed the internal slice")
	}
}

func TestCancel(t *testing.T) {
	a := registered(t)
	later := t0.Add(time.Hour)
	events, err := a.Cancel("supplier cancelled", later)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(ASNCancelled)
	if !ok || e.Reason != "supplier cancelled" {
		t.Fatalf("event = %+v", events[0])
	}
	assertHeader(t, e, "ASNCancelled", later)
	if a.State() != Cancelled || a.Version() != 2 {
		t.Fatalf("state=%s version=%d", a.State(), a.Version())
	}
}

func TestCancelWithoutReason(t *testing.T) {
	a := registered(t)
	events, err := a.Cancel("", t0)
	if err != nil || events[0].(ASNCancelled).Reason != "" {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func assertCancelRejected(t *testing.T, a *Asn, reason string, want error, wantState State, wantVersion int64) {
	t.Helper()
	events, err := a.Cancel(reason, t0)
	if !errors.Is(err, want) || events != nil {
		t.Fatalf("events=%v err=%v; want %v", events, err, want)
	}
	if a.State() != wantState || a.Version() != wantVersion {
		t.Fatalf("state=%s version=%d; want %s v%d", a.State(), a.Version(), wantState, wantVersion)
	}
}

func TestCancelRejectsInvalidReason(t *testing.T) {
	assertCancelRejected(t, registered(t), "bad\nreason", shared.ErrInvalidReason, Registered, 1)
}

func TestCancelRejectsReceiving(t *testing.T) {
	a := registered(t)
	if err := a.BeginReceiving(); err != nil {
		t.Fatal(err)
	}
	assertCancelRejected(t, a, "", ErrAsnInProgress, Receiving, 2)
}

func TestCancelRejectsClosed(t *testing.T) {
	a := registered(t)
	_ = a.BeginReceiving()
	_ = a.Complete()
	assertCancelRejected(t, a, "", ErrAsnTerminal, Closed, 3)
}

func TestCancelRejectsCancelled(t *testing.T) {
	a := registered(t)
	_, _ = a.Cancel("", t0)
	assertCancelRejected(t, a, "", ErrAsnTerminal, Cancelled, 2)
}

func TestBeginReceiving(t *testing.T) {
	a := registered(t)
	if err := a.BeginReceiving(); err != nil || a.State() != Receiving || a.Version() != 2 {
		t.Fatalf("err=%v state=%s v=%d", err, a.State(), a.Version())
	}
	// Idempotent while Receiving: no second bump.
	if err := a.BeginReceiving(); err != nil || a.State() != Receiving || a.Version() != 2 {
		t.Fatalf("repeat: err=%v state=%s v=%d", err, a.State(), a.Version())
	}
}

func TestBeginReceivingTerminal(t *testing.T) {
	cancelled := registered(t)
	_, _ = cancelled.Cancel("", t0)
	if err := cancelled.BeginReceiving(); !errors.Is(err, ErrAsnTerminal) || cancelled.State() != Cancelled || cancelled.Version() != 2 {
		t.Fatalf("cancelled: err=%v state=%s v=%d", err, cancelled.State(), cancelled.Version())
	}
	closed := registered(t)
	_ = closed.BeginReceiving()
	_ = closed.Complete()
	if err := closed.BeginReceiving(); !errors.Is(err, ErrAsnTerminal) || closed.State() != Closed || closed.Version() != 3 {
		t.Fatalf("closed: err=%v state=%s v=%d", err, closed.State(), closed.Version())
	}
}

func TestComplete(t *testing.T) {
	a := registered(t)
	if err := a.Complete(); !errors.Is(err, ErrAsnNotReceiving) || a.State() != Registered || a.Version() != 1 {
		t.Fatalf("registered: err=%v state=%s v=%d", err, a.State(), a.Version())
	}
	_ = a.BeginReceiving()
	if err := a.Complete(); err != nil || a.State() != Closed || a.Version() != 3 {
		t.Fatalf("receiving: err=%v state=%s v=%d", err, a.State(), a.Version())
	}
	if err := a.Complete(); !errors.Is(err, ErrAsnNotReceiving) || a.Version() != 3 {
		t.Fatalf("closed: err=%v v=%d", err, a.Version())
	}
}

func TestReceivable(t *testing.T) {
	want := map[State]bool{Registered: true, Receiving: true, Closed: false, Cancelled: false, "": false, "Bogus": false}
	for s, ok := range want {
		if s.Receivable() != ok {
			t.Errorf("%q.Receivable() = %v, want %v", s, s.Receivable(), ok)
		}
	}
}

func TestSnapshot(t *testing.T) {
	a := registered(t)
	snap := a.Snapshot()
	if snap.Number != "ASN-1001" || snap.State != Registered || len(snap.Lines) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	snap.Lines[0] = Line{}
	if a.Lines()[0].SKU() != "SKU-1" {
		t.Fatal("snapshot shares the aggregate's slice")
	}
}

func TestRehydrate(t *testing.T) {
	arrival := t0.Add(time.Hour)
	for _, state := range []State{Registered, Receiving, Closed, Cancelled} {
		a, err := Rehydrate("ASN-9", "ACME", arrival, twoLines(), state, 7)
		if err != nil || a.State() != state || a.Version() != 7 || a.Number() != "ASN-9" || !a.ExpectedArrival().Equal(arrival) {
			t.Fatalf("state %s: %+v %v", state, a, err)
		}
	}
	a, err := Rehydrate("ASN-9", "ACME", time.Time{}, twoLines(), Receiving, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The version continues from the persisted one.
	if err := a.Complete(); err != nil || a.Version() != 2 {
		t.Fatalf("err=%v v=%d", err, a.Version())
	}
}

func TestRehydrateRevalidates(t *testing.T) {
	cases := []struct {
		name    string
		number  string
		lines   []LineInput
		state   State
		version int64
		want    error
	}{
		{"unknown state", "ASN-9", twoLines(), "Bogus", 1, ErrInvalidState},
		{"empty state", "ASN-9", twoLines(), "", 1, ErrInvalidState},
		{"version zero", "ASN-9", twoLines(), Registered, 0, ErrInvalidVersion},
		{"negative version", "ASN-9", twoLines(), Registered, -1, ErrInvalidVersion},
		{"bad number", "ASN 9", twoLines(), Registered, 1, ErrInvalidNumber},
		{"no lines", "ASN-9", nil, Registered, 1, ErrNoLines},
		{"bad line numbering", "ASN-9", []LineInput{{LineNo: 2, SKU: "A", ExpectedQty: 1}}, Registered, 1, ErrInvalidLineNo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Rehydrate(tc.number, "ACME", time.Time{}, tc.lines, tc.state, tc.version)
			if !errors.Is(err, tc.want) || a != nil {
				t.Fatalf("Rehydrate = %v, %v; want %v", a, err, tc.want)
			}
		})
	}
	if _, err := Rehydrate("ASN-9", "", time.Time{}, twoLines(), Registered, 1); !errors.Is(err, ErrInvalidSupplierRef) {
		t.Fatalf("blank supplier: %v", err)
	}
}
