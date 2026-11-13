package receipt

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// band is the ADR 0026 working example: 5 % over tolerance, 10 % hard stop,
// 2 % under tolerance, 20 % under hard stop, over a 10000-unit line so the
// plain-literal boundaries land on whole units.
var band = Band{Over: 0.05, OverStop: 0.10, Under: 0.02, UnderStop: 0.20}

// toleranceOpened opens a receipt against a one-line ASN of expected units.
func toleranceOpened(t *testing.T, expected int64) *Receipt {
	t.Helper()
	a, _, err := asn.Register(asnNum, "ACME", time.Time{}, []asn.LineInput{
		{LineNo: 1, SKU: "SKU-1", ExpectedQty: expected},
	}, t0)
	if err != nil {
		t.Fatalf("asn.Register: %v", err)
	}
	r, _, err := Open(rid, a.Snapshot(), apptID, doorOne, t0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

// closedKinds returns the kinds of the ReceiptClosed event of a close.
func closedKinds(t *testing.T, events []Event) []Kind {
	t.Helper()
	for _, e := range events {
		if c, ok := e.(ReceiptClosed); ok {
			out := make([]Kind, 0, len(c.Discrepancies))
			for _, d := range c.Discrepancies {
				out = append(out, d.Kind)
			}
			return out
		}
	}
	t.Fatalf("no ReceiptClosed in %+v", events)
	return nil
}

func TestBandDeclarationAndSides(t *testing.T) {
	if (Band{}).Declared() {
		t.Fatal("the zero band is declared")
	}
	if !band.Declared() || !band.OverDeclared() || !band.UnderDeclared() {
		t.Fatal("the working example is not fully declared")
	}
	overOnly := Band{Over: 0.05, OverStop: 0.10}
	if !overOnly.Declared() || !overOnly.OverDeclared() || overOnly.UnderDeclared() {
		t.Fatal("an over-only band must leave the under side undeclared")
	}
}

func TestParseOverrideReasonAcceptsOnlyTheClosedList(t *testing.T) {
	for _, c := range []string{"supplier-agreed", "damaged-in-transit-recounted", "mislabelled-recounted", "recount-confirmed"} {
		if _, err := ParseOverrideReason(c); err != nil {
			t.Fatalf("ParseOverrideReason(%q): %v", c, err)
		}
	}
	if _, err := ParseOverrideReason("because"); !errors.Is(err, ErrInvalidOverrideReason) {
		t.Fatalf("err = %v, want ErrInvalidOverrideReason", err)
	}
	if _, err := ParseOverrideReason(""); !errors.Is(err, ErrInvalidOverrideReason) {
		t.Fatalf("empty reason: %v, want ErrInvalidOverrideReason", err)
	}
}

// The three bands of ADR 0026, on the over side, with the boundary values the
// mutation gate demands (0.0499 / 0.05 / 0.0501 and the hard stop equal).
func TestCloseUnderPolicyOverSide(t *testing.T) {
	cases := []struct {
		name     string
		received int64
		reason   OverrideReason
		wantErr  error
		want     []Kind
	}{
		{"3% over auto-accepts inside a 5% band", 10300, "", nil, nil},
		{"4.99% over is inside", 10499, "", nil, nil},
		{"5% exactly is inside (equal is accepted)", 10500, "", nil, nil},
		{"5.01% over needs a reason", 10501, "", ErrToleranceOverrideRequired, nil},
		{"5.01% over with a reason closes on the tolerance kind", 10501, ReasonSupplierAgreed, nil, []Kind{KindOverTolerance}},
		{"9.99% over with a reason is still the override zone", 10999, ReasonRecountConfirmed, nil, []Kind{KindOverTolerance}},
		{"10% exactly is blocked (a hard stop stops)", 11000, ReasonSupplierAgreed, ErrToleranceBlocked, nil},
		{"12% over is blocked", 11200, ReasonSupplierAgreed, ErrToleranceBlocked, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := toleranceOpened(t, 10000)
			receive(t, r, 1, c.received, ConditionGood)
			events, err := r.CloseUnderPolicy(t0.Add(time.Hour), AppliedPolicy{Band: band, Reason: c.reason})
			assertCloseOutcome(t, r, events, err, c.wantErr, c.want)
		})
	}
}

// assertCloseOutcome checks one close-under-policy outcome: a wanted refusal
// leaves the receipt open; a wanted close raises exactly the wanted kinds.
func assertCloseOutcome(t *testing.T, r *Receipt, events []Event, err error, wantErr error, want []Kind) {
	t.Helper()
	if wantErr != nil {
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if r.State() != StateOpen {
			t.Fatal("a refused close must leave the receipt open")
		}
		return
	}
	if err != nil {
		t.Fatalf("CloseUnderPolicy: %v", err)
	}
	got := closedKinds(t, events)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
}

func TestCloseUnderPolicyUnderSide(t *testing.T) {
	t.Run("1% under is inside the 2% band", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 9900, ConditionGood)
		if _, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band}); err != nil {
			t.Fatalf("close: %v", err)
		}
	})
	t.Run("10% under needs a reason and closes on UnderTolerance", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 9000, ConditionGood)
		if _, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band}); !errors.Is(err, ErrToleranceOverrideRequired) {
			t.Fatalf("err = %v, want ErrToleranceOverrideRequired", err)
		}
		r2 := toleranceOpened(t, 10000)
		receive(t, r2, 1, 9000, ConditionGood)
		events, err := r2.CloseUnderPolicy(t0, AppliedPolicy{Band: band, Reason: ReasonMislabelledRecounted})
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if got := closedKinds(t, events); len(got) != 1 || got[0] != KindUnderTolerance {
			t.Fatalf("kinds = %v, want [UnderTolerance]", got)
		}
	})
	t.Run("25% under is blocked", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 7500, ConditionGood)
		if _, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band, Reason: ReasonSupplierAgreed}); !errors.Is(err, ErrToleranceBlocked) {
			t.Fatalf("err = %v, want ErrToleranceBlocked", err)
		}
	})
}

// An undeclared side keeps today's behaviour: the plain kind, no override.
func TestCloseUnderPolicyLeavesAnUndeclaredSideLegacy(t *testing.T) {
	overOnly := Band{Over: 0.05, OverStop: 0.10}
	r := toleranceOpened(t, 100)
	receive(t, r, 1, 50, ConditionGood) // 50% under, under side undeclared
	events, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: overOnly})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := closedKinds(t, events); len(got) != 1 || got[0] != KindShort {
		t.Fatalf("kinds = %v, want [Short]", got)
	}
}

// The zero applied policy is exactly Close: today's kinds, no tolerance fact.
func TestCloseUnderPolicyWithoutAPolicyIsLegacy(t *testing.T) {
	r := toleranceOpened(t, 100)
	receive(t, r, 1, 103, ConditionGood)
	events, err := r.CloseUnderPolicy(t0, AppliedPolicy{})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := closedKinds(t, events); len(got) != 1 || got[0] != KindOver {
		t.Fatalf("kinds = %v, want [Over]", got)
	}
	for _, e := range events {
		if c, ok := e.(ReceiptClosed); ok && c.Tolerance != nil {
			t.Fatal("a legacy close carries a tolerance fact")
		}
	}
}

func TestClosedUnderPolicyCarriesTheFactAndTheReason(t *testing.T) {
	r := toleranceOpened(t, 10000)
	receive(t, r, 1, 10600, ConditionGood)
	events, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band, Reason: ReasonDamagedInTransitRecounted})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	var closed ReceiptClosed
	for _, e := range events {
		if c, ok := e.(ReceiptClosed); ok {
			closed = c
		}
	}
	if closed.Tolerance == nil || closed.Tolerance.Band != band || closed.Tolerance.Reason != ReasonDamagedInTransitRecounted {
		t.Fatalf("tolerance fact = %+v", closed.Tolerance)
	}
	if len(closed.Discrepancies) != 1 {
		t.Fatalf("discrepancies = %+v", closed.Discrepancies)
	}
	d := closed.Discrepancies[0]
	if d.Kind != KindOverTolerance || d.Reason != ReasonDamagedInTransitRecounted || d.ExpectedQty != 10000 || d.ReceivedQty != 10600 {
		t.Fatalf("discrepancy = %+v", d)
	}
	if w13, _ := CodingOf(d.Kind); w13.W13 != W13Over || w13.CBV != DispositionMismatchQuantity {
		t.Fatalf("coding of OverTolerance = %+v", w13)
	}
}

// Damaged is a different dimension: inside the band, the line still reports it.
func TestCloseUnderPolicyKeepsDamaged(t *testing.T) {
	r := toleranceOpened(t, 10000)
	receive(t, r, 1, 10200, ConditionGood)
	receive(t, r, 1, 5, ConditionDamaged)
	events, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := closedKinds(t, events); len(got) != 1 || got[0] != KindDamaged {
		t.Fatalf("kinds = %v, want [Damaged]", got)
	}
}

// The scan seam: over only, on the lines the scan booked, after the booking.
func TestCheckScanTolerance(t *testing.T) {
	t.Run("inside the band books on", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 10300, ConditionGood)
		if err := r.CheckScanTolerance(band, []int{1}, ""); err != nil {
			t.Fatalf("check: %v", err)
		}
	})
	t.Run("beyond the band without a reason is refused", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 10600, ConditionGood)
		if err := r.CheckScanTolerance(band, []int{1}, ""); !errors.Is(err, ErrToleranceOverrideRequired) {
			t.Fatalf("err = %v, want ErrToleranceOverrideRequired", err)
		}
	})
	t.Run("beyond the band with a reason passes", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 10600, ConditionGood)
		if err := r.CheckScanTolerance(band, []int{1}, ReasonSupplierAgreed); err != nil {
			t.Fatalf("check: %v", err)
		}
	})
	t.Run("at the hard stop no reason saves the booking", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 11000, ConditionGood)
		if err := r.CheckScanTolerance(band, []int{1}, ReasonSupplierAgreed); !errors.Is(err, ErrToleranceBlocked) {
			t.Fatalf("err = %v, want ErrToleranceBlocked", err)
		}
	})
	t.Run("a shortfall is not judged at the scan", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 1000, ConditionGood) // 90% under: close-time, not scan-time
		if err := r.CheckScanTolerance(band, []int{1}, ""); err != nil {
			t.Fatalf("check: %v", err)
		}
	})
	t.Run("an untouched line is not judged", func(t *testing.T) {
		r := toleranceOpened(t, 10000)
		receive(t, r, 1, 12000, ConditionGood) // 20% over on line 1...
		if err := r.CheckScanTolerance(band, nil, ""); err != nil {
			t.Fatalf("a scan that booked nothing judges nothing: %v", err)
		}
	})
}

func TestToleranceErrorCarriesTheBandAndTheVariance(t *testing.T) {
	r := toleranceOpened(t, 10000)
	receive(t, r, 1, 12000, ConditionGood)
	err := r.CheckScanTolerance(band, []int{1}, ReasonSupplierAgreed)
	var te *ToleranceError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *ToleranceError", err)
	}
	if !te.Blocked || !te.Over || te.LineNo != 1 || te.Expected != 10000 || te.Received != 12000 {
		t.Fatalf("error = %+v", te)
	}
	if te.Tolerance != 0.05 || te.HardStop != 0.10 {
		t.Fatalf("band in error = %v/%v", te.Tolerance, te.HardStop)
	}
	if want := 0.2; te.Variance != want {
		t.Fatalf("variance = %v, want %v", te.Variance, want)
	}
}

// The advice must agree with the close: within band no over/under entry, in
// the override zone the tolerance kind with the reason and its quantity.
func TestIssueAdviceUnderPolicy(t *testing.T) {
	r := toleranceOpened(t, 10000)
	receive(t, r, 1, 10600, ConditionGood)
	if _, err := r.CloseUnderPolicy(t0, AppliedPolicy{Band: band, Reason: ReasonSupplierAgreed}); err != nil {
		t.Fatalf("close: %v", err)
	}
	adv, err := r.IssueAdvice("ACME", time.Hour, t0, AppliedPolicy{Band: band, Reason: ReasonSupplierAgreed})
	if err != nil {
		t.Fatalf("advice: %v", err)
	}
	if len(adv.Lines) != 1 || len(adv.Lines[0].Discrepancies) != 1 {
		t.Fatalf("advice lines = %+v", adv.Lines)
	}
	d := adv.Lines[0].Discrepancies[0]
	if d.Kind != KindOverTolerance || d.Quantity != 600 || d.Reason != ReasonSupplierAgreed {
		t.Fatalf("advice discrepancy = %+v", d)
	}
	if d.Coding.W13 != W13Over || d.Coding.CBV != DispositionMismatchQuantity {
		t.Fatalf("advice coding = %+v", d.Coding)
	}

	inside := toleranceOpened(t, 10000)
	receive(t, inside, 1, 10300, ConditionGood)
	if _, err := inside.CloseUnderPolicy(t0, AppliedPolicy{Band: band}); err != nil {
		t.Fatalf("close: %v", err)
	}
	adv2, err := inside.IssueAdvice("ACME", time.Hour, t0, AppliedPolicy{Band: band})
	if err != nil {
		t.Fatalf("advice: %v", err)
	}
	if len(adv2.Lines[0].Discrepancies) != 0 {
		t.Fatalf("within-band advice entries = %+v", adv2.Lines[0].Discrepancies)
	}
}
