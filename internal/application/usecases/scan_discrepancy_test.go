package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// heavyReceipt registers ASN-L, one case of SKU-1 declared at 25 kg and 1300 mm,
// opens its receipt and points the world at it.
func (w *scanWorld) heavyReceipt(t *testing.T) {
	t.Helper()
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: "ASN-L", SupplierRef: "ACME",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: []asn.CartonInput{{
			SSCC: w.ssccX.String(), Kind: "HOMOGENEOUS_CASE", GrossWeightG: 25000,
			Dims:     &asn.DimsInput{LengthMm: 300, WidthMm: 1300, HeightMm: 200},
			Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: w.each1.String(), Qty: 20}},
		}},
	})
	wantNoErr(t, err)
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-L"})
	wantNoErr(t, err)
	w.receipt = string(r.ID())
}

func TestScanRaisesCartonOverLimitAndStillAccepts(t *testing.T) {
	w := newScanWorld(t)
	w.heavyReceipt(t)
	w.uc.Limits = receipt.CartonLimits{MaxGrossWeightG: 20000, MaxLongestSideMm: 1200}
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccX.String()})
	wantNoErr(t, err)
	if !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("an over-limit carton must be accepted: %+v", out.Result)
	}
	d := out.Result.Discrepancies
	if len(d) != 1 || d[0].Kind != receipt.KindCartonOverLimit ||
		d[0].Expected != "grossWeightG:20000,longestSideMm:1200" || d[0].Actual != "grossWeightG:25000,longestSideMm:1300" {
		t.Fatalf("findings = %+v", d)
	}
}

func TestScanChecksOnlyTheConfiguredLimit(t *testing.T) {
	tests := []struct {
		name   string
		limits receipt.CartonLimits
		want   string
	}{
		{"weight only", receipt.CartonLimits{MaxGrossWeightG: 20000}, "grossWeightG:25000"},
		{"side only", receipt.CartonLimits{MaxLongestSideMm: 1200}, "longestSideMm:1300"},
		{"no limits", receipt.CartonLimits{}, ""},
		{"limits not exceeded", receipt.CartonLimits{MaxGrossWeightG: 25000, MaxLongestSideMm: 1300}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newScanWorld(t)
			w.heavyReceipt(t)
			w.uc.Limits = tt.limits
			out, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccX.String()})
			wantNoErr(t, err)
			d := out.Result.Discrepancies
			if tt.want == "" {
				if len(d) != 0 {
					t.Fatalf("findings = %+v, want none", d)
				}
				return
			}
			if len(d) != 1 || d[0].Actual != tt.want {
				t.Fatalf("findings = %+v, want actual %q", d, tt.want)
			}
		})
	}
}

func TestScanCountsAMixedCarton(t *testing.T) {
	w := newScanWorld(t)
	out, err := w.scan(usecases.ReceiveScanCommand{
		Kind: "SSCC", ID: w.ssccM.String(),
		CountedContents: []usecases.CountedContent{{SKU: "SKU-2", Qty: 4}, {SKU: "SKU-1", Qty: 3}},
	})
	wantNoErr(t, err)
	lines := out.Receipt.Lines()
	if lines[0].ReceivedGood() != 3 || lines[1].ReceivedGood() != 4 {
		t.Fatalf("received = %d / %d, want the counted 3 / 4", lines[0].ReceivedGood(), lines[1].ReceivedGood())
	}
	var got []string
	for _, d := range out.Result.Discrepancies {
		got = append(got, string(d.Kind)+" "+d.Expected+" "+d.Actual)
	}
	want := "MixedCartonContentMismatch SKU-2:5 SKU-2:4,MixedCartonContentMismatch SKU-1:0 SKU-1:3"
	if strings.Join(got, ",") != want {
		t.Fatalf("findings = %v, want %s", got, want)
	}
	// the extra SKU got its each GTIN from the PackagingView
	if g := out.Result.Cartons[0].Contents(); len(g) != 2 || g[1].GTIN != w.each1 {
		t.Fatalf("carton contents = %+v", g)
	}
}

func TestScanCountRefusals(t *testing.T) {
	tests := []struct {
		name string
		cmd  func(w *scanWorld) usecases.ReceiveScanCommand
		want error
	}{
		{"sku unknown to the packaging view", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccM.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-2", Qty: 5}, {SKU: "SKU-9", Qty: 1}}}
		}, usecases.ErrCountedSkuUnknown},
		{"invalid sku", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccM.String(), CountedContents: []usecases.CountedContent{{SKU: "a/b", Qty: 1}}}
		}, shared.ErrInvalidSKU},
		{"count on a case gtin scan", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: w.case1.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-1", Qty: 1}}}
		}, usecases.ErrInvalidScan},
		{"count on a raw gtin scan", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Scan: "]E0" + w.each1.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-1", Qty: 1}}}
		}, usecases.ErrInvalidScan},
		{"count on a homogeneous carton", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-1", Qty: 20}}}
		}, receipt.ErrCountedContentsNotMixed},
		{"too many lines", func(w *scanWorld) usecases.ReceiveScanCommand {
			counted := make([]usecases.CountedContent, usecases.MaxCountedContents+1)
			return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccM.String(), CountedContents: counted}
		}, usecases.ErrInvalidScan},
		{"repeated sku", func(w *scanWorld) usecases.ReceiveScanCommand {
			return usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccM.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-2", Qty: 1}, {SKU: "SKU-2", Qty: 2}}}
		}, receipt.ErrInvalidCount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newScanWorld(t)
			_, err := w.scan(tt.cmd(w))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if got := w.newTypes(); len(got) != 0 {
				t.Fatalf("a refused count must write nothing, wrote %v", got)
			}
		})
	}
}

func TestMaxCountedContentsIsAtLeastOneHundred(t *testing.T) {
	if usecases.MaxCountedContents != 200 {
		t.Fatalf("MaxCountedContents = %d, want 200 (the documented maxItems)", usecases.MaxCountedContents)
	}
}

func TestGetCartonLabel(t *testing.T) {
	w := newScanWorld(t)
	uc := &usecases.GetCartonLabel{Receipts: w.receipts}
	ctx := context.Background()
	_, err := w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccA.String()})
	wantNoErr(t, err)
	got, err := uc.Handle(ctx, w.receipt, w.ssccA.String())
	wantNoErr(t, err)
	if got.LPN != w.ssccA.String() || got.Symbology != "CODE128" || got.HumanReadable != "(00)"+w.ssccA.String() ||
		got.SKU != "SKU-1" || got.Units != 20 || got.ReceivedAt.IsZero() {
		t.Fatalf("label = %+v", got)
	}

	// a carton with several SKUs has no single SKU; units add up
	_, err = w.scan(usecases.ReceiveScanCommand{Kind: "SSCC", ID: w.ssccM.String(), CountedContents: []usecases.CountedContent{{SKU: "SKU-2", Qty: 5}, {SKU: "SKU-1", Qty: 2}}})
	wantNoErr(t, err)
	got, err = uc.Handle(ctx, w.receipt, w.ssccM.String())
	wantNoErr(t, err)
	if got.SKU != "" || got.Units != 7 {
		t.Fatalf("mixed label = %+v", got)
	}

	// an unlabelled case: the internal LPN is both plate and human readable text
	out, err := w.scan(usecases.ReceiveScanCommand{Kind: "CASE_GTIN", ID: w.case1.String()})
	wantNoErr(t, err)
	lpn := out.Result.Cartons[0].LPN()
	got, err = uc.Handle(ctx, w.receipt, lpn)
	wantNoErr(t, err)
	if got.HumanReadable != lpn || !strings.HasPrefix(lpn, "LPN-") || got.Units != 20 {
		t.Fatalf("lpn label = %+v", got)
	}
}

func TestGetCartonLabelRefusals(t *testing.T) {
	w := newScanWorld(t)
	uc := &usecases.GetCartonLabel{Receipts: w.receipts}
	ctx := context.Background()
	// announced but not accepted yet
	if _, err := uc.Handle(ctx, w.receipt, w.ssccB.String()); !errors.Is(err, usecases.ErrCartonNotFound) {
		t.Fatalf("expected carton: err = %v", err)
	}
	if _, err := uc.Handle(ctx, w.receipt, "LPN-NOPE"); !errors.Is(err, usecases.ErrCartonNotFound) {
		t.Fatalf("unknown lpn: err = %v", err)
	}
	if _, err := uc.Handle(ctx, "bad-id", w.ssccA.String()); err == nil {
		t.Fatal("an invalid receipt id must be an error")
	}
	if _, err := uc.Handle(ctx, "rcpt-00000000-0000-0000-0000-000000000000", w.ssccA.String()); err == nil {
		t.Fatal("an unknown receipt must be an error")
	}
}
