package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// flatWorld registers a FLAT ASN (lines, no carton manifest, the S-O-I form
// of ADR 0025) and opens its receipt.
type flatWorld struct {
	*world
	pack    *fakePackaging
	uc      *usecases.ReceiveScan
	receipt string
}

func newFlatWorld(t *testing.T) *flatWorld {
	t.Helper()
	w := &flatWorld{world: newWorld(), pack: newFakePackaging()}
	w.pack.add(level(mustGTIN(t, "629104150021"), "SKU-1", shared.PackEach, gs1.GTIN{}, 1))
	w.pack.add(level(mustGTIN(t, "629104150038"), "SKU-2", shared.PackEach, gs1.GTIN{}, 1))
	w.uc = &usecases.ReceiveScan{Writer: w.writer, Packaging: w.pack}
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: "ASN-FLAT", SupplierRef: "ACME",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5}},
	})
	wantNoErr(t, err)
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-FLAT"})
	wantNoErr(t, err)
	w.receipt = string(r.ID())
	return w
}

func (w *flatWorld) scan(raw string) (usecases.ScanOutcome, error) {
	return w.uc.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: w.receipt, Scan: raw})
}

// A content-GTIN scan against a flat ASN books the label's count and accepts
// the carton (ADR 0025): the answer is accepted, NOT UnexpectedCarton.
func TestFlatAsnReceivesByContentGTINScan(t *testing.T) {
	w := newFlatWorld(t)
	out, err := w.scan("]C100" + mustSSCC(t, "00345678901234567").String() + "02" + mustGTIN(t, "629104150021").String() + "3712")
	wantNoErr(t, err)
	if out.Kind != usecases.ScanSSCC || !out.Result.Accepted {
		t.Fatalf("kind %s accepted %v discrepancies %+v, want an accepted SSCC scan", out.Kind, out.Result.Accepted, out.Result.Discrepancies)
	}
	r, err := (&usecases.GetReceipt{Receipts: w.receipts}).Handle(context.Background(), w.receipt)
	wantNoErr(t, err)
	if line := r.Lines()[0]; line.ReceivedGood() != 12 {
		t.Fatalf("line 1 good = %d, want 12", line.ReceivedGood())
	}
}

// A flat-ASN label without AI 02 is refused with the MissingContentGtin
// finding and nothing is booked. (A raw label carrying AI 37 without AI 02 is
// already a LabelNonCompliant parse refusal, ADR 0024; the flat refusal is
// the SSCC-only label, which parses.)
func TestFlatAsnScanWithoutContentGTINIsRefused(t *testing.T) {
	w := newFlatWorld(t)
	out, err := w.scan("]C100" + mustSSCC(t, "00345678901234567").String())
	wantNoErr(t, err)
	if out.Result.Accepted || len(out.Result.Discrepancies) != 1 || out.Result.Discrepancies[0].Kind != "MissingContentGtin" {
		t.Fatalf("accepted %v discrepancies %+v, want MissingContentGtin", out.Result.Accepted, out.Result.Discrepancies)
	}
	r, err := (&usecases.GetReceipt{Receipts: w.receipts}).Handle(context.Background(), w.receipt)
	wantNoErr(t, err)
	if got := r.Lines()[0].Received(); got != 0 {
		t.Fatalf("line 1 received = %d, want nothing booked", got)
	}
}

// A hierarchical ASN still refuses an unknown SSCC exactly as before (ADR
// 0025 leaves the manifest path strict).
func TestCartonAsnStillRefusesUnknownSscc(t *testing.T) {
	w := &flatWorld{world: newWorld(), pack: newFakePackaging()}
	w.pack.add(level(mustGTIN(t, "629104150021"), "SKU-1", shared.PackEach, gs1.GTIN{}, 1))
	w.uc = &usecases.ReceiveScan{Writer: w.writer, Packaging: w.pack}
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: "ASN-H", SupplierRef: "ACME",
		Lines:   []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: cartonsOf(t, mustSSCC(t, "00345678901234567"), mustGTIN(t, "629104150021")),
	})
	wantNoErr(t, err)
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-H"})
	wantNoErr(t, err)
	w.receipt = string(r.ID())
	// A content-GTIN label on an SSCC the manifest never announced: still
	// UnexpectedCarton even though the scan carries AI 02.
	out, err := w.scan("]C100" + mustSSCC(t, "00345678901234570").String() + "02" + mustGTIN(t, "629104150021").String() + "3712")
	wantNoErr(t, err)
	if out.Result.Accepted || len(out.Result.Discrepancies) != 1 || out.Result.Discrepancies[0].Kind != "UnexpectedCarton" {
		t.Fatalf("accepted %v discrepancies %+v, want UnexpectedCarton", out.Result.Accepted, out.Result.Discrepancies)
	}
}

func cartonsOf(t *testing.T, sscc, gtin interface{ String() string }) []asn.CartonInput {
	t.Helper()
	return []asn.CartonInput{{SSCC: sscc.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: gtin.String(), Qty: 20}}}}
}

// The flat path resolves the SECOND line when the content GTIN is SKU-2's,
// and a second scan of an accepted flat carton is DuplicateSscc.
func TestFlatAsnSecondLineAndDuplicate(t *testing.T) {
	w := newFlatWorld(t)
	out, err := w.scan("]C100" + mustSSCC(t, "00345678901234567").String() + "02" + mustGTIN(t, "629104150038").String() + "375")
	wantNoErr(t, err)
	if !out.Result.Accepted {
		t.Fatalf("accepted = false, discrepancies %+v", out.Result.Discrepancies)
	}
	r, err := (&usecases.GetReceipt{Receipts: w.receipts}).Handle(context.Background(), w.receipt)
	wantNoErr(t, err)
	if line := r.Lines()[1]; line.ReceivedGood() != 5 {
		t.Fatalf("line 2 good = %d, want 5", line.ReceivedGood())
	}
	again, err := w.scan("]C100" + mustSSCC(t, "00345678901234567").String() + "02" + mustGTIN(t, "629104150038").String() + "375")
	wantNoErr(t, err)
	if again.Result.Accepted || len(again.Result.Discrepancies) != 1 || again.Result.Discrepancies[0].Kind != "DuplicateSscc" {
		t.Fatalf("accepted %v discrepancies %+v, want DuplicateSscc", again.Result.Accepted, again.Result.Discrepancies)
	}
}

// A flat ASN registered with lines but no cartons keeps working end to end:
// close reports Short on the untouched line (expected quantities unchanged).
func TestFlatAsnCloseReportsShort(t *testing.T) {
	w := newFlatWorld(t)
	_, err := w.scan("]C100" + mustSSCC(t, "00345678901234567").String() + "02" + mustGTIN(t, "629104150021").String() + "3740")
	wantNoErr(t, err)
	closed, err := (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	_ = closed
	var shortOnLine2 bool
	for _, d := range closed.Discrepancies() {
		if d.LineNo == 2 && d.Kind == "Short" {
			shortOnLine2 = true
		}
	}
	if !shortOnLine2 {
		t.Fatalf("discrepancies = %+v, want Short on line 2", closed.Discrepancies())
	}
}
