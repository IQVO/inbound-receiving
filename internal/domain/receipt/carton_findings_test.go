package receipt

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestFreshReceiptHasNoCartonDiscrepancies(t *testing.T) {
	got := openedCartons(t).CartonDiscrepancies()
	if got == nil || len(got) != 0 {
		t.Fatalf("CartonDiscrepancies = %#v, want an empty non-nil slice", got)
	}
}

func TestEveryRaisedFindingIsKeptWithItsInstantInRaiseOrder(t *testing.T) {
	r := openedCartons(t)
	// UnexpectedCarton at minute 1 (rejected scan).
	if _, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	// Accepted but over limit at minute 2.
	over := scanOf(t, ssccA(t), 20)
	over.Declared, over.Limits = DeclaredCarton{GrossWeightG: 30}, CartonLimits{MaxGrossWeightG: 20}
	if _, err := r.AcceptCarton(over, at(2)); err != nil {
		t.Fatal(err)
	}
	// DuplicateSscc at minute 3.
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(3)); err != nil {
		t.Fatal(err)
	}
	// UnknownGtin by GTIN at minute 4.
	if _, err := r.RejectUnknownGtin(eachGTIN1(t), at(4)); err != nil {
		t.Fatal(err)
	}
	// Two UnlabelledCarton at minute 5.
	if _, err := r.ReceiveCases(caseScan(t, 2, "LPN-A", "LPN-B"), at(5)); err != nil {
		t.Fatal(err)
	}
	want := []CartonFinding{
		{CartonDiscrepancy{Kind: KindUnexpectedCarton, SSCC: ssccX(t)}, at(1)},
		{CartonDiscrepancy{Kind: KindCartonOverLimit, SSCC: ssccA(t), Expected: "grossWeightG:20", Actual: "grossWeightG:30"}, at(2)},
		{CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: ssccA(t)}, at(3)},
		{CartonDiscrepancy{Kind: KindUnknownGtin, Actual: eachGTIN1(t).String()}, at(4)},
		{CartonDiscrepancy{Kind: KindUnlabelledCarton, Actual: "LPN-A"}, at(5)},
		{CartonDiscrepancy{Kind: KindUnlabelledCarton, Actual: "LPN-B"}, at(5)},
	}
	if got := r.CartonDiscrepancies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CartonDiscrepancies =\n%+v\nwant\n%+v", got, want)
	}
}

func TestMixedCountFindingsAreKept(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccM(t), 0)
	in.Counted = []CountedContent{{SKU: "SKU-1", Qty: 9}, {SKU: "SKU-2", Qty: 5}}
	res, err := r.AcceptCarton(in, at(1))
	if err != nil || len(res.Discrepancies) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	got := r.CartonDiscrepancies()
	if len(got) != 1 || got[0].Kind != KindMixedCartonContentMismatch || got[0].CartonDiscrepancy != res.Discrepancies[0] || !got[0].At.Equal(at(1)) {
		t.Fatalf("kept = %+v, raised = %+v", got, res.Discrepancies)
	}
}

func TestKeptFindingsMatchTheRaisedEventsOneForOne(t *testing.T) {
	r := openedCartons(t)
	var raised []CartonDiscrepancy
	collect := func(events []Event) {
		for _, e := range events {
			if d, ok := e.(CartonDiscrepancyRaised); ok {
				raised = append(raised, d.CartonDiscrepancy)
			}
		}
	}
	res, _ := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(1))
	collect(res.Events)
	res, _ = r.ReceiveCases(caseScan(t, 1, "LPN-A"), at(2))
	collect(res.Events)
	events, err := r.Close(at(3))
	if err != nil {
		t.Fatal(err)
	}
	collect(events)
	var kept []CartonDiscrepancy
	for _, f := range r.CartonDiscrepancies() {
		kept = append(kept, f.CartonDiscrepancy)
	}
	if len(raised) != 5 || !reflect.DeepEqual(kept, raised) {
		t.Fatalf("kept = %+v\nraised = %+v", kept, raised)
	}
}

func TestCloseKeepsMissingCartonsAfterTheScanFindings(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(scanOf(t, ssccB(t), 10), at(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	want := []CartonFinding{
		{CartonDiscrepancy{Kind: KindUnexpectedCarton, SSCC: ssccX(t)}, at(1)},
		{CartonDiscrepancy{Kind: KindMissingCarton, Expected: ssccA(t).String()}, at(9)},
		{CartonDiscrepancy{Kind: KindMissingCarton, Expected: ssccM(t).String()}, at(9)},
	}
	if got := r.CartonDiscrepancies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CartonDiscrepancies =\n%+v\nwant\n%+v", got, want)
	}
}

func TestARefusedSecondCloseKeepsNoMoreFindings(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(2)); err != nil {
		t.Fatal(err)
	}
	n := len(r.CartonDiscrepancies())
	if _, err := r.Close(at(3)); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("second Close err = %v", err)
	}
	if got := len(r.CartonDiscrepancies()); got != n {
		t.Fatalf("a refused close changed the findings: %d -> %d", n, got)
	}
}

func TestRefusedOperationsKeepNoFinding(t *testing.T) {
	r := openedCartons(t)
	receive(t, r, 1, maxQty-5, ConditionGood)
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(1)); err == nil {
		t.Fatal("overflow must be refused")
	}
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-A", "LPN-B"), at(2)); err == nil {
		t.Fatal("lpn mismatch must be refused")
	}
	if got := r.CartonDiscrepancies(); len(got) != 0 {
		t.Fatalf("findings = %+v, want none", got)
	}
}

func TestCartonDiscrepanciesAreCopiedOut(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	got := r.CartonDiscrepancies()
	got[0].Kind = KindMissingCarton
	if r.CartonDiscrepancies()[0].Kind != KindUnexpectedCarton {
		t.Fatal("the caller changed the receipt's findings")
	}
}

func TestFindingInstantIsUTC(t *testing.T) {
	r := openedCartons(t)
	local := at(1).In(time.FixedZone("BRT", -3*3600))
	if _, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), local); err != nil {
		t.Fatal(err)
	}
	if got := r.CartonDiscrepancies()[0].At; got.Location() != time.UTC || !got.Equal(at(1)) {
		t.Fatalf("At = %v", got)
	}
}

func TestPersistedRoundTripsTheCartonDiscrepancies(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(2)); err != nil {
		t.Fatal(err)
	}
	p := r.Persisted()
	if len(p.CartonDiscrepancies) != 4 {
		t.Fatalf("persisted = %+v", p.CartonDiscrepancies)
	}
	back, err := Rehydrate(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Persisted(), p) || !reflect.DeepEqual(back.CartonDiscrepancies(), r.CartonDiscrepancies()) {
		t.Fatalf("round trip changed the findings:\n%+v\n%+v", back.CartonDiscrepancies(), r.CartonDiscrepancies())
	}
}

func TestRehydrateRefusesAMalformedCartonDiscrepancy(t *testing.T) {
	base := openedCartons(t).Persisted()
	good := CartonFindingState{Kind: string(KindUnexpectedCarton), SSCC: ssccX(t).String(), At: at(1)}
	if _, err := Rehydrate(withFinding(base, good)); err != nil {
		t.Fatalf("a well-formed finding must rehydrate: %v", err)
	}
	tests := map[string]CartonFindingState{
		"unknown kind": {Kind: "Short", SSCC: good.SSCC, At: at(1)},
		"empty kind":   {SSCC: good.SSCC, At: at(1)},
		"bad sscc":     {Kind: good.Kind, SSCC: "123", At: at(1)},
		"no instant":   {Kind: good.Kind, SSCC: good.SSCC},
	}
	for name, f := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Rehydrate(withFinding(base, f)); !errors.Is(err, ErrInvalidCartonDiscrepancy) {
				t.Fatalf("err = %v, want ErrInvalidCartonDiscrepancy", err)
			}
		})
	}
	for _, kind := range []Kind{KindUnexpectedCarton, KindMissingCarton, KindDuplicateSscc, KindUnknownGtin, KindCaseQtyMismatch,
		KindMixedCaseDeclaredAsCasePack, KindUnlabelledCarton, KindCartonOverLimit, KindMixedCartonContentMismatch} {
		if _, err := Rehydrate(withFinding(base, CartonFindingState{Kind: string(kind), At: at(1)})); err != nil {
			t.Fatalf("kind %s must rehydrate: %v", kind, err)
		}
	}
}

func withFinding(p Persisted, f CartonFindingState) Persisted {
	p.CartonDiscrepancies = []CartonFindingState{f}
	return p
}
