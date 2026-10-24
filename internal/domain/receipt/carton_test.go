package receipt

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const owner = "tenant-7f3a"

func mkSSCC(t *testing.T, payload string) gs1.SSCC {
	t.Helper()
	s, err := gs1.NewSSCC(payload)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mkGTIN(t *testing.T, payload string) gs1.GTIN {
	t.Helper()
	g, err := gs1.NewGTIN(payload)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Carton fixtures. SKU-1 x 40 is two homogeneous cases (20 + 10) and 10 in a
// mixed carton that also holds the 5 of SKU-2.
func ssccA(t *testing.T) gs1.SSCC { return mkSSCC(t, "00345678901234567") }
func ssccB(t *testing.T) gs1.SSCC { return mkSSCC(t, "00345678901234568") }
func ssccM(t *testing.T) gs1.SSCC { return mkSSCC(t, "00345678901234569") }
func ssccX(t *testing.T) gs1.SSCC { return mkSSCC(t, "00345678901234570") }
func pallet(t *testing.T) gs1.SSCC {
	return mkSSCC(t, "00345678909999999")
}
func eachGTIN1(t *testing.T) gs1.GTIN { return mkGTIN(t, "1000123456789") }
func eachGTIN2(t *testing.T) gs1.GTIN { return mkGTIN(t, "1000123456790") }
func caseGTIN1(t *testing.T) gs1.GTIN { return mkGTIN(t, "2000123456785") }

func cartonAsn(t *testing.T) *asn.Asn {
	t.Helper()
	g1, g2 := eachGTIN1(t).String(), eachGTIN2(t).String()
	a, _, err := asn.RegisterDraft(asn.Draft{
		Number: asnNum, SupplierRef: "ACME", OwnerRef: owner,
		Lines: []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5}},
		Cartons: []asn.CartonInput{
			{
				SSCC: ssccA(t).String(), ParentSSCC: pallet(t).String(), Kind: "HOMOGENEOUS_CASE",
				Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20, Lot: "L2310", Expiry: "2027-10-31"}},
			},
			{
				SSCC: ssccB(t).String(), Kind: "HOMOGENEOUS_CASE",
				Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 10}},
			},
			{
				SSCC: ssccM(t).String(), Kind: "MIXED",
				Contents: []asn.ContentInput{
					{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 10},
					{LineNo: 2, SKU: "SKU-2", GTIN: g2, Qty: 5},
				},
			},
		},
	}, t0)
	if err != nil {
		t.Fatalf("asn.RegisterDraft: %v", err)
	}
	return a
}

func openedCartons(t *testing.T) *Receipt {
	t.Helper()
	r, _, err := Open(rid, cartonAsn(t).Snapshot(), "", "", t0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func casePacks(t *testing.T, units int64) map[shared.SKU]CasePack {
	return map[shared.SKU]CasePack{"SKU-1": {GTIN: caseGTIN1(t), UnitsPerPack: units}}
}

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

type cartonView struct {
	LPN        string
	SSCC       string
	Parent     string
	Kind       asn.CartonKind
	Status     CartonStatus
	Sealed     bool
	ReceivedAt time.Time
	Contents   []CartonContent
}

func viewsOf(cs []Carton) []cartonView {
	out := make([]cartonView, 0, len(cs))
	for _, c := range cs {
		out = append(out, cartonView{c.LPN(), c.SSCC().String(), c.ParentSSCC().String(), c.Kind(), c.Status(), c.Sealed(), c.ReceivedAt(), c.Contents()})
	}
	return out
}

func manifestViews(t *testing.T) []cartonView {
	g1, g2 := eachGTIN1(t), eachGTIN2(t)
	a, b, m := ssccA(t).String(), ssccB(t).String(), ssccM(t).String()
	return []cartonView{
		{LPN: a, SSCC: a, Parent: pallet(t).String(), Kind: asn.CartonHomogeneousCase, Status: CartonExpected,
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20, Lot: "L2310", Expiry: "2027-10-31"}}},
		{LPN: b, SSCC: b, Kind: asn.CartonHomogeneousCase, Status: CartonExpected,
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 10}}},
		{LPN: m, SSCC: m, Kind: asn.CartonMixed, Status: CartonExpected,
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 10}, {LineNo: 2, SKU: "SKU-2", GTIN: g2, Qty: 5}}},
	}
}

func TestOpenCopiesOwnerAndManifest(t *testing.T) {
	r := openedCartons(t)
	if r.OwnerRef() != owner {
		t.Fatalf("owner = %q", r.OwnerRef())
	}
	if got, want := viewsOf(r.Cartons()), manifestViews(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("cartons = %+v\nwant     %+v", got, want)
	}
	flat := opened(t)
	if len(flat.Cartons()) != 0 || flat.OwnerRef() != "" {
		t.Fatalf("flat receipt has cartons %v owner %q", flat.Cartons(), flat.OwnerRef())
	}
}

func TestCartonsAreCopiedOut(t *testing.T) {
	r := openedCartons(t)
	r.Cartons()[0] = Carton{}
	r.Cartons()[0].Contents()[0] = CartonContent{}
	if r.Cartons()[0].LPN() == "" || r.Cartons()[0].Contents()[0].Qty != 20 {
		t.Fatal("mutating a returned copy changed the aggregate")
	}
}

func TestCartonLookupAndLineOfSKU(t *testing.T) {
	r := openedCartons(t)
	c, ok := r.Carton(ssccB(t))
	if !ok || c.SSCC() != ssccB(t) {
		t.Fatalf("Carton(B) = %+v, %v", c, ok)
	}
	if _, ok := r.Carton(ssccX(t)); ok {
		t.Fatal("an SSCC not on the manifest must not be found")
	}
	if _, ok := r.Carton(gs1.SSCC{}); ok {
		t.Fatal("the zero SSCC must not match a labelled carton")
	}
	for sku, want := range map[shared.SKU]int{"SKU-1": 1, "SKU-2": 2} {
		if got, ok := r.LineOfSKU(sku); !ok || got != want {
			t.Fatalf("LineOfSKU(%s) = %d, %v", sku, got, ok)
		}
	}
	if _, ok := r.LineOfSKU("SKU-9"); ok {
		t.Fatal("unknown sku found")
	}
}

func TestReceiveLineEventCarriesOwnerRef(t *testing.T) {
	r := openedCartons(t)
	events, err := r.ReceiveLine(1, 3, ConditionGood, t0)
	if err != nil {
		t.Fatal(err)
	}
	e := onlyEvent(t, events).(ReceiptLineReceived)
	if e.OwnerRef != owner || !e.PackGTIN.IsZero() || e.Cases != 0 || !e.SSCC.IsZero() {
		t.Fatalf("event = %+v", e)
	}
	flat := opened(t)
	events, _ = flat.ReceiveLine(1, 3, ConditionGood, t0)
	if events[0].(ReceiptLineReceived).OwnerRef != "" {
		t.Fatal("a flat receipt must not invent an owner")
	}
}

func scanOf(t *testing.T, s gs1.SSCC, units int64) CartonScan {
	return CartonScan{SSCC: s, Condition: ConditionGood, CasePacks: casePacks(t, units)}
}

func TestAcceptHomogeneousCarton(t *testing.T) {
	r := openedCartons(t)
	res, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(5))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{ID: rid, Asn: asnNum, At: at(5)}
	content := CartonContent{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t), Qty: 20, Lot: "L2310", Expiry: "2027-10-31", Condition: ConditionGood}
	wantEvents := []Event{
		ReceiptLineReceived{Header: h, LineNo: 1, SKU: "SKU-1", Quantity: 20, Condition: ConditionGood, OwnerRef: owner,
			PackGTIN: caseGTIN1(t), Cases: 1, SSCC: ssccA(t)},
		ReceiptCartonReceived{Header: h, OwnerRef: owner, SSCC: ssccA(t), ParentSSCC: pallet(t), Sealed: true, LPN: ssccA(t).String(),
			Contents: []CartonContent{content}},
	}
	if !res.Accepted || len(res.Discrepancies) != 0 || !reflect.DeepEqual(res.Events, wantEvents) {
		t.Fatalf("result = %+v\nwant events %+v", res, wantEvents)
	}
	if r.Lines()[0].ReceivedGood() != 20 || r.Version() != 2 {
		t.Fatalf("line good = %d version = %d", r.Lines()[0].ReceivedGood(), r.Version())
	}
	wantLedger := cartonView{LPN: ssccA(t).String(), SSCC: ssccA(t).String(), Parent: pallet(t).String(), Kind: asn.CartonHomogeneousCase,
		Status: CartonAccepted, Sealed: true, ReceivedAt: at(5), Contents: []CartonContent{content}}
	if got := viewsOf(r.Cartons()); !reflect.DeepEqual(got[0], wantLedger) || got[1].Status != CartonExpected || got[2].Status != CartonExpected {
		t.Fatalf("ledger = %+v", got)
	}
	if got := viewsOf(res.Cartons); len(got) != 1 || !reflect.DeepEqual(got[0], wantLedger) {
		t.Fatalf("result cartons = %+v", got)
	}
}

func TestAcceptCartonWithoutCaseLevelOmitsPackFacts(t *testing.T) {
	r := openedCartons(t)
	res, err := r.AcceptCarton(CartonScan{SSCC: ssccA(t), Condition: ConditionGood}, at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
	line := res.Events[0].(ReceiptLineReceived)
	if !line.PackGTIN.IsZero() || line.Cases != 0 || line.SSCC != ssccA(t) {
		t.Fatalf("line event = %+v", line)
	}
}

func TestAcceptMixedCartonBooksEveryContentLine(t *testing.T) {
	r := openedCartons(t)
	// A case level for SKU-1 must not turn a mixed carton into a case pack.
	res, err := r.AcceptCarton(scanOf(t, ssccM(t), 99), at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
	h := Header{ID: rid, Asn: asnNum, At: at(1)}
	g1, g2 := eachGTIN1(t), eachGTIN2(t)
	wantEvents := []Event{
		ReceiptLineReceived{Header: h, LineNo: 1, SKU: "SKU-1", Quantity: 10, Condition: ConditionGood, OwnerRef: owner, SSCC: ssccM(t)},
		ReceiptLineReceived{Header: h, LineNo: 2, SKU: "SKU-2", Quantity: 5, Condition: ConditionGood, OwnerRef: owner, SSCC: ssccM(t)},
		ReceiptCartonReceived{Header: h, OwnerRef: owner, SSCC: ssccM(t), Sealed: true, LPN: ssccM(t).String(), Contents: []CartonContent{
			{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 10, Condition: ConditionGood},
			{LineNo: 2, SKU: "SKU-2", GTIN: g2, Qty: 5, Condition: ConditionGood},
		}},
	}
	if !reflect.DeepEqual(res.Events, wantEvents) {
		t.Fatalf("events = %+v\nwant     %+v", res.Events, wantEvents)
	}
	if r.Lines()[0].ReceivedGood() != 10 || r.Lines()[1].ReceivedGood() != 5 {
		t.Fatalf("lines = %+v", r.Lines())
	}
}

func TestAcceptDamagedCarton(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccB(t), 10)
	in.Condition = ConditionDamaged
	res, err := r.AcceptCarton(in, at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
	if r.Lines()[0].ReceivedDamaged() != 10 || r.Lines()[0].ReceivedGood() != 0 {
		t.Fatalf("line = %+v", r.Lines()[0])
	}
	if res.Events[0].(ReceiptLineReceived).Condition != ConditionDamaged || res.Events[1].(ReceiptCartonReceived).Contents[0].Condition != ConditionDamaged {
		t.Fatalf("events = %+v", res.Events)
	}
}

func TestAcceptCartonRefusals(t *testing.T) {
	closed := openedCartons(t)
	if _, err := closed.Close(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.AcceptCarton(scanOf(t, ssccA(t), 20), t0); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("closed: %v", err)
	}
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.Condition = "Broken"
	if _, err := r.AcceptCarton(in, t0); !errors.Is(err, ErrInvalidCondition) {
		t.Fatalf("condition: %v", err)
	}
	if r.Version() != 1 {
		t.Fatalf("a refused scan must not change the receipt (version %d)", r.Version())
	}
}

func TestAcceptCartonOverflowChangesNothing(t *testing.T) {
	r := openedCartons(t)
	receive(t, r, 1, maxQty-10, ConditionGood)
	version := r.Version()
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), t0); !errors.Is(err, shared.ErrInvalidQuantity) {
		t.Fatalf("overflow: %v", err)
	}
	if r.Version() != version || r.Lines()[0].ReceivedGood() != maxQty-10 {
		t.Fatalf("receipt changed: version %d good %d", r.Version(), r.Lines()[0].ReceivedGood())
	}
	if c, _ := r.Carton(ssccA(t)); c.Status() != CartonExpected {
		t.Fatal("the carton must stay expected")
	}
}

func TestAcceptCartonOverflowOnASecondContentChangesNothing(t *testing.T) {
	r := openedCartons(t)
	receive(t, r, 2, maxQty-3, ConditionGood)
	if _, err := r.AcceptCarton(scanOf(t, ssccM(t), 20), t0); !errors.Is(err, shared.ErrInvalidQuantity) {
		t.Fatalf("overflow: %v", err)
	}
	if r.Lines()[0].ReceivedGood() != 0 {
		t.Fatal("the first content must not stay booked when the second does not fit")
	}
}

func discrepancyKinds(res ScanResult) []Kind {
	kinds := make([]Kind, 0, len(res.Discrepancies))
	for _, d := range res.Discrepancies {
		kinds = append(kinds, d.Kind)
	}
	return kinds
}

// assertRejected checks the outcome of a scan that was refused with findings:
// nothing booked, one CartonDiscrepancyRaised per finding, version bumped.
func assertRejected(t *testing.T, r *Receipt, res ScanResult, versionBefore int64, kinds ...Kind) {
	t.Helper()
	got := discrepancyKinds(res)
	if res.Accepted || len(got) != len(kinds) || len(res.Events) != len(kinds) {
		t.Fatalf("result = %+v, want kinds %v", res, kinds)
	}
	for i, k := range kinds {
		if got[i] != k {
			t.Fatalf("kinds = %v, want %v", got, kinds)
		}
		e, ok := res.Events[i].(CartonDiscrepancyRaised)
		if !ok || e.Kind != k || e.CartonDiscrepancy != res.Discrepancies[i] {
			t.Fatalf("event %d = %+v", i, res.Events[i])
		}
	}
	for _, l := range r.Lines() {
		if l.Received() != 0 {
			t.Fatalf("a refused scan booked %+v", l)
		}
	}
	if r.Version() != versionBefore+1 {
		t.Fatalf("version = %d, want %d", r.Version(), versionBefore+1)
	}
}

func TestAcceptUnexpectedCarton(t *testing.T) {
	r := openedCartons(t)
	res, err := r.AcceptCarton(scanOf(t, ssccX(t), 20), at(2))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindUnexpectedCarton)
	d := res.Discrepancies[0]
	if d.SSCC != ssccX(t) || d.Expected != "" || d.Actual != "" {
		t.Fatalf("discrepancy = %+v", d)
	}
	assertHeader(t, res.Events[0], "CartonDiscrepancyRaised", at(2))
	if len(r.Cartons()) != 3 {
		t.Fatal("an unexpected carton must not enter the ledger")
	}
}

func TestAcceptDuplicateCartonOnTheReceipt(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(1)); err != nil {
		t.Fatal(err)
	}
	version := r.Version()
	res, err := r.AcceptCarton(scanOf(t, ssccA(t), 20), at(2))
	if err != nil || res.Accepted || len(res.Events) != 1 {
		t.Fatalf("second scan = %+v, %v", res, err)
	}
	if k := discrepancyKinds(res); len(k) != 1 || k[0] != KindDuplicateSscc || res.Discrepancies[0].SSCC != ssccA(t) {
		t.Fatalf("discrepancies = %+v", res.Discrepancies)
	}
	if len(res.Cartons) != 1 || res.Cartons[0].Status() != CartonAccepted || res.Cartons[0].SSCC() != ssccA(t) {
		t.Fatalf("the answer must carry the carton already received: %+v", res.Cartons)
	}
	if r.Lines()[0].ReceivedGood() != 20 || r.Version() != version+1 {
		t.Fatalf("a duplicate must book nothing: good %d version %d", r.Lines()[0].ReceivedGood(), r.Version())
	}
}

func TestAcceptCartonReceivedElsewhere(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.ReceivedElsewhere = true
	res, err := r.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindDuplicateSscc)
	if len(res.Cartons) != 0 {
		t.Fatalf("a carton received on another receipt is not ours to return: %+v", res.Cartons)
	}
	// Not on this manifest and already received elsewhere is a duplicate, not unexpected.
	r2 := openedCartons(t)
	in = scanOf(t, ssccX(t), 20)
	in.ReceivedElsewhere = true
	res, err = r2.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r2, res, 1, KindDuplicateSscc)
}

func TestAcceptCaseQtyMismatchAgainstTheCaseLevel(t *testing.T) {
	r := openedCartons(t)
	res, err := r.AcceptCarton(scanOf(t, ssccA(t), 12), at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindCaseQtyMismatch)
	if d := res.Discrepancies[0]; d.SSCC != ssccA(t) || d.Expected != "12" || d.Actual != "20" {
		t.Fatalf("discrepancy = %+v", d)
	}
	if c, _ := r.Carton(ssccA(t)); c.Status() != CartonExpected {
		t.Fatal("a mismatching carton must stay expected")
	}
}

func TestAcceptCaseQtyMismatchAgainstTheLabelCount(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.LabelCount = 18
	res, err := r.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindCaseQtyMismatch)
	if d := res.Discrepancies[0]; d.Expected != "20" || d.Actual != "18" {
		t.Fatalf("discrepancy = %+v", d)
	}
	in.LabelCount = 20
	res, err = r.AcceptCarton(in, at(2))
	if err != nil || !res.Accepted {
		t.Fatalf("a matching label count must be accepted: %+v, %v", res, err)
	}
}

func TestLabelCountIsIgnoredForAMixedCarton(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccM(t), 20)
	in.LabelCount = 3
	if res, err := r.AcceptCarton(in, at(1)); err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
}

func TestAcceptCartonWithAnUnresolvedLabelGTIN(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.LabelContentGTIN = eachGTIN2(t)
	res, err := r.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindUnknownGtin)
	if d := res.Discrepancies[0]; d.SSCC != ssccA(t) || d.Actual != eachGTIN2(t).String() || d.Expected != "" {
		t.Fatalf("discrepancy = %+v", d)
	}
}

func TestAcceptHomogeneousCartonWhoseLabelNamesAnotherSKU(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 20)
	in.LabelContentGTIN, in.LabelContentSKU = eachGTIN2(t), "SKU-2"
	res, err := r.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindMixedCaseDeclaredAsCasePack)
	if d := res.Discrepancies[0]; d.SSCC != ssccA(t) || d.Expected != "SKU-1" || d.Actual != "SKU-2" {
		t.Fatalf("discrepancy = %+v", d)
	}
	in.LabelContentGTIN, in.LabelContentSKU = eachGTIN1(t), "SKU-1"
	if res, err := r.AcceptCarton(in, at(2)); err != nil || !res.Accepted {
		t.Fatalf("a label that agrees with the manifest must be accepted: %+v, %v", res, err)
	}
}

func TestMixedCartonLabelIsNotCheckedForHomogeneity(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccM(t), 20)
	in.LabelContentGTIN, in.LabelContentSKU = eachGTIN2(t), "SKU-2"
	if res, err := r.AcceptCarton(in, at(1)); err != nil || !res.Accepted {
		t.Fatalf("AcceptCarton = %+v, %v", res, err)
	}
}

func TestSeveralFindingsAreAllRaised(t *testing.T) {
	r := openedCartons(t)
	in := scanOf(t, ssccA(t), 12)
	in.LabelCount = 18
	in.LabelContentGTIN, in.LabelContentSKU = eachGTIN2(t), "SKU-2"
	res, err := r.AcceptCarton(in, at(1))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindMixedCaseDeclaredAsCasePack, KindCaseQtyMismatch, KindCaseQtyMismatch)
	if res.Discrepancies[1].Actual != "18" || res.Discrepancies[2].Expected != "12" {
		t.Fatalf("discrepancies = %+v", res.Discrepancies)
	}
}

func TestRejectUnknownGtin(t *testing.T) {
	r := openedCartons(t)
	res, err := r.RejectUnknownGtin(eachGTIN2(t), at(3))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, r, res, 1, KindUnknownGtin)
	if d := res.Discrepancies[0]; !d.SSCC.IsZero() || d.Actual != eachGTIN2(t).String() || d.Expected != "" {
		t.Fatalf("discrepancy = %+v", d)
	}
	assertHeader(t, res.Events[0], "CartonDiscrepancyRaised", at(3))
	closed := openedCartons(t)
	if _, err := closed.Close(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.RejectUnknownGtin(eachGTIN2(t), t0); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func caseScan(t *testing.T, cases int64, lpns ...string) CaseScan {
	return CaseScan{
		SKU: "SKU-1", Pack: CasePack{GTIN: caseGTIN1(t), UnitsPerPack: 12}, ChildGTIN: eachGTIN1(t),
		Cases: cases, Condition: ConditionGood, Lot: "L9", Expiry: "2028-01-31", LPNs: lpns,
	}
}

func TestReceiveCasesOnAFlatReceipt(t *testing.T) {
	r := opened(t)
	res, err := r.ReceiveCases(caseScan(t, 2, "LPN-A", "LPN-B"), at(4))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{ID: rid, Asn: asnNum, At: at(4)}
	content := CartonContent{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t), Qty: 12, Lot: "L9", Expiry: "2028-01-31", Condition: ConditionGood}
	wantEvents := []Event{
		ReceiptLineReceived{Header: h, LineNo: 1, SKU: "SKU-1", Quantity: 24, Condition: ConditionGood, PackGTIN: caseGTIN1(t), Cases: 2},
		ReceiptCartonReceived{Header: h, Sealed: true, LPN: "LPN-A", Contents: []CartonContent{content}},
		ReceiptCartonReceived{Header: h, Sealed: true, LPN: "LPN-B", Contents: []CartonContent{content}},
	}
	if !res.Accepted || len(res.Discrepancies) != 0 || !reflect.DeepEqual(res.Events, wantEvents) {
		t.Fatalf("result = %+v\nwant events %+v", res, wantEvents)
	}
	wantLedger := func(lpn string) cartonView {
		return cartonView{LPN: lpn, Kind: asn.CartonHomogeneousCase, Status: CartonAccepted, Sealed: true, ReceivedAt: at(4), Contents: []CartonContent{content}}
	}
	want := []cartonView{wantLedger("LPN-A"), wantLedger("LPN-B")}
	if got := viewsOf(r.Cartons()); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(viewsOf(res.Cartons), want) {
		t.Fatalf("ledger = %+v\nwant     %+v", got, want)
	}
	if r.Lines()[0].ReceivedGood() != 24 || r.Version() != 2 {
		t.Fatalf("good %d version %d", r.Lines()[0].ReceivedGood(), r.Version())
	}
}

func TestReceiveCasesOnACartonAsnRaisesUnlabelledCarton(t *testing.T) {
	r := openedCartons(t)
	res, err := r.ReceiveCases(caseScan(t, 2, "LPN-A", "LPN-B"), at(4))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || len(res.Events) != 5 || len(res.Discrepancies) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for i, lpn := range []string{"LPN-A", "LPN-B"} {
		d := res.Discrepancies[i]
		if d.Kind != KindUnlabelledCarton || !d.SSCC.IsZero() || d.Actual != lpn || d.Expected != "" {
			t.Fatalf("discrepancy %d = %+v", i, d)
		}
		e, ok := res.Events[3+i].(CartonDiscrepancyRaised)
		if !ok || e.CartonDiscrepancy != d {
			t.Fatalf("event %d = %+v", 3+i, res.Events[3+i])
		}
	}
	if len(r.Cartons()) != 5 {
		t.Fatalf("ledger = %d entries", len(r.Cartons()))
	}
	if res.Events[0].(ReceiptLineReceived).OwnerRef != owner {
		t.Fatal("the owner must travel on the line event")
	}
	if res.Events[1].(ReceiptCartonReceived).OwnerRef != owner {
		t.Fatal("the owner must travel on the carton event")
	}
}

func TestReceiveCasesDamagedWithoutLotOrExpiry(t *testing.T) {
	r := opened(t)
	in := caseScan(t, 1, "LPN-A")
	in.Condition, in.Lot, in.Expiry = ConditionDamaged, "", ""
	res, err := r.ReceiveCases(in, at(1))
	if err != nil || !res.Accepted {
		t.Fatalf("ReceiveCases = %+v, %v", res, err)
	}
	if r.Lines()[0].ReceivedDamaged() != 12 || res.Events[1].(ReceiptCartonReceived).Contents[0].Condition != ConditionDamaged {
		t.Fatalf("damaged not booked: %+v", r.Lines()[0])
	}
}

func TestReceiveCasesBounds(t *testing.T) {
	lpns := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "LPN-" + strings.Repeat("x", i%3) + string(rune('A'+i%26))
		}
		return out
	}
	r := opened(t)
	if res, err := r.ReceiveCases(caseScan(t, MaxCasesPerScan, lpns(MaxCasesPerScan)...), t0); err != nil || len(res.Events) != 1+MaxCasesPerScan {
		t.Fatalf("1000 cases: %v", err)
	}
	for _, n := range []int64{0, -1, MaxCasesPerScan + 1} {
		r := opened(t)
		if _, err := r.ReceiveCases(caseScan(t, n), t0); !errors.Is(err, ErrInvalidCases) {
			t.Fatalf("cases %d: %v", n, err)
		}
	}
}

func TestReceiveCasesRefusals(t *testing.T) {
	r := opened(t)
	in := caseScan(t, 1, "LPN-A")
	in.SKU = "SKU-9"
	if _, err := r.ReceiveCases(in, t0); !errors.Is(err, ErrLineNotOnAsn) {
		t.Fatalf("sku not on the asn: %v", err)
	}
	if _, err := r.ReceiveCases(caseScan(t, 2, "LPN-A"), t0); !errors.Is(err, ErrScanMismatch) {
		t.Fatalf("lpn count: %v", err)
	}
	in = caseScan(t, 1, "LPN-A")
	in.Condition = "Broken"
	if _, err := r.ReceiveCases(in, t0); !errors.Is(err, ErrInvalidCondition) {
		t.Fatalf("condition: %v", err)
	}
	closed := opened(t)
	if _, err := closed.Close(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.ReceiveCases(caseScan(t, 1, "LPN-A"), t0); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("closed: %v", err)
	}
	if r.Version() != 1 || len(r.Cartons()) != 0 {
		t.Fatal("refused scans must not change the receipt")
	}
}

func TestReceiveCasesOverflowChangesNothing(t *testing.T) {
	r := opened(t)
	receive(t, r, 1, maxQty-11, ConditionGood)
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-A"), t0); !errors.Is(err, shared.ErrInvalidQuantity) {
		t.Fatalf("overflow: %v", err)
	}
	if r.Lines()[0].ReceivedGood() != maxQty-11 || len(r.Cartons()) != 0 {
		t.Fatal("the receipt changed")
	}
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-B"), t0); err == nil {
		t.Fatal("11 left, 12 wanted")
	}
	r2 := opened(t)
	in := caseScan(t, 1, "LPN-A")
	in.Pack.UnitsPerPack = 0
	if _, err := r2.ReceiveCases(in, t0); !errors.Is(err, shared.ErrInvalidQuantity) {
		t.Fatalf("zero units per pack: %v", err)
	}
}

func TestCloseRaisesMissingCartons(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AcceptCarton(scanOf(t, ssccB(t), 10), at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReceiveCases(caseScan(t, 1, "LPN-Z"), at(2)); err != nil {
		t.Fatal(err)
	}
	events, err := r.Close(at(9))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{ID: rid, Asn: asnNum, At: at(9)}
	want := []Event{
		CartonDiscrepancyRaised{Header: h, CartonDiscrepancy: CartonDiscrepancy{Kind: KindMissingCarton, Expected: ssccA(t).String()}},
		CartonDiscrepancyRaised{Header: h, CartonDiscrepancy: CartonDiscrepancy{Kind: KindMissingCarton, Expected: ssccM(t).String()}},
		ReceiptClosed{Header: h, Discrepancies: r.Discrepancies()},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v\nwant   %+v", events, want)
	}
	assertOnlyLineKinds(t, r.Discrepancies())
}

// assertOnlyLineKinds: ReceiptClosed keeps its three v1 kinds.
func assertOnlyLineKinds(t *testing.T, ds []Discrepancy) {
	t.Helper()
	if len(ds) == 0 {
		t.Fatal("expected line discrepancies on a partly received receipt")
	}
	for _, d := range ds {
		switch d.Kind {
		case KindShort, KindOver, KindDamaged:
		default:
			t.Fatalf("ReceiptClosed must keep its three v1 kinds, got %s", d.Kind)
		}
	}
}

func TestCloseWithEveryCartonAcceptedRaisesNoMissing(t *testing.T) {
	r := openedCartons(t)
	for _, s := range []gs1.SSCC{ssccA(t), ssccB(t), ssccM(t)} {
		if res, err := r.AcceptCarton(scanOf(t, s, 20), at(1)); err != nil || (!res.Accepted && s != ssccB(t)) {
			t.Fatalf("accept %s: %+v %v", s, res, err)
		}
	}
	// B has 10 against a case level of 20, so it was refused: it is the only missing one.
	events, _ := r.Close(at(2))
	if len(events) != 2 || events[0].(CartonDiscrepancyRaised).Expected != ssccB(t).String() {
		t.Fatalf("events = %+v", events)
	}
}

func TestCloseOfAFlatReceiptIsUnchanged(t *testing.T) {
	r := opened(t)
	events, err := r.Close(t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := onlyEvent(t, events).(ReceiptClosed); !ok {
		t.Fatalf("event = %T", events[0])
	}
}

func persistedWithCartons(t *testing.T) Persisted {
	t.Helper()
	return Persisted{
		ID: rid, AsnNumber: asnNum, OwnerRef: owner, State: StateOpen, OpenedAt: t0, Version: 4,
		Lines: []LineState{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40, ReceivedGood: 32}},
		Cartons: []CartonState{
			{
				LPN: ssccA(t).String(), SSCC: ssccA(t).String(), ParentSSCC: pallet(t).String(), Kind: "HOMOGENEOUS_CASE", Status: CartonExpected,
				Contents: []CartonContentState{{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 20, Lot: "L1", Expiry: "2027-10-31"}},
			},
			{
				LPN: ssccB(t).String(), SSCC: ssccB(t).String(), Kind: "HOMOGENEOUS_CASE", Status: CartonAccepted, Sealed: true, ReceivedAt: at(3),
				Contents: []CartonContentState{{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 20, Condition: "Good"}},
			},
			{
				LPN: "LPN-ABC", Kind: "HOMOGENEOUS_CASE", Status: CartonAccepted, Sealed: true, ReceivedAt: at(4),
				Contents: []CartonContentState{{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 12, Condition: "Damaged"}},
			},
		},
	}
}

func TestRehydrateKeepsTheCartonLedger(t *testing.T) {
	r, err := Rehydrate(persistedWithCartons(t))
	if err != nil {
		t.Fatal(err)
	}
	g1 := eachGTIN1(t)
	want := []cartonView{
		{LPN: ssccA(t).String(), SSCC: ssccA(t).String(), Parent: pallet(t).String(), Kind: asn.CartonHomogeneousCase, Status: CartonExpected,
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20, Lot: "L1", Expiry: "2027-10-31"}}},
		{LPN: ssccB(t).String(), SSCC: ssccB(t).String(), Kind: asn.CartonHomogeneousCase, Status: CartonAccepted, Sealed: true, ReceivedAt: at(3),
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20, Condition: ConditionGood}}},
		{LPN: "LPN-ABC", Kind: asn.CartonHomogeneousCase, Status: CartonAccepted, Sealed: true, ReceivedAt: at(4),
			Contents: []CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 12, Condition: ConditionDamaged}}},
	}
	if r.OwnerRef() != owner || !reflect.DeepEqual(viewsOf(r.Cartons()), want) {
		t.Fatalf("receipt = %+v\nwant %+v", viewsOf(r.Cartons()), want)
	}
	bad := Persisted{ID: rid, AsnNumber: asnNum, OwnerRef: "bad owner", State: StateOpen, OpenedAt: t0, Version: 1, Lines: persistedWithCartons(t).Lines}
	if _, err := Rehydrate(bad); !errors.Is(err, shared.ErrInvalidOwnerRef) {
		t.Fatalf("owner: %v", err)
	}
}

func TestRehydrateRejectsMalformedCartons(t *testing.T) {
	cases := map[string]func(p *Persisted){
		"empty lpn":                  func(p *Persisted) { p.Cartons[0].LPN = "" },
		"unknown status":             func(p *Persisted) { p.Cartons[0].Status = "Lost" },
		"unknown kind":               func(p *Persisted) { p.Cartons[0].Kind = "PALLET" },
		"bad sscc":                   func(p *Persisted) { p.Cartons[0].SSCC = "123" },
		"bad pallet sscc":            func(p *Persisted) { p.Cartons[0].ParentSSCC = "123" },
		"bad content sku":            func(p *Persisted) { p.Cartons[0].Contents[0].SKU = "bad sku" },
		"bad content gtin":           func(p *Persisted) { p.Cartons[0].Contents[0].GTIN = "123" },
		"content line not on lines":  func(p *Persisted) { p.Cartons[0].Contents[0].LineNo = 2 },
		"content line zero":          func(p *Persisted) { p.Cartons[0].Contents[0].LineNo = 0 },
		"content quantity zero":      func(p *Persisted) { p.Cartons[0].Contents[0].Qty = 0 },
		"accepted without condition": func(p *Persisted) { p.Cartons[1].Contents[0].Condition = "" },
		"accepted bad condition":     func(p *Persisted) { p.Cartons[1].Contents[0].Condition = "Broken" },
		"accepted without time":      func(p *Persisted) { p.Cartons[1].ReceivedAt = time.Time{} },
		"expected with a time":       func(p *Persisted) { p.Cartons[0].ReceivedAt = at(1) },
		"expected and sealed":        func(p *Persisted) { p.Cartons[0].Sealed = true },
		"expected with a condition":  func(p *Persisted) { p.Cartons[0].Contents[0].Condition = "Good" },
		"no contents":                func(p *Persisted) { p.Cartons[0].Contents = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := persistedWithCartons(t)
			mutate(&p)
			if _, err := Rehydrate(p); err == nil {
				t.Fatal("malformed carton accepted")
			} else if name != "bad content sku" && name != "content quantity zero" && name != "bad content gtin" && !errors.Is(err, ErrInvalidCarton) {
				t.Fatalf("err = %v, want ErrInvalidCarton", err)
			}
		})
	}
}
