package receipt

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const sla24h = 24 * time.Hour

// --- Hold ------------------------------------------------------------------

func TestHoldLineDeclaresGoodUnitsHeld(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 30, ConditionGood)
	receive(t, r, 1, 4, ConditionDamaged)
	before := r.Version()
	if err := r.HoldLine(1, 5); err != nil {
		t.Fatal(err)
	}
	if got := r.Lines()[0].Held(); got != 5 {
		t.Fatalf("held = %d, want 5", got)
	}
	if r.Version() != before+1 {
		t.Fatalf("version = %d, want %d (a hold is a change of the receipt)", r.Version(), before+1)
	}
	if err := r.HoldLine(1, 25); err != nil {
		t.Fatalf("holding up to every Good unit not yet held must be allowed: %v", err)
	}
	if got := r.Lines()[0].Held(); got != 30 {
		t.Fatalf("held = %d, want 30", got)
	}
	if got := r.Lines()[0].Received(); got != 34 {
		t.Fatalf("a hold must not change what was received: %d", got)
	}
}

func TestHoldLineRefusals(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 10, ConditionGood)
	receive(t, r, 2, 3, ConditionDamaged) // Damaged units cannot be held: they are already rejected
	for _, tc := range []struct {
		name string
		line int
		qty  int64
		want error
	}{
		{"line zero", 0, 1, ErrLineNotOnAsn},
		{"line past the end", 3, 1, ErrLineNotOnAsn},
		{"zero units", 1, 0, shared.ErrInvalidQuantity},
		{"negative units", 1, -1, shared.ErrInvalidQuantity},
		{"one above the good units", 1, 11, ErrHoldExceedsGood},
		{"only damaged received", 2, 1, ErrHoldExceedsGood},
	} {
		v := r.Version()
		if err := r.HoldLine(tc.line, tc.qty); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if r.Version() != v || r.Lines()[0].Held() != 0 {
			t.Errorf("%s: a refused hold must change nothing", tc.name)
		}
	}
	if err := r.HoldLine(1, 10); err != nil {
		t.Fatalf("exactly the good units must be holdable: %v", err)
	}
	if err := r.HoldLine(1, 1); !errors.Is(err, ErrHoldExceedsGood) {
		t.Fatalf("a second hold past the good units: %v", err)
	}
	if _, err := r.Close(t0); err != nil {
		t.Fatal(err)
	}
	if err := r.HoldLine(1, 1); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("hold on a closed receipt: %v", err)
	}
}

func TestHoldAndLastReceivedSurviveStorage(t *testing.T) {
	r := opened(t)
	if _, err := r.ReceiveLine(1, 10, ConditionGood, at(7)); err != nil {
		t.Fatal(err)
	}
	if err := r.HoldLine(1, 4); err != nil {
		t.Fatal(err)
	}
	p := r.Persisted()
	if p.Lines[0].Held != 4 || !p.LastReceivedAt.Equal(at(7)) {
		t.Fatalf("persisted = %+v", p)
	}
	back, err := Rehydrate(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.Lines()[0].Held() != 4 || !back.LastReceivedAt().Equal(at(7)) {
		t.Fatalf("rehydrated held = %d last = %v", back.Lines()[0].Held(), back.LastReceivedAt())
	}
	p.Lines[0].Held = 11 // more held than Good received is not a receipt
	if _, err := Rehydrate(p); !errors.Is(err, ErrInvalidHeld) {
		t.Fatalf("held above good: %v", err)
	}
	p.Lines[0].Held = -1
	if _, err := Rehydrate(p); !errors.Is(err, ErrInvalidHeld) {
		t.Fatalf("negative held: %v", err)
	}
	p.Lines[0].Held = 10
	if _, err := Rehydrate(p); err != nil {
		t.Fatalf("held equal to good received is valid: %v", err)
	}
}

// --- the advice clock ------------------------------------------------------

func TestLastReceivedAtFollowsEveryReceivingAction(t *testing.T) {
	r := openedCartons(t)
	if !r.LastReceivedAt().IsZero() {
		t.Fatalf("nothing received yet: %v", r.LastReceivedAt())
	}
	if !r.AdviceClock().Equal(t0) {
		t.Fatalf("with nothing received the clock starts at opened_at, got %v", r.AdviceClock())
	}
	if _, err := r.ReceiveLine(2, 1, ConditionGood, at(3)); err != nil {
		t.Fatal(err)
	}
	if !r.LastReceivedAt().Equal(at(3)) || !r.AdviceClock().Equal(at(3)) {
		t.Fatalf("after a line quantity: last = %v clock = %v", r.LastReceivedAt(), r.AdviceClock())
	}
	// A refused scan received nothing: the clock does not move.
	if _, err := r.AcceptCarton(CartonScan{SSCC: ssccX(t), Condition: ConditionGood}, at(5)); err != nil {
		t.Fatal(err)
	}
	if !r.LastReceivedAt().Equal(at(3)) {
		t.Fatalf("a refused scan moved the clock to %v", r.LastReceivedAt())
	}
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(6)); err != nil {
		t.Fatal(err)
	}
	if !r.LastReceivedAt().Equal(at(6)) {
		t.Fatalf("after an accepted carton: %v", r.LastReceivedAt())
	}
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-1"), at(8)); err != nil {
		t.Fatal(err)
	}
	if !r.LastReceivedAt().Equal(at(8)) {
		t.Fatalf("after cases: %v", r.LastReceivedAt())
	}
	if err := r.HoldLine(1, 1); err != nil {
		t.Fatal(err)
	}
	if !r.LastReceivedAt().Equal(at(8)) {
		t.Fatalf("a hold received nothing: %v", r.LastReceivedAt())
	}
}

// --- overdue ---------------------------------------------------------------

func TestAdviceOverdueIsStrictlyPastTheSLA(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 1, ConditionGood) // receive() stamps t0
	due := t0.Add(sla24h)
	if _, ok := r.AdviceOverdue(sla24h, due); ok {
		t.Fatal("exactly at the SLA the advice is still on time")
	}
	if _, ok := r.AdviceOverdue(sla24h, due.Add(-time.Nanosecond)); ok {
		t.Fatal("before the SLA nothing is overdue")
	}
	ev, ok := r.AdviceOverdue(sla24h, due.Add(time.Nanosecond))
	if !ok {
		t.Fatal("one nanosecond past the SLA the advice is overdue")
	}
	if ev.EventName() != "ReceivingAdviceOverdue" || ev.ReceiptID() != rid || ev.AsnNumber() != asnNum {
		t.Fatalf("event = %+v", ev)
	}
	if !ev.LastCartonAt.Equal(t0) || !ev.DueAt.Equal(due) || ev.SLA != sla24h || !ev.OccurredAt().Equal(due.Add(time.Nanosecond)) {
		t.Fatalf("event = %+v", ev)
	}
}

func TestAdviceOverdueClockStartsAtOpenWhenNothingWasReceived(t *testing.T) {
	r := opened(t) // opened at t0, nothing received
	if _, ok := r.AdviceOverdue(sla24h, t0.Add(sla24h)); ok {
		t.Fatal("on time at the boundary")
	}
	ev, ok := r.AdviceOverdue(sla24h, t0.Add(sla24h+time.Second))
	if !ok || !ev.LastCartonAt.Equal(t0) {
		t.Fatalf("overdue = %+v, %v", ev, ok)
	}
}

func TestAdviceOverdueOnlyForAnOpenReceipt(t *testing.T) {
	r := opened(t)
	if _, err := r.Close(t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.AdviceOverdue(sla24h, t0.Add(100*sla24h)); ok {
		t.Fatal("a closed receipt has its advice: it cannot be overdue")
	}
}

// --- the advice ------------------------------------------------------------

// closedWithExceptions: line 1 expected 40: 30 Good (5 held) + 4 Damaged = 34
// received, Short 6; line 2 expected 5: 7 received, Over 2.
func closedWithExceptions(t *testing.T) *Receipt {
	t.Helper()
	r := opened(t)
	for _, step := range []struct {
		line int
		qty  int64
		c    Condition
	}{{1, 30, ConditionGood}, {1, 4, ConditionDamaged}, {2, 7, ConditionGood}} {
		if _, err := r.ReceiveLine(step.line, step.qty, step.c, at(10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.HoldLine(1, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(20)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIssueAdviceReportsEveryLineWithItsCodedDiscrepancies(t *testing.T) {
	r := closedWithExceptions(t)
	ev, err := r.IssueAdvice("ACME", sla24h, at(20))
	if err != nil {
		t.Fatal(err)
	}
	assertHeader(t, ev, "ReceivingAdviceIssued", at(20))
	if ev.DespatchAdviceRef != asnNum || ev.SupplierRef != "ACME" || ev.OwnerRef != "" {
		t.Fatalf("references = %+v", ev)
	}
	if !ev.LastCartonAt.Equal(at(10)) || ev.SLA != sla24h || !ev.WithinSLA {
		t.Fatalf("clock = last %v sla %v within %v", ev.LastCartonAt, ev.SLA, ev.WithinSLA)
	}
	want := []AdviceLine{
		{
			LineNo: 1, SKU: "SKU-1", Expected: 40, Received: 34, Accepted: 25, Rejected: 4, Held: 5,
			Discrepancies: []AdviceDiscrepancy{
				{Kind: KindShort, Quantity: 6, Coding: Coding{W13: "02", CBV: "mismatch_quantity"}},
				{Kind: KindDamaged, Quantity: 4, Coding: Coding{W13: "01", CBV: "damaged"}},
				{Kind: KindHold, Quantity: 5, Coding: Coding{W13: "09", CBV: "non_conformant"}},
			},
		},
		{
			LineNo: 2, SKU: "SKU-2", Expected: 5, Received: 7, Accepted: 7, Rejected: 0, Held: 0,
			Discrepancies: []AdviceDiscrepancy{
				{Kind: KindOver, Quantity: 2, Coding: Coding{W13: "03", CBV: "mismatch_quantity"}},
			},
		},
	}
	if !reflect.DeepEqual(ev.Lines, want) {
		t.Fatalf("lines =\n%+v\nwant\n%+v", ev.Lines, want)
	}
	if ev.LinesTotal != 2 || ev.LinesTruncated {
		t.Fatalf("lines_total = %d truncated = %v", ev.LinesTotal, ev.LinesTruncated)
	}
	for _, l := range ev.Lines {
		if l.Accepted+l.Rejected+l.Held != l.Received {
			t.Errorf("line %d: accepted + rejected + held = %d, received %d", l.LineNo, l.Accepted+l.Rejected+l.Held, l.Received)
		}
	}
}

func TestIssueAdviceOfAMatchingLineHasNoDiscrepancy(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, 40, ConditionGood)
	receive(t, r, 2, 5, ConditionGood)
	if _, err := r.Close(t0); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ev.Lines {
		if l.Discrepancies == nil || len(l.Discrepancies) != 0 {
			t.Errorf("line %d: discrepancies = %#v, want an empty, non-nil list", l.LineNo, l.Discrepancies)
		}
	}
	if ev.CartonFindings == nil || len(ev.CartonFindings) != 0 || ev.CartonFindingsTotal != 0 {
		t.Fatalf("carton findings = %#v total %d", ev.CartonFindings, ev.CartonFindingsTotal)
	}
}

func TestIssueAdviceNeedsAClosedReceipt(t *testing.T) {
	r := opened(t)
	if _, err := r.IssueAdvice("ACME", sla24h, t0); !errors.Is(err, ErrReceiptNotClosed) {
		t.Fatalf("open receipt: %v", err)
	}
}

func TestIssueAdviceWithinSLAIsInclusive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issued time.Duration
		within bool
	}{
		{"a nanosecond before the deadline", sla24h - time.Nanosecond, true},
		{"exactly at the deadline", sla24h, true},
		{"a nanosecond after the deadline", sla24h + time.Nanosecond, false},
	} {
		r := opened(t)
		receive(t, r, 1, 1, ConditionGood) // stamps t0
		issued := t0.Add(tc.issued)
		if _, err := r.Close(issued); err != nil {
			t.Fatal(err)
		}
		ev, err := r.IssueAdvice("ACME", sla24h, issued)
		if err != nil {
			t.Fatal(err)
		}
		if ev.WithinSLA != tc.within {
			t.Errorf("%s: within_sla = %v, want %v", tc.name, ev.WithinSLA, tc.within)
		}
	}
}

func TestIssueAdviceCarriesTheDepositor(t *testing.T) {
	r := openedCartons(t) // the ASN has owner_ref
	if _, err := r.Close(at(1)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if ev.OwnerRef != owner {
		t.Fatalf("owner_ref = %q, want the depositor %q", ev.OwnerRef, owner)
	}
}

func TestIssueAdviceListsTheSsccsReceivedPerLine(t *testing.T) {
	r := openedCartons(t)
	for _, s := range []gs1.SSCC{ssccA(t), ssccM(t)} {
		if res, err := r.AcceptCarton(scanOf(t, s, 20), at(2)); err != nil || !res.Accepted {
			t.Fatalf("accept %s: %+v %v", s, res, err)
		}
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, at(9))
	if err != nil {
		t.Fatal(err)
	}
	l1, l2 := ev.Lines[0], ev.Lines[1]
	if !reflect.DeepEqual(l1.SSCCs, []gs1.SSCC{ssccA(t), ssccM(t)}) || l1.SSCCCount != 2 || l1.SSCCsTruncated {
		t.Fatalf("line 1 ssccs = %v count %d truncated %v", l1.SSCCs, l1.SSCCCount, l1.SSCCsTruncated)
	}
	if !reflect.DeepEqual(l2.SSCCs, []gs1.SSCC{ssccM(t)}) || l2.SSCCCount != 1 {
		t.Fatalf("line 2 ssccs = %v count %d", l2.SSCCs, l2.SSCCCount)
	}
	if l1.GTIN != eachGTIN1(t) || l2.GTIN != eachGTIN2(t) {
		t.Fatalf("gtins = %s %s", l1.GTIN, l2.GTIN)
	}
	// B was never received: it is neither listed nor counted.
	for _, s := range l1.SSCCs {
		if s == ssccB(t) {
			t.Fatal("a carton that was not received is listed")
		}
	}
}

func TestIssueAdviceCartonFindingsCarryTheirCoding(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(CartonScan{SSCC: ssccX(t), Condition: ConditionGood}, at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, at(9))
	if err != nil {
		t.Fatal(err)
	}
	if ev.CartonFindingsTotal != 4 || len(ev.CartonFindings) != 4 {
		t.Fatalf("findings = %d total %d, want the unexpected carton and the 3 missing ones", len(ev.CartonFindings), ev.CartonFindingsTotal)
	}
	if ev.CartonFindings[0].Kind != KindUnexpectedCarton || ev.CartonFindings[1].Kind != KindMissingCarton {
		t.Fatalf("findings = %+v", ev.CartonFindings)
	}
}

// --- IncorrectProduct: derived from the refused cartons --------------------

func wrongLabel(t *testing.T, s gs1.SSCC) CartonScan {
	in := scanOf(t, s, 20)
	in.LabelContentGTIN, in.LabelContentSKU = eachGTIN2(t), "SKU-2"
	return in
}

func lineKinds(l AdviceLine) map[Kind]int64 {
	out := map[Kind]int64{}
	for _, d := range l.Discrepancies {
		out[d.Kind] = d.Quantity
	}
	return out
}

func TestIncorrectProductIsDerivedFromAWrongProductCarton(t *testing.T) {
	r := openedCartons(t)
	// Carton A (SKU-1 x 20) is scanned twice with a label that names SKU-2:
	// refused both times. Carton B's label GTIN does not resolve at all.
	for _, in := range []CartonScan{wrongLabel(t, ssccA(t)), wrongLabel(t, ssccA(t))} {
		if _, err := r.AcceptCarton(in, at(1)); err != nil {
			t.Fatal(err)
		}
	}
	unknown := scanOf(t, ssccB(t), 10)
	unknown.LabelContentGTIN = eachGTIN2(t)
	if _, err := r.AcceptCarton(unknown, at(2)); err != nil {
		t.Fatal(err)
	}
	// A GTIN scan that resolved nothing names no carton and no line.
	if _, err := r.RejectUnknownGtin(eachGTIN2(t), at(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, at(9))
	if err != nil {
		t.Fatal(err)
	}
	got := lineKinds(ev.Lines[0])
	if got[KindIncorrectProduct] != 30 {
		t.Fatalf("line 1 incorrect product = %d, want 30 (A once: 20, B once: 10): %+v", got[KindIncorrectProduct], ev.Lines[0].Discrepancies)
	}
	var d AdviceDiscrepancy
	for _, x := range ev.Lines[0].Discrepancies {
		if x.Kind == KindIncorrectProduct {
			d = x
		}
	}
	if d.Coding != (Coding{W13: "05", CBV: "mismatch_class"}) {
		t.Fatalf("coding = %+v", d.Coding)
	}
	// Rule 3 of ADR 0023: the refused units are not received, so they are inside Short.
	if got[KindShort] != 40 {
		t.Fatalf("short = %d, want 40: IncorrectProduct explains the shortfall, it does not replace it", got[KindShort])
	}
	if len(lineKinds(ev.Lines[1])) != 1 || lineKinds(ev.Lines[1])[KindShort] != 5 {
		t.Fatalf("line 2 must only be Short: %+v", ev.Lines[1].Discrepancies)
	}
	// The line-level ReceiptClosed list keeps its three v1 kinds.
	assertOnlyLineKinds(t, r.Discrepancies())
}

func TestAWrongLabelThatIsLaterAcceptedIsNoLongerIncorrect(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(wrongLabel(t, ssccA(t)), at(1)); err != nil {
		t.Fatal(err)
	}
	fixed := scanOf(t, ssccA(t), 20)
	if res, err := r.AcceptCarton(fixed, at(2)); err != nil || !res.Accepted {
		t.Fatalf("accept: %+v %v", res, err)
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", sla24h, at(9))
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := lineKinds(ev.Lines[0])[KindIncorrectProduct]; bad {
		t.Fatalf("carton A was received: %+v", ev.Lines[0].Discrepancies)
	}
}

// --- bounds ----------------------------------------------------------------

func TestAdviceBoundsAreThePlainLiteralsOfTheADR(t *testing.T) {
	if MaxAdviceLines != 1000 || MaxAdviceSSCCs != 1000 || MaxAdviceCartonFindings != 200 {
		t.Fatalf("bounds = %d %d %d, ADR 0023 fixes 1000 1000 200", MaxAdviceLines, MaxAdviceSSCCs, MaxAdviceCartonFindings)
	}
}

func adviceWith(t *testing.T, r *Receipt, b adviceBounds) ReceivingAdviceIssued {
	t.Helper()
	ev, err := r.issueAdvice("ACME", sla24h, at(9), b)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func receivedCartonsClosed(t *testing.T) *Receipt {
	t.Helper()
	r := openedCartons(t)
	for _, s := range []gs1.SSCC{ssccA(t), ssccB(t), ssccM(t)} {
		if res, err := r.AcceptCarton(scanOf(t, s, 20), at(1)); err != nil {
			t.Fatal(err)
		} else if s != ssccB(t) && !res.Accepted {
			t.Fatalf("accept %s refused", s)
		}
	}
	// B has 10 units: scanOf(…,20) refused it as a CaseQtyMismatch; receive it properly.
	if res, err := r.AcceptCarton(scanOf(t, ssccB(t), 10), at(2)); err != nil || !res.Accepted {
		t.Fatalf("accept B: %+v %v", res, err)
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdviceSsccBudgetIsSharedAcrossLinesInLineOrder(t *testing.T) {
	r := receivedCartonsClosed(t) // line 1 has A, B, M; line 2 has M
	for _, tc := range []struct {
		budget                   int
		l1, l2                   int
		l1Truncated, l2Truncated bool
	}{
		{4, 3, 1, false, false}, // exactly enough: nothing is cut
		{3, 3, 0, false, true},  // one short: the second line loses its SSCC
		{2, 2, 0, true, true},
		{0, 0, 0, true, true},
	} {
		ev := adviceWith(t, r, adviceBounds{lines: 10, sscc: tc.budget, findings: 10})
		l1, l2 := ev.Lines[0], ev.Lines[1]
		if len(l1.SSCCs) != tc.l1 || len(l2.SSCCs) != tc.l2 || l1.SSCCsTruncated != tc.l1Truncated || l2.SSCCsTruncated != tc.l2Truncated {
			t.Errorf("budget %d: line 1 %d/%v line 2 %d/%v, want %d/%v %d/%v", tc.budget,
				len(l1.SSCCs), l1.SSCCsTruncated, len(l2.SSCCs), l2.SSCCsTruncated, tc.l1, tc.l1Truncated, tc.l2, tc.l2Truncated)
		}
		if tc.l2 == 0 && l2.SSCCs != nil {
			t.Errorf("budget %d: a line with nothing listed has no list (nil), got %v", tc.budget, l2.SSCCs)
		}
		if l1.SSCCCount != 3 || l2.SSCCCount != 1 {
			t.Errorf("budget %d: counts stay exact, got %d and %d", tc.budget, l1.SSCCCount, l2.SSCCCount)
		}
	}
}

func TestAdviceCartonFindingsAreCappedAndCounted(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.Close(at(9)); err != nil { // 3 MissingCarton findings
		t.Fatal(err)
	}
	ev := adviceWith(t, r, adviceBounds{lines: 10, sscc: 10, findings: 3})
	if len(ev.CartonFindings) != 3 || ev.CartonFindingsTotal != 3 {
		t.Fatalf("at the cap: %d findings, total %d", len(ev.CartonFindings), ev.CartonFindingsTotal)
	}
	ev = adviceWith(t, r, adviceBounds{lines: 10, sscc: 10, findings: 2})
	if len(ev.CartonFindings) != 2 || ev.CartonFindingsTotal != 3 {
		t.Fatalf("one over the cap: %d findings, total %d", len(ev.CartonFindings), ev.CartonFindingsTotal)
	}
	if ev.CartonFindings[0].Expected != ssccA(t).String() || ev.CartonFindings[1].Expected != ssccB(t).String() {
		t.Fatalf("the first findings in raise order are kept: %+v", ev.CartonFindings)
	}
}

func manyLinesReceipt(t *testing.T, n int, shortLine int) *Receipt {
	t.Helper()
	inputs := make([]asn.LineInput, 0, n)
	for i := 1; i <= n; i++ {
		inputs = append(inputs, asn.LineInput{LineNo: i, SKU: fmt.Sprintf("SKU-%d", i), ExpectedQty: 2})
	}
	a, _, err := asn.Register(asnNum, "ACME", time.Time{}, inputs, t0)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := Open(rid, a.Snapshot(), "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		qty := int64(2)
		if i == shortLine {
			qty = 1
		}
		if _, err := r.ReceiveLine(i, qty, ConditionGood, t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Close(at(1)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdviceLinesAreCappedWithTheExceptionsFirst(t *testing.T) {
	r := manyLinesReceipt(t, 6, 5) // only line 5 has a discrepancy
	ev := adviceWith(t, r, adviceBounds{lines: 6, sscc: 10, findings: 10})
	if len(ev.Lines) != 6 || ev.LinesTotal != 6 || ev.LinesTruncated {
		t.Fatalf("at the cap: %d lines total %d truncated %v", len(ev.Lines), ev.LinesTotal, ev.LinesTruncated)
	}
	ev = adviceWith(t, r, adviceBounds{lines: 3, sscc: 10, findings: 10})
	if len(ev.Lines) != 3 || ev.LinesTotal != 6 || !ev.LinesTruncated {
		t.Fatalf("over the cap: %d lines total %d truncated %v", len(ev.Lines), ev.LinesTotal, ev.LinesTruncated)
	}
	var got []int
	for _, l := range ev.Lines {
		got = append(got, l.LineNo)
	}
	// Line 5 is the one with an exception: it is kept, then lines 1 and 2; the lines are in line order.
	if !reflect.DeepEqual(got, []int{1, 2, 5}) {
		t.Fatalf("lines kept = %v, want [1 2 5]", got)
	}
	ev = adviceWith(t, r, adviceBounds{lines: 5, sscc: 10, findings: 10})
	if len(ev.Lines) != 5 || !ev.LinesTruncated || ev.Lines[4].LineNo != 5 {
		t.Fatalf("one under the total: %d lines, last %d", len(ev.Lines), ev.Lines[len(ev.Lines)-1].LineNo)
	}
}

func TestAdviceLinesWithMoreExceptionsThanTheCapKeepTheFirstOnes(t *testing.T) {
	r := manyShortLinesReceipt(t, 5) // five exceptions for a cap of three
	ev := adviceWith(t, r, adviceBounds{lines: 3, sscc: 10, findings: 10})
	var got []int
	for _, l := range ev.Lines {
		got = append(got, l.LineNo)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) || ev.LinesTotal != 5 || !ev.LinesTruncated {
		t.Fatalf("lines kept = %v total %d truncated %v, want [1 2 3] of 5", got, ev.LinesTotal, ev.LinesTruncated)
	}
}

func manyShortLinesReceipt(t *testing.T, n int) *Receipt {
	t.Helper()
	inputs := make([]asn.LineInput, 0, n)
	for i := 1; i <= n; i++ {
		inputs = append(inputs, asn.LineInput{LineNo: i, SKU: fmt.Sprintf("SKU-%d", i), ExpectedQty: 2})
	}
	a, _, err := asn.Register(asnNum, "ACME", time.Time{}, inputs, t0)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := Open(rid, a.Snapshot(), "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(1)); err != nil { // nothing received: every line is short
		t.Fatal(err)
	}
	return r
}

func TestIssueAdviceUsesTheRealBounds(t *testing.T) {
	r := manyLinesReceipt(t, MaxAdviceLines+1, MaxAdviceLines+1)
	ev, err := r.IssueAdvice("ACME", sla24h, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Lines) != MaxAdviceLines || ev.LinesTotal != MaxAdviceLines+1 || !ev.LinesTruncated {
		t.Fatalf("%d lines total %d truncated %v", len(ev.Lines), ev.LinesTotal, ev.LinesTruncated)
	}
	if ev.Lines[len(ev.Lines)-1].LineNo != MaxAdviceLines+1 {
		t.Fatal("the line with an exception must survive the cap")
	}
}
