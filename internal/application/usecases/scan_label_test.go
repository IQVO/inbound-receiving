package usecases_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const (
	siteGLN  = "4012345000009"
	otherGLN = "7612345000008"
)

func (w *scanWorld) withSites(t *testing.T, raw string) {
	t.Helper()
	sites, err := receipt.ParseSiteGLNs(raw)
	wantNoErr(t, err)
	w.uc.SiteGLNs = sites
}

// conflicting is a label with AI 01 and AI 02 together: the standard forbids it.
func (w *scanWorld) conflicting() string {
	return "]C101" + w.each1.String() + "02" + w.each1.String() + "3720"
}

func TestScanRefusesAndRecordsANonCompliantLabel(t *testing.T) {
	w := newScanWorld(t)
	versionBefore := w.receiptNow(t).Version()
	raw := w.conflicting()
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
	wantNoErr(t, err)
	if out.Result.Accepted || out.Kind != usecases.ScanEachGTIN {
		t.Fatalf("outcome = %+v kind %s", out.Result, out.Kind)
	}
	want := receipt.CartonDiscrepancy{Kind: receipt.KindLabelNonCompliant, Expected: "conflicting_ais", Actual: raw, SupplierRef: "ACME"}
	if len(out.Result.Discrepancies) != 1 || out.Result.Discrepancies[0] != want {
		t.Fatalf("findings = %+v, want %+v", out.Result.Discrepancies, want)
	}
	if got := w.newTypes(); !reflect.DeepEqual(got, []string{"receipt.CartonDiscrepancyRaised"}) {
		t.Fatalf("outbox = %v", got)
	}
	r := w.receiptNow(t)
	if r.Version() != versionBefore+1 || r.Lines()[0].ReceivedGood() != 0 {
		t.Fatalf("version %d good %d", r.Version(), r.Lines()[0].ReceivedGood())
	}
	if f := r.CartonDiscrepancies(); len(f) != 1 || f[0].CartonDiscrepancy != want {
		t.Fatalf("kept findings = %+v", f)
	}
	if len(w.metrics.calls) != 0 {
		t.Fatalf("a refused label must record no units: %v", w.metrics.calls)
	}
}

func TestScanRecordsOneFindingPerRefusedScanWithItsReason(t *testing.T) {
	w := newScanWorld(t)
	tests := []struct {
		name   string
		raw    string
		reason string
		kind   usecases.ScanKind
	}{
		{"a label that starts with the SSCC is an SSCC scan", "]C100" + w.ssccA.String() + "17261340", "bad_date", usecases.ScanSSCC},
		{"a bad check digit", "]C100" + w.ssccA.String()[:17] + "1", "bad_check_digit", usecases.ScanSSCC},
		{"02 without 00", "]C102" + w.each1.String() + "3720", "missing_ai", usecases.ScanEachGTIN},
		{"an unknown AI", "]C199ABC", "unknown_ai", usecases.ScanEachGTIN},
		{"the same AI twice", "]C101" + w.each1.String() + "01" + w.each1.String(), "duplicate_ai", usecases.ScanEachGTIN},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := w.scan(usecases.ReceiveScanCommand{Scan: tt.raw})
			wantNoErr(t, err)
			if out.Kind != tt.kind || out.Result.Accepted || len(out.Result.Discrepancies) != 1 {
				t.Fatalf("outcome = %+v kind %s", out.Result, out.Kind)
			}
			if d := out.Result.Discrepancies[0]; d.Kind != receipt.KindLabelNonCompliant || d.Expected != tt.reason || d.Actual != tt.raw || d.SupplierRef != "ACME" {
				t.Fatalf("finding = %+v", d)
			}
			if got := len(w.receiptNow(t).CartonDiscrepancies()); got != i+1 {
				t.Fatalf("the receipt keeps %d findings, want one per refused scan (%d)", got, i+1)
			}
		})
	}
}

func TestScanOfTheSameBadLabelTwiceRecordsTwoFindings(t *testing.T) {
	w := newScanWorld(t)
	for i := 0; i < 2; i++ {
		if _, err := w.scan(usecases.ReceiveScanCommand{Scan: w.conflicting()}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(w.receiptNow(t).CartonDiscrepancies()); got != 2 {
		t.Fatalf("findings = %d, want 2", got)
	}
}

func TestScanTheRefusedLabelKeepsTheKindTheScreenExpected(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C199ABC", ExpectedKind: "CASE_GTIN"})
	wantNoErr(t, err)
	if out.Kind != usecases.ScanCaseGTIN || len(out.Result.Discrepancies) != 1 {
		t.Fatalf("outcome = %+v kind %s", out.Result, out.Kind)
	}
}

func TestScanTheGSSeparatorIsShownInTheFinding(t *testing.T) {
	w := newScanWorld(t)
	raw := "]C110AB\x1d99XYZ"
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
	wantNoErr(t, err)
	if d := out.Result.Discrepancies[0]; d.Actual != "]C110AB<GS>99XYZ" || d.Expected != "unknown_ai" {
		t.Fatalf("finding = %+v", d)
	}
}

func TestScanJunkIsStillAClientError(t *testing.T) {
	long := "]C1" + strings.Repeat("A", 510) // 513 characters
	for name, raw := range map[string]string{
		"junk":                       "hello",
		"the identifier alone":       "]C1",
		"EAN data that is no GTIN":   "]E0" + "123",
		"an unsupported symbology":   "]Q1" + "ABC",
		"a space in the element":     "]C100 123",
		"a control character":        "]C100\x01123",
		"more than 512 characters":   long,
		"a violation without ]C1":    "99ABC",
		"ITF-14 with 13 digits only": "]I1" + "1234567890123",
	} {
		t.Run(name, func(t *testing.T) {
			w := newScanWorld(t)
			before := len(w.outbox.msgs)
			_, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
			wantErr(t, err, usecases.ErrInvalidScan)
			if len(w.outbox.msgs) != before || len(w.receiptNow(t).CartonDiscrepancies()) != 0 {
				t.Fatal("junk must record nothing")
			}
		})
	}
}

func TestScanOfALabelOnAnUnknownOrClosedReceipt(t *testing.T) {
	w := newScanWorld(t)
	_, err := w.uc.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: "rcpt-00000000-0000-0000-0000-000000000000", Scan: w.conflicting()})
	wantErr(t, err, repository.ErrReceiptNotFound)

	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	before := len(w.outbox.msgs)
	_, err = w.scan(usecases.ReceiveScanCommand{Scan: w.conflicting()})
	wantErr(t, err, receipt.ErrReceiptClosed)
	if len(w.outbox.msgs) != before {
		t.Fatal("a refused label on a closed receipt must record nothing")
	}
}

func TestScanOfARefusedLabelHonoursIfMatch(t *testing.T) {
	w := newScanWorld(t)
	v := w.receiptNow(t).Version()
	_, err := w.scan(usecases.ReceiveScanCommand{Scan: w.conflicting(), ExpectedVersion: v + 5})
	wantErr(t, err, usecases.ErrVersionMismatch)
	if _, err := w.scan(usecases.ReceiveScanCommand{Scan: w.conflicting(), ExpectedVersion: v}); err != nil {
		t.Fatal(err)
	}
}

func TestScanAcceptsALabelWithTheNewAIsAndKeepsTheirData(t *testing.T) {
	w := newScanWorld(t)
	raw := "]C100" + w.ssccA.String() + "11260901" + "13260902" + "15270300" + "17270630" + "410" + siteGLN + "413" + otherGLN + "21SER1\x1d" + "400PO77"
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
	wantNoErr(t, err)
	if !out.Result.Accepted {
		t.Fatalf("outcome = %+v", out.Result)
	}
	want := receipt.LabelData{
		BestBefore: "2027-03-31", Expiry: "2027-06-30", ProductionDate: "2026-09-01", PackagingDate: "2026-09-02",
		Serial: "SER1", PurchaseOrder: "PO77", ShipToGLN: siteGLN, ShipForGLN: otherGLN,
	}
	ev := out.Result.Events[1].(receipt.ReceiptCartonReceived)
	if ev.Label != want || ev.SSCCSource != receipt.SsccSupplier {
		t.Fatalf("event label = %+v source %q, want %+v", ev.Label, ev.SSCCSource, want)
	}
	if c, _ := w.receiptNow(t).Carton(w.ssccA); c.Label() != want {
		t.Fatalf("stored label = %+v", c.Label())
	}
}

func TestScanRequestExpiryWinsOverTheLabelsAndIsKept(t *testing.T) {
	w := newScanWorld(t)
	raw := "]C100" + w.ssccA.String() + "17270630"
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw, Expiry: "2027-01-00"})
	wantNoErr(t, err)
	if got := out.Result.Events[1].(receipt.ReceiptCartonReceived).Label.Expiry; got != "2027-01-00" {
		t.Fatalf("expiry = %q, want the request's", got)
	}
}

func TestScanReadsDatesWithTheGS1CenturyWindowOfTheClock(t *testing.T) {
	// The fake clock reads 2026: YY 76 is 2076, YY 77 is 1977 (51 above the clock's YY).
	for yymmdd, want := range map[string]string{"760131": "2076-01-31", "770131": "1977-01-31", "000131": "2000-01-31"} {
		w := newScanWorld(t)
		out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "15" + yymmdd})
		wantNoErr(t, err)
		if got := out.Result.Events[1].(receipt.ReceiptCartonReceived).Label.BestBefore; got != want {
			t.Errorf("(15)%s = %q, want %q", yymmdd, got, want)
		}
	}
}

func TestScanRefusesALabelForAnotherDestination(t *testing.T) {
	w := newScanWorld(t)
	w.withSites(t, siteGLN)
	raw := "]C100" + w.ssccA.String() + "410" + otherGLN
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: raw})
	wantNoErr(t, err)
	want := receipt.CartonDiscrepancy{Kind: receipt.KindWrongDestination, SSCC: w.ssccA, Actual: otherGLN, SupplierRef: "ACME"}
	if out.Result.Accepted || out.Kind != usecases.ScanSSCC || len(out.Result.Discrepancies) != 1 || out.Result.Discrepancies[0] != want {
		t.Fatalf("outcome = %+v kind %s, want %+v", out.Result, out.Kind, want)
	}
	if got := w.newTypes(); !reflect.DeepEqual(got, []string{"receipt.CartonDiscrepancyRaised"}) {
		t.Fatalf("outbox = %v", got)
	}
	r := w.receiptNow(t)
	if c, _ := r.Carton(w.ssccA); c.Status() != receipt.CartonExpected || r.Lines()[0].ReceivedGood() != 0 {
		t.Fatal("a carton for another destination must stay unreceived")
	}
}

func TestScanDestinationRuleBothSidesOfTheList(t *testing.T) {
	tests := []struct {
		name     string
		sites    string
		label    string
		accepted bool
	}{
		{"the GLN is the site", siteGLN, "410" + siteGLN, true},
		{"the GLN is the second site", siteGLN + "," + otherGLN, "410" + otherGLN, true},
		{"the GLN is another site", siteGLN, "410" + otherGLN, false},
		{"no AI 410 on the label", siteGLN, "", true},
		{"the rule is off", "", "410" + otherGLN, true},
		{"the rule is off and the label has no AI 410", "", "", true},
		{"AI 413 is never enforced", siteGLN, "413" + otherGLN, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newScanWorld(t)
			w.withSites(t, tt.sites)
			out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + tt.label})
			wantNoErr(t, err)
			if out.Result.Accepted != tt.accepted {
				t.Fatalf("accepted = %v, want %v (%+v)", out.Result.Accepted, tt.accepted, out.Result.Discrepancies)
			}
			if !tt.accepted && out.Result.Discrepancies[0].Kind != receipt.KindWrongDestination {
				t.Fatalf("finding = %+v", out.Result.Discrepancies)
			}
		})
	}
}

func TestScanWithTheRuleOffStoresTheShipToGLN(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "410" + otherGLN})
	wantNoErr(t, err)
	if got := out.Result.Events[1].(receipt.ReceiptCartonReceived).Label.ShipToGLN; !out.Result.Accepted || got != otherGLN {
		t.Fatalf("accepted %v ship-to %q", out.Result.Accepted, got)
	}
}

func TestScanAGTINLabelForAnotherDestinationIsRecordedWithoutAnSSCC(t *testing.T) {
	w := newScanWorld(t)
	w.withSites(t, siteGLN)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C101" + w.each1.String() + "410" + otherGLN})
	wantNoErr(t, err)
	d := out.Result.Discrepancies
	if out.Result.Accepted || out.Kind != usecases.ScanEachGTIN || len(d) != 1 || d[0].Kind != receipt.KindWrongDestination || !d[0].SSCC.IsZero() {
		t.Fatalf("outcome = %+v kind %s", out.Result, out.Kind)
	}
}

func TestScanANonCompliantLabelIsRefusedBeforeTheDestinationIsChecked(t *testing.T) {
	w := newScanWorld(t)
	w.withSites(t, siteGLN)
	out, err := w.scan(usecases.ReceiveScanCommand{Scan: "]C100" + w.ssccA.String() + "17261340" + "410" + otherGLN})
	wantNoErr(t, err)
	if d := out.Result.Discrepancies; len(d) != 1 || d[0].Kind != receipt.KindLabelNonCompliant {
		t.Fatalf("findings = %+v", d)
	}
}

func TestScanByKindAndIDIsNotSubjectToTheDestinationRule(t *testing.T) {
	w := newScanWorld(t)
	w.withSites(t, siteGLN)
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	if !out.Result.Accepted {
		t.Fatalf("outcome = %+v", out.Result)
	}
}
