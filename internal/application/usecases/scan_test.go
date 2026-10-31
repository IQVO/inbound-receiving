package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// Carton fixtures: SKU-1 x 30 is two homogeneous cases (20 + 10 is NOT a case
// pack of 20, see the mismatch test) so the ASN uses 20 + 20 = 40 and a mixed
// carton carries the 5 of SKU-2.
func mustSSCC(t *testing.T, payload string) gs1.SSCC {
	t.Helper()
	s, err := gs1.NewSSCC(payload)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustGTIN(t *testing.T, payload string) gs1.GTIN {
	t.Helper()
	g, err := gs1.NewGTIN(payload)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

var t0 = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

type scanWorld struct {
	*world
	pack     *fakePackaging
	uc       *usecases.ReceiveScan
	receipt  string
	each1    gs1.GTIN
	case1    gs1.GTIN
	each2    gs1.GTIN
	ssccA    gs1.SSCC
	ssccB    gs1.SSCC
	ssccM    gs1.SSCC
	ssccX    gs1.SSCC
	metrics  *fakeMetrics
	baseline int
}

type fakeMetrics struct{ calls []string }

func (m *fakeMetrics) LineReceived(_ context.Context, condition string, quantity int64) {
	m.calls = append(m.calls, condition+":"+itoa(quantity))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for ; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}

// newScanWorld registers a carton ASN (A and B are homogeneous cases of 20
// SKU-1, M is mixed with the 5 SKU-2), opens its receipt and declares an EACH
// and a CASE (of 20) level for SKU-1 and an EACH level for SKU-2.
func newScanWorld(t *testing.T) *scanWorld {
	t.Helper()
	w := &scanWorld{world: newWorld(), pack: newFakePackaging(), metrics: &fakeMetrics{}}
	w.each1, w.case1, w.each2 = mustGTIN(t, "629104150021"), mustGTIN(t, "2000123456785"), mustGTIN(t, "629104150038")
	w.ssccA, w.ssccB, w.ssccM, w.ssccX = mustSSCC(t, "00345678901234567"), mustSSCC(t, "00345678901234568"), mustSSCC(t, "00345678901234569"), mustSSCC(t, "00345678901234570")
	w.pack.add(level(w.each1, "SKU-1", shared.PackEach, gs1.GTIN{}, 1))
	w.pack.add(level(w.case1, "SKU-1", shared.PackCase, w.each1, 20))
	w.pack.add(level(w.each2, "SKU-2", shared.PackEach, gs1.GTIN{}, 1))
	w.uc = (&usecases.ReceiveScan{Writer: w.writer, Packaging: w.pack}).WithMetrics(w.metrics)
	w.registerCartonAsn(t, "ASN-C")
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-C"})
	wantNoErr(t, err)
	w.receipt, w.baseline = string(r.ID()), len(w.outbox.msgs)
	return w
}

func level(g gs1.GTIN, sku string, l shared.PackLevel, child gs1.GTIN, units int64) repository.PackLevel {
	p := repository.PackLevel{GTIN: g, SKU: shared.SKU(sku), Level: l, ChildGTIN: child, UnitsPerPack: units, EffectiveFrom: t0.Add(-time.Hour), Version: 1}
	if l != shared.PackEach {
		p.QtyOfChild = units
	}
	return p
}

func (w *scanWorld) registerCartonAsn(t *testing.T, number string) {
	t.Helper()
	g1, g2 := w.each1.String(), w.each2.String()
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", OwnerRef: "tenant-7f3a",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5}},
		Cartons: []asn.CartonInput{
			{SSCC: w.ssccA.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20}}},
			{SSCC: w.ssccB.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20}}},
			{SSCC: w.ssccM.String(), Kind: "MIXED", Contents: []asn.ContentInput{{LineNo: 2, SKU: "SKU-2", GTIN: g2, Qty: 5}}},
		},
	})
	wantNoErr(t, err)
}

func (w *scanWorld) scan(cmd usecases.ReceiveScanCommand) (usecases.ScanOutcome, error) {
	cmd.ReceiptID = w.receipt
	return w.uc.Handle(context.Background(), cmd)
}

func (w *scanWorld) newTypes() []string { return w.eventTypes()[w.baseline:] }

func (w *scanWorld) receiptNow(t *testing.T) *receipt.Receipt {
	t.Helper()
	r, err := w.receipts.Get(context.Background(), receipt.ID(w.receipt))
	wantNoErr(t, err)
	return r
}

func TestScanAcceptsACartonBySSCC(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	if out.Kind != usecases.ScanSSCC || !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("outcome = %+v", out)
	}
	if got, want := w.newTypes(), []string{"receipt.ReceiptLineReceived", "receipt.ReceiptCartonReceived"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox = %v, want %v", got, want)
	}
	line := out.Result.Events[0].(receipt.ReceiptLineReceived)
	if line.PackGTIN != w.case1 || line.Cases != 1 || line.SSCC != w.ssccA || line.OwnerRef != "tenant-7f3a" {
		t.Fatalf("line event = %+v", line)
	}
	if !reflect.DeepEqual(w.metrics.calls, []string{"good:20"}) {
		t.Fatalf("metrics = %v", w.metrics.calls)
	}
}

func TestScanParsesAGS1128CaseLabel(t *testing.T) {
	w := newScanWorld(t)
	raw := "]C100" + w.ssccA.String() + "02" + w.each1.String() + "3720"
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw, ExpectedKind: "SSCC"})
	wantNoErr(t, err)
	if !out.Result.Accepted || out.Kind != usecases.ScanSSCC {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestScanRefusesALabelCountThatDisagreesWithTheManifest(t *testing.T) {
	w := newScanWorld(t)
	raw := "]C100" + w.ssccA.String() + "02" + w.each1.String() + "3718"
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindCaseQtyMismatch)
	if !reflect.DeepEqual(w.newTypes(), []string{"receipt.CartonDiscrepancyRaised"}) {
		t.Fatalf("outbox = %v", w.newTypes())
	}
	if len(w.metrics.calls) != 0 {
		t.Fatalf("a refused scan must record no units: %v", w.metrics.calls)
	}
}

func TestScanRefusesALabelWhoseContentGTINIsUnknownOrAnotherSKU(t *testing.T) {
	w := newScanWorld(t)
	unknown := mustGTIN(t, "629104150045")
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "02" + unknown.String() + "3720"})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindUnknownGtin)
	out, err = w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "02" + w.each2.String() + "3720"})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindMixedCaseDeclaredAsCasePack)
}

func assertFinding(t *testing.T, out usecases.ScanOutcome, kind receipt.Kind) {
	t.Helper()
	if out.Result.Accepted || len(out.Result.Discrepancies) != 1 || out.Result.Discrepancies[0].Kind != kind {
		t.Fatalf("outcome = %+v, want one %s", out.Result, kind)
	}
}

func TestScanFindings(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccX.String()})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindUnexpectedCarton)

	if _, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()}); err != nil {
		t.Fatal(err)
	}
	out, err = w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindDuplicateSscc)
	if len(out.Result.Cartons) != 1 || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("a duplicate answers with the carton already received and books nothing: %+v", out)
	}
}

func TestScanRefusesACartonAlreadyReceivedOnAnotherReceipt(t *testing.T) {
	w := newScanWorld(t)
	other := mustSSCC(t, "00345678901234583")
	// Another receipt of the site already accepted `other`.
	w.registerOtherAsn(t, "ASN-D", other)
	otherReceipt, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-D"})
	wantNoErr(t, err)
	_, err = w.uc.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: string(otherReceipt.ID()), Kind: "SSCC", ID: other.String()})
	wantNoErr(t, err)
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: other.String()})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindDuplicateSscc)
	if len(out.Result.Cartons) != 0 {
		t.Fatalf("cartons = %+v", out.Result.Cartons)
	}
}

func (w *scanWorld) registerOtherAsn(t *testing.T, number string, sscc gs1.SSCC) {
	t.Helper()
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME",
		Lines:   []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: []asn.CartonInput{{SSCC: sscc.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: w.each1.String(), Qty: 20}}}},
	})
	wantNoErr(t, err)
}

func TestScanRefusesACartonWhoseQuantityIsNotTheCasePack(t *testing.T) {
	w := newScanWorld(t)
	w.pack.add(level(w.case1, "SKU-1", shared.PackCase, w.each1, 12)) // same GTIN would be refused by version; bump it
	cur := w.pack.levels[w.case1]
	cur.UnitsPerPack, cur.QtyOfChild = 12, 12
	w.pack.levels[w.case1] = cur
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	assertFinding(t, out, receipt.KindCaseQtyMismatch)
	if d := out.Result.Discrepancies[0]; d.Expected != "12" || d.Actual != "20" {
		t.Fatalf("discrepancy = %+v", d)
	}
}

func TestScanReceivesCasesByTheirCaseGTIN(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: w.case1.String(), Qty: 2, Lot: "L1", Expiry: "2027-10-31"})
	wantNoErr(t, err)
	if out.Kind != usecases.ScanCaseGTIN || !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 40 {
		t.Fatalf("outcome = %+v", out)
	}
	want := []string{
		"receipt.ReceiptLineReceived", "receipt.ReceiptCartonReceived", "receipt.ReceiptCartonReceived",
		"receipt.CartonDiscrepancyRaised", "receipt.CartonDiscrepancyRaised", // UnlabelledCarton x2 on a carton ASN
	}
	if got := w.newTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox = %v, want %v", got, want)
	}
	line := out.Result.Events[0].(receipt.ReceiptLineReceived)
	carton := out.Result.Events[1].(receipt.ReceiptCartonReceived)
	if line.Quantity != 40 || line.Cases != 2 || line.PackGTIN != w.case1 || !strings.HasPrefix(carton.LPN, "LPN-") ||
		carton.Contents[0].GTIN != w.each1 || carton.Contents[0].Qty != 20 || carton.Contents[0].Lot != "L1" || carton.Contents[0].Expiry != "2027-10-31" {
		t.Fatalf("events = %+v / %+v", line, carton)
	}
}

func TestScanTakesLotAndExpiryFromTheLabel(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C101" + w.case1.String() + "10LOT9\x1d17271031", ExpectedKind: "CASE_GTIN"})
	wantNoErr(t, err)
	c := out.Result.Events[1].(receipt.ReceiptCartonReceived).Contents[0]
	if c.Lot != "LOT9" || c.Expiry != "2027-10-31" || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("contents = %+v", c)
	}
}

func TestScanReceivesEachesByTheirEachGTIN(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]E0" + w.each1.String()[1:], Qty: 3})
	wantNoErr(t, err)
	if out.Kind != usecases.ScanEachGTIN || out.Receipt.Lines()[0].ReceivedGood() != 3 {
		t.Fatalf("outcome = %+v", out)
	}
	out, err = w.scan(usecases.ReceiveScanCommand{Kind: "EACH_GTIN", ID: w.each2.String()})
	wantNoErr(t, err)
	if out.Receipt.Lines()[1].ReceivedGood() != 1 {
		t.Fatalf("a missing quantity is one each: %+v", out.Receipt.Lines())
	}
	if got := w.newTypes(); !reflect.DeepEqual(got, []string{"receipt.ReceiptLineReceived", "receipt.ReceiptLineReceived"}) {
		t.Fatalf("outbox = %v", got)
	}
}

func TestScanRefusesAnUnknownGTINAsAFinding(t *testing.T) {
	w := newScanWorld(t)
	unknown := mustGTIN(t, "629104150045")
	for _, kind := range []string{"EACH_GTIN", "CASE_GTIN"} {
		out, err := w.scan(usecases.ReceiveScanCommand{Kind: kind, ID: unknown.String()})
		wantNoErr(t, err)
		assertFinding(t, out, receipt.KindUnknownGtin)
		if string(out.Kind) != kind {
			t.Fatalf("kind = %s, want %s", out.Kind, kind)
		}
	}
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]E0" + unknown.String()[1:]})
	wantNoErr(t, err)
	if out.Kind != usecases.ScanEachGTIN {
		t.Fatalf("an unexpected-kind unknown GTIN reports each: %s", out.Kind)
	}
}

func TestScanRefusesTheWrongKindWithoutChangingAnything(t *testing.T) {
	w := newScanWorld(t)
	cases := map[string]usecases.ReceiveScanCommand{
		"each EAN printed on a case":      {Kind: "CASE_GTIN", ID: w.each1.String()},
		"case GTIN where eaches expected": {Kind: "EACH_GTIN", ID: w.case1.String()},
		"gtin where a carton expected":    {Scan: "]E0" + w.each1.String()[1:], ExpectedKind: "SSCC"},
		"sscc where a gtin expected":      {Scan: "]C100" + w.ssccA.String(), ExpectedKind: "EACH_GTIN"},
	}
	for name, cmd := range cases {
		if _, err := w.scan(cmd); !errors.Is(err, usecases.ErrScanKindMismatch) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(w.newTypes()) != 0 || w.receiptNow(t).Version() != 1 {
		t.Fatalf("a wrong-kind scan changed something: %v", w.newTypes())
	}
}

func TestScanRefusesLevelsReceivingDoesNotImplement(t *testing.T) {
	w := newScanWorld(t)
	inner, pallet := mustGTIN(t, "3000123456784"), mustGTIN(t, "4000123456783")
	w.pack.add(level(inner, "SKU-1", shared.PackInner, w.each1, 5))
	w.pack.add(level(pallet, "SKU-1", shared.PackPallet, w.case1, 40))
	for _, g := range []gs1.GTIN{inner, pallet} {
		if _, err := w.scan(usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: g.String()}); !errors.Is(err, usecases.ErrUnsupportedPackLevel) {
			t.Errorf("%s: err = %v", g, err)
		}
	}
}

func TestScanRejectsMalformedRequests(t *testing.T) {
	w := newScanWorld(t)
	cases := map[string]struct {
		cmd  usecases.ReceiveScanCommand
		want error
	}{
		"unknown kind":         {usecases.ReceiveScanCommand{Kind: "PALLET", ID: "1"}, usecases.ErrInvalidScanKind},
		"no kind and no scan":  {usecases.ReceiveScanCommand{}, usecases.ErrInvalidScanKind},
		"bad expected kind":    {usecases.ReceiveScanCommand{Scan: "]E0" + w.each1.String()[1:], ExpectedKind: "BOX"}, usecases.ErrInvalidScanKind},
		"raw and structured":   {usecases.ReceiveScanCommand{Scan: "]E0" + w.each1.String()[1:], Kind: "EACH_GTIN"}, usecases.ErrInvalidScan},
		"raw and id":           {usecases.ReceiveScanCommand{Scan: "]E0" + w.each1.String()[1:], ID: "1"}, usecases.ErrInvalidScan},
		"junk raw scan":        {usecases.ReceiveScanCommand{Scan: "hello"}, usecases.ErrInvalidScan},
		"bad sscc check digit": {usecases.ReceiveScanCommand{Kind: "SSCC", ID: "003456789012345679"}, gs1.ErrInvalidSSCC},
		"bad gtin":             {usecases.ReceiveScanCommand{Kind: "EACH_GTIN", ID: "123"}, gs1.ErrInvalidGTIN},
		"bad condition":        {usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String(), Condition: "Broken"}, receipt.ErrInvalidCondition},
		"zero-cases overflow":  {usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: w.case1.String(), Qty: 1001}, receipt.ErrInvalidCases},
		"negative cases":       {usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: w.case1.String(), Qty: -1}, receipt.ErrInvalidCases},
		"negative eaches":      {usecases.ReceiveScanCommand{Kind: "EACH_GTIN", ID: w.each1.String(), Qty: -1}, shared.ErrInvalidQuantity},
		"stale version":        {usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String(), ExpectedVersion: 7}, usecases.ErrVersionMismatch},
	}
	for name, tc := range cases {
		if _, err := w.scan(tc.cmd); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := w.uc.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: "nope", Kind: "SSCC", ID: w.ssccA.String()}); !errors.Is(err, receipt.ErrInvalidID) {
		t.Errorf("receipt id: %v", err)
	}
	if _, err := w.uc.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: missingReceipt, Kind: "SSCC", ID: w.ssccA.String()}); !errors.Is(err, repository.ErrReceiptNotFound) {
		t.Errorf("unknown receipt: %v", err)
	}
	if len(w.newTypes()) != 0 {
		t.Fatalf("rejected requests wrote events: %v", w.newTypes())
	}
}

func TestScanOfASKUThatIsNotOnTheAsnIsAnError(t *testing.T) {
	w := newScanWorld(t)
	foreign := mustGTIN(t, "629104150052")
	w.pack.add(level(foreign, "SKU-9", shared.PackEach, gs1.GTIN{}, 1))
	if _, err := w.scan(usecases.ReceiveScanCommand{Kind: "EACH_GTIN", ID: foreign.String()}); !errors.Is(err, receipt.ErrLineNotOnAsn) {
		t.Fatalf("err = %v", err)
	}
}

func TestScanOnAClosedReceipt(t *testing.T) {
	w := newScanWorld(t)
	_, err := (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	if _, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()}); !errors.Is(err, receipt.ErrReceiptClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestScanPropagatesPortFailures(t *testing.T) {
	boom := errors.New("boom")
	sscc := func(w *scanWorld) usecases.ReceiveScanCommand {
		return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()}
	}
	each := func(w *scanWorld) usecases.ReceiveScanCommand {
		return usecases.ReceiveScanCommand{Kind: "EACH_GTIN", ID: w.each1.String()}
	}
	label := func(w *scanWorld) usecases.ReceiveScanCommand {
		return usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "02" + w.each1.String() + "3720"}
	}
	cases := map[string]struct {
		setup func(w *scanWorld)
		cmd   func(w *scanWorld) usecases.ReceiveScanCommand
	}{
		"gtin lookup":         {func(w *scanWorld) { w.pack.err = boom }, each},
		"active level lookup": {func(w *scanWorld) { w.pack.err = boom }, sscc},
		"label gtin lookup":   {func(w *scanWorld) { w.pack.err = boom }, label},
		"received ssccs":      {func(w *scanWorld) { w.receipts.ssccErr = boom }, sscc},
		"unit of work":        {func(w *scanWorld) { w.uow.err = boom }, sscc},
		"outbox":              {func(w *scanWorld) { w.outbox.err = boom }, sscc},
	}
	for name, tc := range cases {
		w := newScanWorld(t)
		tc.setup(w)
		if _, err := w.scan(tc.cmd(w)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMissingCartonsAreRaisedWhenTheReceiptCloses(t *testing.T) {
	w := newScanWorld(t)
	_, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	want := []string{
		"receipt.ReceiptLineReceived", "receipt.ReceiptCartonReceived",
		"receipt.CartonDiscrepancyRaised", "receipt.CartonDiscrepancyRaised", "receipt.ReceiptClosed", // B and M are missing
	}
	if got := w.newTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox = %v, want %v", got, want)
	}
}

func TestGetAsnCartonsReportsTheReceiveStatus(t *testing.T) {
	w := newScanWorld(t)
	q := &usecases.GetAsnCartons{Asns: w.asns, Receipts: w.receipts}
	status := func() map[string]string {
		v, err := q.Handle(context.Background(), "ASN-C")
		wantNoErr(t, err)
		out := map[string]string{}
		for _, it := range v.Items {
			out[it.Carton.SSCC().String()] = it.Status
		}
		return out
	}
	if got := status(); got[w.ssccA.String()] != "expected" || got[w.ssccM.String()] != "expected" {
		t.Fatalf("open receipt, nothing scanned: %v", got)
	}
	_, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	if got := status(); got[w.ssccA.String()] != "accepted" || got[w.ssccB.String()] != "expected" {
		t.Fatalf("after scanning A: %v", got)
	}
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	if got := status(); got[w.ssccA.String()] != "accepted" || got[w.ssccB.String()] != "missing" || got[w.ssccM.String()] != "missing" {
		t.Fatalf("after close: %v", got)
	}
}

func TestGetAsnCartonsWithoutAReceiptAndForAFlatAsn(t *testing.T) {
	w := newWorld()
	w.registerAsn("ASN-FLAT", "SKU-1")
	q := &usecases.GetAsnCartons{Asns: w.asns, Receipts: w.receipts}
	v, err := q.Handle(context.Background(), "ASN-FLAT")
	if err != nil || v.Items == nil || len(v.Items) != 0 || v.Asn.Number() != "ASN-FLAT" {
		t.Fatalf("flat = %+v, %v", v, err)
	}
	sw := newScanWorld(t)
	sw.registerOtherAsn(t, "ASN-NR", mustSSCC(t, "00345678901234622"))
	v, err = (&usecases.GetAsnCartons{Asns: sw.asns, Receipts: sw.receipts}).Handle(context.Background(), "ASN-NR")
	if err != nil || len(v.Items) != 1 || v.Items[0].Status != "expected" {
		t.Fatalf("no receipt = %+v, %v", v, err)
	}
	if _, err := q.Handle(context.Background(), "bad number"); !errors.Is(err, asn.ErrInvalidNumber) {
		t.Fatalf("number: %v", err)
	}
	if _, err := q.Handle(context.Background(), "ASN-404"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	failing := &usecases.GetAsnCartons{Asns: sw.asns, Receipts: &failingList{fakeReceipts: sw.receipts}}
	if _, err := failing.Handle(context.Background(), "ASN-NR"); err == nil {
		t.Fatal("a failing receipt lookup must fail the query")
	}
}

type failingList struct{ *fakeReceipts }

func (failingList) List(context.Context, repository.ReceiptFilter, receipt.ID, int) ([]*receipt.Receipt, error) {
	return nil, errors.New("list failed")
}

func TestRegisterAsnWithCartons(t *testing.T) {
	w := newScanWorld(t)
	a, err := w.asns.Get(context.Background(), "ASN-C")
	wantNoErr(t, err)
	if a.OwnerRef() != "tenant-7f3a" || len(a.Cartons()) != 3 {
		t.Fatalf("asn = %+v", a)
	}
	// The SSCCs of an ASN that is already registered are taken, pallets included.
	dup := usecases.RegisterAsnCommand{
		AsnNumber: "ASN-DUP", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: []asn.CartonInput{{SSCC: w.ssccA.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: w.each1.String(), Qty: 20}}}},
	}
	reg := &usecases.RegisterAsn{Writer: w.writer}
	if _, err := reg.Handle(context.Background(), dup); !errors.Is(err, asn.ErrDuplicateSscc) {
		t.Fatalf("duplicate carton sscc: %v", err)
	}
	pal := mustSSCC(t, "00345678909999999")
	dup.Cartons[0].SSCC, dup.Cartons[0].ParentSSCC = mustSSCC(t, "00345678901234598").String(), pal.String()
	if _, err := reg.Handle(context.Background(), dup); err != nil {
		t.Fatalf("a fresh carton on a fresh pallet: %v", err)
	}
	dup.AsnNumber, dup.Cartons[0].SSCC = "ASN-DUP2", mustSSCC(t, "00345678901234606").String()
	if _, err := reg.Handle(context.Background(), dup); !errors.Is(err, asn.ErrDuplicateSscc) {
		t.Fatalf("duplicate pallet sscc: %v", err)
	}
}

func TestACancelledAsnReleasesItsSSCCs(t *testing.T) {
	w := newScanWorld(t)
	w.registerOtherAsn(t, "ASN-E", mustSSCC(t, "00345678901234614"))
	_, err := (&usecases.CancelAsn{Writer: w.writer}).Handle(context.Background(), usecases.CancelAsnCommand{AsnNumber: "ASN-E"})
	wantNoErr(t, err)
	w.registerOtherAsn(t, "ASN-F", mustSSCC(t, "00345678901234614"))
}

func TestRegisterAsnSsccLookupFailureFailsTheRegistration(t *testing.T) {
	w := newScanWorld(t)
	w.asns.ssccErr = errors.New("boom")
	err := func() error {
		_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
			AsnNumber: "ASN-Z", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
			Cartons: []asn.CartonInput{{SSCC: w.ssccX.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: w.each1.String(), Qty: 20}}}},
		})
		return err
	}()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
}
