package receipt

import (
	"errors"
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

const (
	rid     = "rcpt-123e4567-e89b-12d3-a456-426614174000"
	apptID  = "appt-223e4567-e89b-12d3-a456-426614174000"
	asnNum  = "ASN-1001"
	maxQty  = shared.MaxQuantity
	doorOne = "DOOR-1"
)

var t0 = time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)

func registeredAsn(t *testing.T) *asn.Asn {
	t.Helper()
	a, _, err := asn.Register(asnNum, "ACME", time.Time{}, []asn.LineInput{
		{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40},
		{LineNo: 2, SKU: "SKU-2", ExpectedQty: 5},
	}, t0)
	if err != nil {
		t.Fatalf("asn.Register: %v", err)
	}
	return a
}

func opened(t *testing.T) *Receipt {
	t.Helper()
	r, _, err := Open(rid, registeredAsn(t).Snapshot(), apptID, doorOne, t0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func onlyEvent(t *testing.T, events []Event) Event {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	return events[0]
}

func assertHeader(t *testing.T, e Event, name string, at time.Time) {
	t.Helper()
	if e.EventName() != name || e.ReceiptID() != rid || e.AsnNumber() != asnNum || !e.OccurredAt().Equal(at) || e.OccurredAt().Location() != time.UTC {
		t.Fatalf("event = %s receipt=%s asn=%s at=%v (%v), want %s at %v UTC", e.EventName(), e.ReceiptID(), e.AsnNumber(), e.OccurredAt(), e.OccurredAt().Location(), name, at)
	}
}

func receive(t *testing.T, r *Receipt, lineNo int, qty int64, c Condition) {
	t.Helper()
	if _, err := r.ReceiveLine(lineNo, qty, c, t0); err != nil {
		t.Fatalf("ReceiveLine(%d, %d, %s): %v", lineNo, qty, c, err)
	}
}

func TestOpen(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	r, events, err := Open(rid, registeredAsn(t).Snapshot(), apptID, doorOne, t0.In(brt))
	if err != nil {
		t.Fatal(err)
	}
	assertOpenedState(t, r)
	assertOpenedLines(t, r.Lines())
	e, ok := onlyEvent(t, events).(ReceiptOpened)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "ReceiptOpened", t0)
	if e.AppointmentID != apptID || e.DoorCode != doorOne {
		t.Fatalf("event = %+v", e)
	}
}

func assertOpenedState(t *testing.T, r *Receipt) {
	t.Helper()
	if r.ID() != rid || r.AsnNumber() != asnNum || r.AppointmentID() != apptID || r.DoorCode() != doorOne || r.State() != StateOpen || r.Version() != 1 {
		t.Fatalf("receipt = %+v", r)
	}
	if !r.OpenedAt().Equal(t0) || r.OpenedAt().Location() != time.UTC || !r.ClosedAt().IsZero() {
		t.Fatalf("openedAt=%v closedAt=%v", r.OpenedAt(), r.ClosedAt())
	}
}

func assertOpenedLines(t *testing.T, lines []Line) {
	t.Helper()
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	want := []struct {
		sku string
		qty int64
	}{{"SKU-1", 40}, {"SKU-2", 5}}
	for i, l := range lines {
		if l.LineNo() != i+1 || string(l.SKU()) != want[i].sku || l.ExpectedQty() != want[i].qty || l.Received() != 0 {
			t.Fatalf("line %d = %+v", i, l)
		}
	}
}

func TestOpenWalkInHasNoAppointmentOrDoor(t *testing.T) {
	r, events, err := Open(rid, registeredAsn(t).Snapshot(), "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	e := events[0].(ReceiptOpened)
	if r.AppointmentID() != "" || r.DoorCode() != "" || e.AppointmentID != "" || e.DoorCode != "" {
		t.Fatalf("receipt=%+v event=%+v", r, e)
	}
}

func TestOpenAgainstAReceivingAsn(t *testing.T) {
	a := registeredAsn(t)
	if err := a.BeginReceiving(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(rid, a.Snapshot(), "", "", t0); err != nil {
		t.Fatalf("a Receiving ASN must be receivable: %v", err)
	}
}

func TestOpenRejectsAnAsnThatIsNotReceivable(t *testing.T) {
	cancelled := registeredAsn(t)
	_, _ = cancelled.Cancel("", t0)
	closed := registeredAsn(t)
	_ = closed.BeginReceiving()
	_ = closed.Complete()
	for name, a := range map[string]*asn.Asn{"cancelled": cancelled, "closed": closed} {
		r, events, err := Open(rid, a.Snapshot(), "", "", t0)
		if !errors.Is(err, ErrAsnNotReceivable) || r != nil || events != nil {
			t.Errorf("%s: Open = %v %v %v", name, r, events, err)
		}
	}
}

func TestOpenRejects(t *testing.T) {
	snap := registeredAsn(t).Snapshot()
	cases := []struct {
		name     string
		id       string
		snapshot asn.Snapshot
		appt     string
		door     string
		now      time.Time
		want     error
	}{
		{"bad receipt id", "rcpt-1", snap, "", "", t0, ErrInvalidID},
		{"appointment id of the wrong kind", rid, snap, rid, "", t0, appointment.ErrInvalidID},
		{"bad door code", rid, snap, "", "DOOR 1", t0, appointment.ErrInvalidDoorCode},
		{"unset clock", rid, snap, "", "", time.Time{}, ErrInvalidTimestamps},
		{"snapshot without lines", rid, asn.Snapshot{Number: asnNum, State: asn.Registered}, "", "", t0, asn.ErrNoLines},
		{"snapshot without a number", rid, asn.Snapshot{State: asn.Registered, Lines: snap.Lines}, "", "", t0, asn.ErrInvalidNumber},
		{"snapshot with a zero line", rid, asn.Snapshot{Number: asnNum, State: asn.Registered, Lines: []asn.Line{{}}}, "", "", t0, asn.ErrInvalidLineNo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, events, err := Open(tc.id, tc.snapshot, tc.appt, tc.door, tc.now)
			if !errors.Is(err, tc.want) || r != nil || events != nil {
				t.Fatalf("Open = %v %v %v; want %v", r, events, err, tc.want)
			}
		})
	}
}

func TestReceiveLine(t *testing.T) {
	r := opened(t)
	at := t0.Add(5 * time.Minute)
	events, err := r.ReceiveLine(1, 30, ConditionGood, at.In(time.FixedZone("BRT", -3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(ReceiptLineReceived)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "ReceiptLineReceived", at)
	if e.LineNo != 1 || e.SKU != "SKU-1" || e.Quantity != 30 || e.Condition != ConditionGood {
		t.Fatalf("event = %+v", e)
	}
	if r.Version() != 2 {
		t.Fatalf("version = %d", r.Version())
	}
	l := r.Lines()[0]
	if l.ReceivedGood() != 30 || l.ReceivedDamaged() != 0 || l.Received() != 30 {
		t.Fatalf("line = %+v", l)
	}
}

func TestReceiveLineAccumulatesPerCondition(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 30, ConditionGood)
	receive(t, r, 1, 4, ConditionDamaged)
	receive(t, r, 1, 6, ConditionGood)
	receive(t, r, 2, 2, ConditionDamaged)
	l1, l2 := r.Lines()[0], r.Lines()[1]
	if l1.ReceivedGood() != 36 || l1.ReceivedDamaged() != 4 || l1.Received() != 40 {
		t.Fatalf("line 1 = %+v", l1)
	}
	if l2.ReceivedGood() != 0 || l2.ReceivedDamaged() != 2 {
		t.Fatalf("line 2 = %+v", l2)
	}
	if r.Version() != 5 {
		t.Fatalf("version = %d", r.Version())
	}
}

func TestReceiveLineAllowsOverReceipt(t *testing.T) {
	r := opened(t)
	receive(t, r, 2, 9, ConditionGood)
	if got := r.Lines()[1].Received(); got != 9 {
		t.Fatalf("received = %d", got)
	}
}

func assertReceiveRejected(t *testing.T, r *Receipt, lineNo int, qty int64, c Condition, want error) {
	t.Helper()
	version := r.Version()
	events, err := r.ReceiveLine(lineNo, qty, c, t0)
	if !errors.Is(err, want) || events != nil || r.Version() != version {
		t.Fatalf("ReceiveLine(%d, %d, %q) = %v, %v (v%d); want %v", lineNo, qty, c, events, err, r.Version(), want)
	}
}

func TestReceiveLineRejectsAnUnknownLine(t *testing.T) {
	r := opened(t)
	for _, lineNo := range []int{-1, 0, 3, 100} {
		assertReceiveRejected(t, r, lineNo, 1, ConditionGood, ErrLineNotOnAsn)
	}
	receive(t, r, 1, 1, ConditionGood)
	receive(t, r, 2, 1, ConditionGood)
}

func TestReceiveLineRejectsABadQuantity(t *testing.T) {
	r := opened(t)
	for _, qty := range []int64{0, -5, maxQty + 1} {
		assertReceiveRejected(t, r, 1, qty, ConditionGood, shared.ErrInvalidQuantity)
	}
}

func TestReceiveLineRejectsABadCondition(t *testing.T) {
	r := opened(t)
	for _, c := range []Condition{"", "good", "Spoiled"} {
		assertReceiveRejected(t, r, 1, 1, c, ErrInvalidCondition)
	}
}

func TestReceiveLineKeepsTheLineTotalWithinTheBound(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, maxQty-1, ConditionGood)
	assertReceiveRejected(t, r, 1, 2, ConditionDamaged, shared.ErrInvalidQuantity)
	receive(t, r, 1, 1, ConditionDamaged) // exactly the bound is fine
	assertReceiveRejected(t, r, 1, 1, ConditionGood, shared.ErrInvalidQuantity)
}

func TestReceiveLineOnAClosedReceipt(t *testing.T) {
	r := opened(t)
	if _, err := r.Close(t0); err != nil {
		t.Fatal(err)
	}
	assertReceiveRejected(t, r, 1, 1, ConditionGood, ErrReceiptClosed)
	assertReceiveRejected(t, r, 99, 0, "", ErrReceiptClosed) // closed wins over every other fault
}

func TestParseCondition(t *testing.T) {
	for _, v := range []string{"Good", "Damaged"} {
		if c, err := ParseCondition(v); err != nil || string(c) != v {
			t.Errorf("ParseCondition(%q) = %q, %v", v, c, err)
		}
	}
	for _, v := range []string{"", "good", "DAMAGED", "Quarantine"} {
		if c, err := ParseCondition(v); !errors.Is(err, ErrInvalidCondition) || c != "" {
			t.Errorf("ParseCondition(%q) = %q, %v; want ErrInvalidCondition", v, c, err)
		}
	}
}

func TestCloseWithoutDiscrepancies(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 40, ConditionGood)
	receive(t, r, 2, 5, ConditionGood)
	closeAt := t0.Add(time.Hour)
	events, err := r.Close(closeAt.In(time.FixedZone("BRT", -3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(ReceiptClosed)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "ReceiptClosed", closeAt)
	if e.Discrepancies == nil || len(e.Discrepancies) != 0 {
		t.Fatalf("discrepancies = %#v; want a non-nil empty slice", e.Discrepancies)
	}
	if r.State() != StateClosed || r.Version() != 4 || !r.ClosedAt().Equal(closeAt) || r.ClosedAt().Location() != time.UTC {
		t.Fatalf("state=%s version=%d closedAt=%v", r.State(), r.Version(), r.ClosedAt())
	}
}

func TestCloseReportsEveryDiscrepancyKind(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 30, ConditionGood)   // 30 + 4 = 34 < 40: Short
	receive(t, r, 1, 4, ConditionDamaged) // and Damaged
	receive(t, r, 2, 7, ConditionGood)    // 7 > 5: Over
	events, err := r.Close(t0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Discrepancy{
		{LineNo: 1, SKU: "SKU-1", Kind: KindShort, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
		{LineNo: 1, SKU: "SKU-1", Kind: KindDamaged, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
		{LineNo: 2, SKU: "SKU-2", Kind: KindOver, ExpectedQty: 5, ReceivedQty: 7, DamagedQty: 0},
	}
	got := events[0].(ReceiptClosed).Discrepancies
	if len(got) != len(want) {
		t.Fatalf("discrepancies = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("discrepancy %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCloseWithNothingReceivedIsShortOnEveryLine(t *testing.T) {
	r := opened(t)
	events, err := r.Close(t0)
	if err != nil {
		t.Fatal(err)
	}
	got := events[0].(ReceiptClosed).Discrepancies
	if len(got) != 2 || got[0].Kind != KindShort || got[0].ReceivedQty != 0 || got[1].Kind != KindShort || got[1].LineNo != 2 {
		t.Fatalf("discrepancies = %+v", got)
	}
}

func TestDiscrepancyBoundaries(t *testing.T) {
	cases := []struct {
		name            string
		good, damaged   int64
		wantKinds       []Kind
		wantReceivedQty int64
	}{
		{"exact match", 5, 0, nil, 5},
		{"one short", 4, 0, []Kind{KindShort}, 4},
		{"one over", 6, 0, []Kind{KindOver}, 6},
		{"exact match with damage is damaged only", 4, 1, []Kind{KindDamaged}, 5},
		{"all damaged, exact count", 0, 5, []Kind{KindDamaged}, 5},
		{"over with damage", 5, 1, []Kind{KindOver, KindDamaged}, 6},
		{"short with damage", 3, 1, []Kind{KindShort, KindDamaged}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := opened(t)
			receive(t, r, 1, 40, ConditionGood)
			receiveIfPositive(t, r, tc.good, ConditionGood)
			receiveIfPositive(t, r, tc.damaged, ConditionDamaged)
			got := line2Discrepancies(r)
			if len(got) != len(tc.wantKinds) {
				t.Fatalf("discrepancies = %+v, want kinds %v", got, tc.wantKinds)
			}
			for i, kind := range tc.wantKinds {
				if got[i].Kind != kind || got[i].ReceivedQty != tc.wantReceivedQty || got[i].DamagedQty != tc.damaged || got[i].ExpectedQty != 5 {
					t.Errorf("discrepancy %d = %+v, want kind %s received %d damaged %d", i, got[i], kind, tc.wantReceivedQty, tc.damaged)
				}
			}
		})
	}
}

func receiveIfPositive(t *testing.T, r *Receipt, qty int64, c Condition) {
	t.Helper()
	if qty > 0 {
		receive(t, r, 2, qty, c)
	}
}

func line2Discrepancies(r *Receipt) []Discrepancy {
	var out []Discrepancy
	for _, d := range r.Discrepancies() {
		if d.LineNo == 2 {
			out = append(out, d)
		}
	}
	return out
}

func TestCloseOnAClosedReceipt(t *testing.T) {
	r := opened(t)
	_, _ = r.Close(t0)
	events, err := r.Close(t0.Add(time.Hour))
	if !errors.Is(err, ErrReceiptClosed) || events != nil || r.Version() != 2 || !r.ClosedAt().Equal(t0) {
		t.Fatalf("events=%v err=%v v=%d closedAt=%v", events, err, r.Version(), r.ClosedAt())
	}
}

func TestLinesReturnsACopy(t *testing.T) {
	r := opened(t)
	lines := r.Lines()
	lines[0] = Line{}
	if r.Lines()[0].SKU() != "SKU-1" {
		t.Fatal("Lines() exposed the internal slice")
	}
}

func TestNewID(t *testing.T) {
	oracle := regexp.MustCompile(`^rcpt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	for _, v := range []string{rid, "rcpt-00000000-0000-0000-0000-000000000000", "rcpt-ffffffff-ffff-ffff-ffff-ffffffffffff", "", "rcpt-", "appt-123e4567-e89b-12d3-a456-426614174000", rid + "0", "x" + rid, "rcpt-123E4567-e89b-12d3-a456-426614174000"} {
		got, err := NewID(v)
		if (err == nil) != oracle.MatchString(v) {
			t.Errorf("NewID(%q): err=%v", v, err)
		}
		if err == nil && got.String() != v {
			t.Errorf("NewID(%q) = %q", v, got)
		}
	}
}

func persisted() Persisted {
	return Persisted{
		ID:            rid,
		AsnNumber:     asnNum,
		AppointmentID: apptID,
		DoorCode:      doorOne,
		State:         StateOpen,
		Lines: []LineState{
			{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40, ReceivedGood: 30, ReceivedDamaged: 2},
			{LineNo: 2, SKU: "SKU-2", ExpectedQty: 5},
		},
		OpenedAt: t0,
		Version:  3,
	}
}

func TestRehydrate(t *testing.T) {
	r, err := Rehydrate(persisted())
	if err != nil {
		t.Fatal(err)
	}
	if r.ID() != rid || r.AsnNumber() != asnNum || r.AppointmentID() != apptID || r.DoorCode() != doorOne || r.State() != StateOpen || r.Version() != 3 || !r.OpenedAt().Equal(t0) {
		t.Fatalf("receipt = %+v", r)
	}
	if l := r.Lines()[0]; l.ReceivedGood() != 30 || l.ReceivedDamaged() != 2 || l.ExpectedQty() != 40 {
		t.Fatalf("line = %+v", l)
	}
	// The version continues from the persisted one.
	events, err := r.ReceiveLine(2, 1, ConditionGood, t0)
	if err != nil || len(events) != 1 || r.Version() != 4 {
		t.Fatalf("events=%v err=%v v=%d", events, err, r.Version())
	}
}

func TestRehydrateAcceptsTheFirstVersion(t *testing.T) {
	p := persisted()
	p.Version = 1
	r, err := Rehydrate(p)
	if err != nil || r.Version() != 1 {
		t.Fatalf("version 1: %v, %v", r, err)
	}
}

func TestRehydrateClosedAndWalkIn(t *testing.T) {
	p := persisted()
	p.State = StateClosed
	p.ClosedAt = t0.Add(time.Hour)
	p.AppointmentID, p.DoorCode = "", ""
	r, err := Rehydrate(p)
	if err != nil || r.State() != StateClosed || !r.ClosedAt().Equal(p.ClosedAt) || r.AppointmentID() != "" || r.DoorCode() != "" {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
	if _, err := r.Close(t0); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("a rehydrated closed receipt must stay closed: %v", err)
	}
}

func TestRehydrateRevalidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Persisted)
		want   error
	}{
		{"unknown state", func(p *Persisted) { p.State = "Bogus" }, ErrInvalidState},
		{"empty state", func(p *Persisted) { p.State = "" }, ErrInvalidState},
		{"version zero", func(p *Persisted) { p.Version = 0 }, ErrInvalidVersion},
		{"bad id", func(p *Persisted) { p.ID = "x" }, ErrInvalidID},
		{"bad asn number", func(p *Persisted) { p.AsnNumber = "ASN 1" }, asn.ErrInvalidNumber},
		{"bad appointment id", func(p *Persisted) { p.AppointmentID = "x" }, appointment.ErrInvalidID},
		{"bad door", func(p *Persisted) { p.DoorCode = "a b" }, appointment.ErrInvalidDoorCode},
		{"opened_at unset", func(p *Persisted) { p.OpenedAt = time.Time{} }, ErrInvalidTimestamps},
		{"open with closed_at", func(p *Persisted) { p.ClosedAt = t0 }, ErrInvalidTimestamps},
		{"closed without closed_at", func(p *Persisted) { p.State = StateClosed }, ErrInvalidTimestamps},
		{"no lines", func(p *Persisted) { p.Lines = nil }, asn.ErrNoLines},
		{"line numbers start at 2", func(p *Persisted) { p.Lines = p.Lines[1:] }, asn.ErrInvalidLineNo},
		{"bad sku", func(p *Persisted) { p.Lines[0].SKU = "a b" }, shared.ErrInvalidSKU},
		{"zero expected", func(p *Persisted) { p.Lines[0].ExpectedQty = 0 }, shared.ErrInvalidQuantity},
		{"duplicate sku", func(p *Persisted) { p.Lines[1].SKU = "SKU-1" }, asn.ErrDuplicateSKU},
		{"negative good", func(p *Persisted) { p.Lines[0].ReceivedGood = -1 }, ErrInvalidReceived},
		{"negative damaged", func(p *Persisted) { p.Lines[0].ReceivedDamaged = -1 }, ErrInvalidReceived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := persisted()
			p.Lines = append([]LineState(nil), p.Lines...)
			tc.mutate(&p)
			r, err := Rehydrate(p)
			if !errors.Is(err, tc.want) || r != nil {
				t.Fatalf("Rehydrate = %v, %v; want %v", r, err, tc.want)
			}
		})
	}
}

func TestRehydrateReceivedBounds(t *testing.T) {
	cases := []struct {
		name          string
		good, damaged int64
		ok            bool
	}{
		{"zero", 0, 0, true},
		{"only good at the bound", maxQty, 0, true},
		{"only damaged at the bound", 0, maxQty, true},
		{"split at the bound", maxQty - 1, 1, true},
		{"good above the bound", maxQty + 1, 0, false},
		{"damaged above the bound", 0, maxQty + 1, false},
		{"sum above the bound", maxQty, 1, false},
		{"sum above the bound, split", maxQty - 1, 2, false},
		{"overflowing sum", math.MaxInt64, 1, false},
		{"overflowing damaged", 1, math.MaxInt64, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := persisted()
			p.Lines = []LineState{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40, ReceivedGood: tc.good, ReceivedDamaged: tc.damaged}}
			_, err := Rehydrate(p)
			if tc.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidReceived) {
				t.Fatalf("err = %v; want ErrInvalidReceived", err)
			}
		})
	}
}
