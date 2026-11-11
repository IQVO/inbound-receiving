package receipt

import (
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func TestEarliestDate(t *testing.T) {
	tests := []struct {
		name  string
		dates []string
		want  string
	}{
		{"no date", nil, ""},
		{"one date", []string{"2027-05-10"}, "2027-05-10"},
		{"equal dates give the date", []string{"2027-05-10", "2027-05-10"}, "2027-05-10"},
		{"earlier first", []string{"2027-03-01", "2027-05-10"}, "2027-03-01"},
		{"earlier last", []string{"2027-05-10", "2027-03-01"}, "2027-03-01"},
		{"earlier in the middle", []string{"2027-05-10", "2027-03-01", "2027-04-01"}, "2027-03-01"},
		{"a day apart", []string{"2027-05-11", "2027-05-10"}, "2027-05-10"},
		{"a month apart", []string{"2027-06-01", "2027-05-31"}, "2027-05-31"},
		{"a year apart", []string{"2028-01-01", "2027-12-31"}, "2027-12-31"},
		{"day 00 is later than a real day of the same month", []string{"2027-03-00", "2027-03-15"}, "2027-03-15"},
		{"a real day before day 00 of the same month", []string{"2027-03-15", "2027-03-00"}, "2027-03-15"},
		{"day 00 equals the last day, the first keeps", []string{"2027-03-00", "2027-03-31"}, "2027-03-00"},
		{"last day then day 00, the first keeps", []string{"2027-03-31", "2027-03-00"}, "2027-03-31"},
		{"day 00 is before the next month", []string{"2027-04-01", "2027-03-00"}, "2027-03-00"},
		{"day 00 of a short month equals its 30th", []string{"2027-04-00", "2027-04-30"}, "2027-04-00"},
		{"day 00 of April is after its 29th", []string{"2027-04-00", "2027-04-29"}, "2027-04-29"},
		{"day 00 of February in a common year equals the 28th", []string{"2027-02-28", "2027-02-00"}, "2027-02-28"},
		{"day 00 of February in a leap year is the 29th", []string{"2028-02-00", "2028-02-28"}, "2028-02-28"},
		{"day 00 of February in a leap year equals the 29th", []string{"2028-02-29", "2028-02-00"}, "2028-02-29"},
		{"the empty string is skipped", []string{"", "2027-05-10"}, "2027-05-10"},
		{"a string that is not a date is skipped", []string{"garbage", "2027-05-10"}, "2027-05-10"},
		{"month 13 is skipped", []string{"2027-13-01", "2027-05-10"}, "2027-05-10"},
		{"month 0 is skipped", []string{"2027-00-10", "2027-05-10"}, "2027-05-10"},
		{"day 31 of April is skipped", []string{"2027-04-31", "2027-05-10"}, "2027-05-10"},
		{"day 30 of February is skipped", []string{"2027-02-30", "2027-05-10"}, "2027-05-10"},
		{"only invalid dates", []string{"x", "2027-13-01"}, ""},
		{"a short date is skipped", []string{"2027-5-1", "2027-05-10"}, "2027-05-10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EarliestDate(tt.dates...); got != tt.want {
				t.Fatalf("EarliestDate(%q) = %q, want %q", tt.dates, got, tt.want)
			}
		})
	}
}

// palletReceipt rebuilds a receipt whose ledger has: three accepted cartons on pallet P
// (best-before May 10, March "00" and none), one accepted carton on pallet Q, an EXPECTED carton on P
// with an earlier date (never counts) and an accepted carton with no pallet.
func palletReceipt(t *testing.T) *Receipt {
	t.Helper()
	p, q := pallet(t).String(), mkSSCC(t, "00345678908888888").String()
	g := eachGTIN1(t).String()
	accepted := func(sscc, parent string, qty int64, label LabelData, expiry string) CartonState {
		return CartonState{
			LPN: sscc, SSCC: sscc, ParentSSCC: parent, Kind: "HOMOGENEOUS_CASE", Status: CartonAccepted, Sealed: true, ReceivedAt: at(2),
			Contents: []CartonContentState{{LineNo: 1, SKU: "SKU-1", GTIN: g, Qty: qty, Expiry: expiry, Condition: "Good"}}, Label: label,
		}
	}
	line2 := CartonState{
		LPN: "LPN-X", Kind: "HOMOGENEOUS_CASE", Status: CartonAccepted, Sealed: true, ReceivedAt: at(2),
		Contents: []CartonContentState{{LineNo: 2, SKU: "SKU-2", GTIN: eachGTIN2(t).String(), Qty: 5, Condition: "Good"}},
		Label:    LabelData{BestBefore: "2026-12-01"},
	}
	expected := CartonState{
		LPN: ssccX(t).String(), SSCC: ssccX(t).String(), ParentSSCC: p, Kind: "HOMOGENEOUS_CASE", Status: CartonExpected,
		Contents: []CartonContentState{{LineNo: 1, SKU: "SKU-1", GTIN: g, Qty: 10}}, Label: LabelData{BestBefore: "2020-01-01"},
	}
	r, err := Rehydrate(Persisted{
		ID: rid, AsnNumber: asnNum, State: StateOpen, OpenedAt: t0, Version: 5,
		Lines: []LineState{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 60, ReceivedGood: 50}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5, ReceivedGood: 5}},
		Cartons: []CartonState{
			accepted(ssccA(t).String(), p, 20, LabelData{BestBefore: "2027-05-10"}, ""),
			accepted(ssccB(t).String(), p, 10, LabelData{BestBefore: "2027-03-00", Expiry: "2027-09-30"}, "2028-01-01"),
			accepted(ssccM(t).String(), p, 10, LabelData{}, "2027-11-30"),
			accepted(mkSSCC(t, "00345678907777777").String(), q, 10, LabelData{BestBefore: "2027-07-01"}, ""),
			expected, line2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPalletsReportTheEarliestDatesOfAMixedPallet(t *testing.T) {
	r := palletReceipt(t)
	want := []Pallet{
		{ParentSSCC: pallet(t), CartonCount: 3, Earliest: EarliestDates{BestBefore: "2027-03-00", Expiry: "2027-09-30"}},
		{ParentSSCC: mkSSCC(t, "00345678908888888"), CartonCount: 1, Earliest: EarliestDates{BestBefore: "2027-07-01"}},
	}
	got := r.Pallets()
	if len(got) != len(want) {
		t.Fatalf("Pallets() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Pallets()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPalletsOfAReceiptWithoutPalletsIsEmpty(t *testing.T) {
	if got := openedCartons(t).Pallets(); len(got) != 0 {
		t.Fatalf("Pallets() of a receipt with nothing received = %+v", got)
	}
	if got := opened(t).Pallets(); len(got) != 0 {
		t.Fatalf("Pallets() of a flat receipt = %+v", got)
	}
}

func TestLineDatesAreTheEarliestOverTheCartonsOfTheLine(t *testing.T) {
	r := palletReceipt(t)
	tests := []struct {
		lineNo int
		want   EarliestDates
	}{
		{1, EarliestDates{BestBefore: "2027-03-00", Expiry: "2027-09-30"}},
		{2, EarliestDates{BestBefore: "2026-12-01"}},
		{3, EarliestDates{}},
	}
	for _, tt := range tests {
		if got := r.LineDates(tt.lineNo); got != tt.want {
			t.Errorf("LineDates(%d) = %+v, want %+v", tt.lineNo, got, tt.want)
		}
	}
}

func TestACartonExpiryIsItsLabelExpiryElseItsEarliestContentExpiry(t *testing.T) {
	r, err := Rehydrate(Persisted{
		ID: rid, AsnNumber: asnNum, State: StateOpen, OpenedAt: t0, Version: 2,
		Lines: []LineState{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40, ReceivedGood: 30}},
		Cartons: []CartonState{{
			LPN: "LPN-A", Kind: "HOMOGENEOUS_CASE", Status: CartonAccepted, Sealed: true, ReceivedAt: at(1),
			Contents: []CartonContentState{
				{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 10, Expiry: "2028-02-00", Condition: "Good"},
				{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 20, Expiry: "2028-02-10", Condition: "Good"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.LineDates(1); got.Expiry != "2028-02-10" || got.BestBefore != "" {
		t.Fatalf("LineDates(1) = %+v, want the earliest content expiry 2028-02-10 (day 00 is the 29th)", got)
	}
}

func TestAcceptCartonKeepsTheLabelDataAndMarksTheSupplierSource(t *testing.T) {
	r := openedCartons(t)
	label := LabelData{
		BestBefore: "2027-03-31", Expiry: "2027-06-30", ProductionDate: "2026-09-01", PackagingDate: "2026-09-02",
		Serial: "S1", PurchaseOrder: "PO-77", ShipToGLN: "4012345000009",
	}
	in := scanOf(t, ssccA(t), 20)
	in.Label = label
	res, err := r.AcceptCarton(in, at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
	c, _ := r.Carton(ssccA(t))
	if c.Label() != label || c.SSCCSource() != SsccSupplier {
		t.Fatalf("carton label = %+v source %q", c.Label(), c.SSCCSource())
	}
	ev := res.Events[1].(ReceiptCartonReceived)
	if ev.Label != label || ev.SSCCSource != SsccSupplier {
		t.Fatalf("event label = %+v source %q", ev.Label, ev.SSCCSource)
	}
	if got := r.LineDates(1); got.BestBefore != "2027-03-31" || got.Expiry != "2027-06-30" {
		t.Fatalf("LineDates(1) = %+v", got)
	}
	// An Expected carton shows no source on the wire of an event but derives "supplier" from its SSCC.
	if b, _ := r.Carton(ssccB(t)); b.SSCCSource() != SsccSupplier || !b.Label().IsZero() {
		t.Fatalf("expected carton B: source %q label %+v", b.SSCCSource(), b.Label())
	}
}

func TestACartonWithoutAnSSCCHasNoSource(t *testing.T) {
	r := opened(t)
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-A"), at(1)); err != nil {
		t.Fatal(err)
	}
	c, _ := r.CartonByLPN("LPN-A")
	if c.SSCCSource() != "" {
		t.Fatalf("source = %q, want none for a carton with no SSCC", c.SSCCSource())
	}
}

func TestCaseScanLabelDataGoesToEveryCartonOfTheScan(t *testing.T) {
	r := opened(t)
	in := caseScan(t, 2, "LPN-A", "LPN-B")
	in.Label = LabelData{BestBefore: "2027-01-31", PurchaseOrder: "PO-1"}
	res, err := r.ReceiveCases(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Events[1:] {
		if ev := e.(ReceiptCartonReceived); ev.Label != in.Label {
			t.Fatalf("event label = %+v", ev.Label)
		}
	}
	if got := r.LineDates(1); got.BestBefore != "2027-01-31" || got.Expiry != "2028-01-31" {
		t.Fatalf("LineDates(1) = %+v (the case scan's own expiry is the content expiry)", got)
	}
}

func TestLabelDataIsZero(t *testing.T) {
	if !(LabelData{}).IsZero() {
		t.Fatal("the zero label is zero")
	}
	for name, l := range map[string]LabelData{
		"best before": {BestBefore: "x"}, "expiry": {Expiry: "x"}, "production": {ProductionDate: "x"}, "packaging": {PackagingDate: "x"},
		"serial": {Serial: "x"}, "po": {PurchaseOrder: "x"}, "ship-to": {ShipToGLN: "x"}, "ship-for": {ShipForGLN: "x"},
	} {
		if l.IsZero() {
			t.Errorf("a label with only %s must not be zero", name)
		}
	}
}

func TestPersistedRoundTripsLabelDataAndSource(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.Label = LabelData{BestBefore: "2027-03-31", ShipToGLN: "4012345000009"}
	if _, err := r.AcceptCarton(in, at(1)); err != nil {
		t.Fatal(err)
	}
	assigned := assignScan(t, "006141410000000012")
	if _, err := r.AssignSscc(assigned, at(2)); err != nil {
		t.Fatal(err)
	}
	p := r.Persisted()
	if p.Cartons[0].Label != in.Label || p.Cartons[0].SSCCSource != "" {
		t.Fatalf("persisted supplier carton = %+v", p.Cartons[0])
	}
	if last := p.Cartons[len(p.Cartons)-1]; last.SSCCSource != "receiver" {
		t.Fatalf("persisted assigned carton = %+v", last)
	}
	back, err := Rehydrate(p)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := back.Carton(ssccA(t)); c.Label() != in.Label || c.SSCCSource() != SsccSupplier {
		t.Fatalf("rehydrated carton = %+v", c)
	}
	if c, ok := back.CartonByLPN("006141410000000012"); !ok || c.SSCCSource() != SsccReceiver {
		t.Fatalf("rehydrated assigned carton = %+v, %v", c, ok)
	}
	bad := r.Persisted()
	bad.Cartons[0].SSCCSource = "somebody"
	if _, err := Rehydrate(bad); err == nil {
		t.Fatal("an unknown sscc source was accepted")
	}
}

func TestAdviceLinesCarryTheEarliestDates(t *testing.T) {
	r := palletReceipt(t)
	if _, err := r.Close(at(9)); err != nil {
		t.Fatal(err)
	}
	ev, err := r.IssueAdvice("ACME", 24*time.Hour, at(10))
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.Lines[0]; got.EarliestBestBefore != "2027-03-00" || got.EarliestExpiry != "2027-09-30" {
		t.Fatalf("advice line 1 = %+v", got)
	}
	if got := ev.Lines[1]; got.EarliestBestBefore != "2026-12-01" || got.EarliestExpiry != "" {
		t.Fatalf("advice line 2 = %+v", got)
	}
}

var _ = gs1.SSCC{}
