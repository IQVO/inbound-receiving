package receipt

import (
	"errors"
	"testing"
)

func findingsOf(res ScanResult) []CartonDiscrepancy { return res.Discrepancies }

func countRaised(res ScanResult, kind Kind) int {
	n := 0
	for _, e := range res.Events {
		if d, ok := e.(CartonDiscrepancyRaised); ok && d.Kind == kind {
			n++
		}
	}
	return n
}

func TestOverLimitFlagsButStillAccepts(t *testing.T) {
	tests := []struct {
		name         string
		declared     DeclaredCarton
		limits       CartonLimits
		wantExpected string
		wantActual   string
	}{
		{"weight over", DeclaredCarton{GrossWeightG: 25000, LongestSideMm: 500}, CartonLimits{MaxGrossWeightG: 20000, MaxLongestSideMm: 1200},
			"grossWeightG:20000", "grossWeightG:25000"},
		{"side over", DeclaredCarton{GrossWeightG: 100, LongestSideMm: 1300}, CartonLimits{MaxGrossWeightG: 20000, MaxLongestSideMm: 1200},
			"longestSideMm:1200", "longestSideMm:1300"},
		{"both over", DeclaredCarton{GrossWeightG: 25000, LongestSideMm: 1300}, CartonLimits{MaxGrossWeightG: 20000, MaxLongestSideMm: 1200},
			"grossWeightG:20000,longestSideMm:1200", "grossWeightG:25000,longestSideMm:1300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := openedCartons(t)
			res, err := r.AcceptCarton(CartonScan{SSCC: ssccA(t), Condition: ConditionGood, Declared: tt.declared, Limits: tt.limits}, at(1))
			if err != nil || !res.Accepted {
				t.Fatalf("accepted=%v err=%v: an over-limit carton is on the dock and must be accepted", res.Accepted, err)
			}
			want := []CartonDiscrepancy{{Kind: KindCartonOverLimit, SSCC: ssccA(t), Expected: tt.wantExpected, Actual: tt.wantActual}}
			if got := findingsOf(res); len(got) != 1 || got[0] != want[0] {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
			if raised := countRaised(res, KindCartonOverLimit); raised != 1 {
				t.Fatalf("CartonDiscrepancyRaised events = %d, want 1", raised)
			}
			if got := r.Lines()[0].ReceivedGood(); got != 20 {
				t.Fatalf("received = %d, want the carton's 20", got)
			}
		})
	}
}

func TestOverLimitBoundariesAndUncheckedLimits(t *testing.T) {
	tests := []struct {
		name     string
		declared DeclaredCarton
		limits   CartonLimits
	}{
		{"exactly at the limit", DeclaredCarton{GrossWeightG: 20000, LongestSideMm: 1200}, CartonLimits{MaxGrossWeightG: 20000, MaxLongestSideMm: 1200}},
		{"limits unset", DeclaredCarton{GrossWeightG: 99999, LongestSideMm: 9999}, CartonLimits{}},
		{"nothing declared", DeclaredCarton{}, CartonLimits{MaxGrossWeightG: 1, MaxLongestSideMm: 1}},
		{"only weight limit and side huge", DeclaredCarton{GrossWeightG: 5, LongestSideMm: 9999}, CartonLimits{MaxGrossWeightG: 10}},
		{"only side limit and weight huge", DeclaredCarton{GrossWeightG: 99999, LongestSideMm: 5}, CartonLimits{MaxLongestSideMm: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := openedCartons(t).AcceptCarton(CartonScan{SSCC: ssccA(t), Condition: ConditionGood, Declared: tt.declared, Limits: tt.limits}, at(1))
			if err != nil || !res.Accepted || len(res.Discrepancies) != 0 {
				t.Fatalf("accepted=%v findings=%+v err=%v, want a clean acceptance", res.Accepted, res.Discrepancies, err)
			}
		})
	}
}

func TestMixedCountMatchesManifestRaisesNothing(t *testing.T) {
	r := openedCartons(t)
	counted := []CountedContent{{SKU: "SKU-1", Qty: 10}, {SKU: "SKU-2", Qty: 5}}
	res, err := r.AcceptCarton(CartonScan{SSCC: ssccM(t), Condition: ConditionGood, Counted: counted}, at(1))
	if err != nil || !res.Accepted || len(res.Discrepancies) != 0 {
		t.Fatalf("accepted=%v findings=%+v err=%v", res.Accepted, res.Discrepancies, err)
	}
}

func TestMixedCountDifferenceBooksTheCountedQuantity(t *testing.T) {
	r := openedCartons(t)
	counted := []CountedContent{{SKU: "SKU-1", Qty: 8}, {SKU: "SKU-2", Qty: 5}}
	res, err := r.AcceptCarton(CartonScan{SSCC: ssccM(t), Condition: ConditionGood, Counted: counted}, at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("accepted=%v err=%v", res.Accepted, err)
	}
	want := CartonDiscrepancy{Kind: KindMixedCartonContentMismatch, SSCC: ssccM(t), Expected: "SKU-1:10", Actual: "SKU-1:8"}
	if got := res.Discrepancies; len(got) != 1 || got[0] != want {
		t.Fatalf("findings = %+v, want [%+v]", got, want)
	}
	if got := r.Lines()[0].ReceivedGood(); got != 8 {
		t.Fatalf("SKU-1 received = %d, want the COUNTED 8, not the label's 10", got)
	}
	if got := r.Lines()[1].ReceivedGood(); got != 5 {
		t.Fatalf("SKU-2 received = %d, want 5", got)
	}
	c, _ := r.Carton(ssccM(t))
	if cs := c.Contents(); len(cs) != 2 || cs[0].Qty != 8 || cs[1].Qty != 5 {
		t.Fatalf("carton contents = %+v, want the counted quantities", cs)
	}
}

func TestMixedCountMissingAndExtraSkus(t *testing.T) {
	r := openedCartons(t)
	// SKU-2 was not counted at all (found none); the count has nothing for it.
	res, err := r.AcceptCarton(CartonScan{SSCC: ssccM(t), Condition: ConditionGood, Counted: []CountedContent{{SKU: "SKU-1", Qty: 10}}}, at(1))
	if err != nil {
		t.Fatal(err)
	}
	want := CartonDiscrepancy{Kind: KindMixedCartonContentMismatch, SSCC: ssccM(t), Expected: "SKU-2:5", Actual: "SKU-2:0"}
	if got := res.Discrepancies; len(got) != 1 || got[0] != want {
		t.Fatalf("findings = %+v, want [%+v]", got, want)
	}
	if got := r.Lines()[1].Received(); got != 0 {
		t.Fatalf("SKU-2 received = %d, want 0", got)
	}

	// A SKU the manifest does not list, counted in the carton.
	r2 := openedCartons(t)
	extra := []CountedContent{{SKU: "SKU-1", Qty: 10}, {SKU: "SKU-2", Qty: 5}}
	m := r2.cartons[2]
	m.contents = m.contents[:1] // the manifest lists SKU-1 only
	r2.cartons[2] = m
	extra[1].GTIN = eachGTIN2(t)
	res, err = r2.AcceptCarton(CartonScan{SSCC: ssccM(t), Condition: ConditionGood, Counted: extra}, at(1))
	if err != nil {
		t.Fatal(err)
	}
	want = CartonDiscrepancy{Kind: KindMixedCartonContentMismatch, SSCC: ssccM(t), Expected: "SKU-2:0", Actual: "SKU-2:5"}
	if got := res.Discrepancies; len(got) != 1 || got[0] != want {
		t.Fatalf("findings = %+v, want [%+v]", got, want)
	}
	if got := r2.Lines()[1].ReceivedGood(); got != 5 {
		t.Fatalf("extra SKU received = %d, want 5", got)
	}
}

func TestMixedCountRefusals(t *testing.T) {
	tests := []struct {
		name    string
		counted []CountedContent
		homog   bool
		want    error
	}{
		{name: "counted on a homogeneous carton", counted: []CountedContent{{SKU: "SKU-1", Qty: 20}}, homog: true, want: ErrCountedContentsNotMixed},
		{name: "repeated sku", counted: []CountedContent{{SKU: "SKU-1", Qty: 1}, {SKU: "SKU-1", Qty: 2}}, want: ErrInvalidCount},
		{name: "negative quantity", counted: []CountedContent{{SKU: "SKU-1", Qty: -1}}, want: ErrInvalidCount},
		{name: "sku not on the asn", counted: []CountedContent{{SKU: "SKU-1", Qty: 10}, {SKU: "SKU-9", Qty: 1}}, want: ErrLineNotOnAsn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := openedCartons(t)
			sscc := ssccM(t)
			if tt.homog {
				sscc = ssccA(t)
			}
			before := r.Version()
			_, err := r.AcceptCarton(CartonScan{SSCC: sscc, Condition: ConditionGood, Counted: tt.counted}, at(1))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if r.Version() != before || r.Lines()[0].Received() != 0 {
				t.Fatalf("a refused count must change nothing (version %d -> %d)", before, r.Version())
			}
		})
	}
}

func TestCartonByLPN(t *testing.T) {
	r := openedCartons(t)
	if c, ok := r.CartonByLPN(ssccA(t).String()); !ok || c.SSCC() != ssccA(t) {
		t.Fatalf("CartonByLPN(sscc) = %+v, %v", c, ok)
	}
	if _, ok := r.CartonByLPN("LPN-NOPE"); ok {
		t.Fatal("an unknown lpn must not be found")
	}
	if _, ok := r.CartonByLPN(""); ok {
		t.Fatal("an empty lpn must not match an unlabelled entry")
	}
}
