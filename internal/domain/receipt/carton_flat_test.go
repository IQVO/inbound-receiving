package receipt

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// flatReceipt is an open receipt against a FLAT ASN (ADR 0025): two lines, no
// carton manifest.
func flatReceipt(t *testing.T) *Receipt {
	t.Helper()
	r, err := Rehydrate(Persisted{
		ID: "rcpt-00000000-0000-4000-8000-000000000001", AsnNumber: "ASN-FLAT-1", State: StateOpen,
		OpenedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), Version: 1,
		Lines: []LineState{
			{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40},
			{LineNo: 2, SKU: "SKU-2", ExpectedQty: 5},
		},
	})
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	return r
}

func flatScan(sscc, gtin string, count int64) CartonScan {
	s, _ := gs1.ParseSSCC(sscc)
	g, _ := gs1.ParseGTIN(gtin)
	in := CartonScan{SSCC: s, Condition: ConditionGood, LabelContentGTIN: g, LabelCount: count}
	if !g.IsZero() {
		in.LabelContentSKU = skuOfGTIN(g)
	}
	return in
}

// skuOfGTIN stands for the PackagingView in the domain tests: GTIN ...92 is
// SKU-1 (line 1) and ...09 is SKU-2 (line 2); ...55 maps to no line.
func skuOfGTIN(g gs1.GTIN) shared.SKU {
	switch g.String() {
	case "10001234567892":
		return shared.SKU("SKU-1")
	case "10001234567502":
		return shared.SKU("SKU-2")
	default:
		return ""
	}
}

var flatAt = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// A content-GTIN scan against a flat ASN books the AI 37 count on the line
// the GTIN resolves to and ACCEPTS the carton (ADR 0025): not
// UnexpectedCarton.
func TestReceiveFlatCartonBooksTheLabelCount(t *testing.T) {
	r := flatReceipt(t)
	res, err := r.ReceiveFlatCarton(flatScan("003456789012345670", "10001234567892", 12), flatAt)
	if err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("accepted = false, discrepancies %+v", res.Discrepancies)
	}
	line := r.Lines()[0]
	if line.ReceivedGood() != 12 || line.ReceivedDamaged() != 0 {
		t.Fatalf("line 1 = good %d damaged %d, want 12 booked good", line.ReceivedGood(), line.ReceivedDamaged())
	}
	if got := r.Lines()[1].Received(); got != 0 {
		t.Fatalf("line 2 received = %d, want 0", got)
	}
	wantFlatCarton(t, r)
	if len(res.Events) != 2 {
		t.Fatalf("events = %d (%v), want the line event then the carton event", len(res.Events), res.Events)
	}
	wantFlatEvents(t, res.Events)
}

// wantFlatCarton pins the ledger entry an accepted flat scan creates: the
// scanned SSCC as its identity, supplier-sourced, sealed, one content.
func wantFlatCarton(t *testing.T, r *Receipt) {
	t.Helper()
	c, ok := r.CartonByLPN("003456789012345670")
	if !ok || c.Status() != CartonAccepted || !c.Sealed() || c.SSCCSource() != SsccSupplier {
		t.Fatalf("carton = %+v, want an accepted sealed supplier carton", c)
	}
	contents := c.Contents()
	if len(contents) != 1 || contents[0].LineNo != 1 || contents[0].Qty != 12 || contents[0].GTIN.String() != "10001234567892" || contents[0].Condition != ConditionGood {
		t.Fatalf("contents = %+v", contents)
	}
}

// wantFlatEvents pins the two events of an accepted flat scan: the line
// booking (quantity authority, SSCC carried) then the carton identity.
func wantFlatEvents(t *testing.T, events []Event) {
	t.Helper()
	le, ok := events[0].(ReceiptLineReceived)
	if !ok || le.LineNo != 1 || le.Quantity != 12 || le.SSCC.String() != "003456789012345670" || le.PackGTIN != (gs1.GTIN{}) {
		t.Fatalf("line event = %+v", le)
	}
	if _, ok := events[1].(ReceiptCartonReceived); !ok {
		t.Fatalf("second event = %T, want ReceiptCartonReceived", events[1])
	}
}

// The count defaults to ONE without AI 37, and the scan condition is honoured.
func TestReceiveFlatCartonDefaultsToOneAndHonoursCondition(t *testing.T) {
	r := flatReceipt(t)
	res, err := r.ReceiveFlatCarton(CartonScan{
		SSCC:      flatScan("003456789012345687", "10001234567892", 0).SSCC,
		Condition: ConditionDamaged, LabelContentGTIN: flatScan("003456789012345687", "10001234567892", 0).LabelContentGTIN,
		LabelContentSKU: shared.SKU("SKU-1"),
	}, flatAt)
	if err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("accepted = false, discrepancies %+v", res.Discrepancies)
	}
	if line := r.Lines()[0]; line.ReceivedDamaged() != 1 || line.ReceivedGood() != 0 {
		t.Fatalf("line 1 = good %d damaged %d, want 1 damaged", line.ReceivedGood(), line.ReceivedDamaged())
	}
}

// The GTIN resolves the LINE: line 2 when the content GTIN is SKU-2's.
func TestReceiveFlatCartonResolvesTheSecondLine(t *testing.T) {
	r := flatReceipt(t)
	res, err := r.ReceiveFlatCarton(flatScan("003456789012345694", "10001234567502", 3), flatAt)
	if err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	if !res.Accepted || r.Lines()[1].Received() != 3 || r.Lines()[0].Received() != 0 {
		t.Fatalf("accepted %v, lines = %d / %d, want 0 / 3", res.Accepted, r.Lines()[0].Received(), r.Lines()[1].Received())
	}
}

// A flat-ASN scan with no AI 02 is refused with MissingContentGtin and books
// nothing (ADR 0025): the label may be compliant, but a flat ASN has nothing
// else to resolve the carton against.
func TestReceiveFlatCartonWithoutContentGTINIsRefused(t *testing.T) {
	r := flatReceipt(t)
	res, err := r.ReceiveFlatCarton(flatScan("003456789012345670", "", 12), flatAt)
	if err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	if res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0].Kind != KindMissingContentGtin {
		t.Fatalf("result = accepted %v discrepancies %+v, want MissingContentGtin", res.Accepted, res.Discrepancies)
	}
	if d := res.Discrepancies[0]; d.SSCC.String() != "003456789012345670" || d.Expected != "" || d.Actual != "" {
		t.Fatalf("finding = %+v, want only the SSCC carried", d)
	}
	if coding, ok := CodingOf(KindMissingContentGtin); !ok || coding.Mapped() || coding.CBV != DispositionNonConformant {
		t.Fatalf("coding of MissingContentGtin = %+v %v", coding, ok)
	}
	if line := r.Lines()[0]; line.Received() != 0 {
		t.Fatalf("line 1 received = %d, want nothing booked", line.Received())
	}
	if _, ok := r.CartonByLPN("003456789012345670"); ok {
		t.Fatal("a refused flat scan must not create a carton")
	}
}

// A content GTIN that resolves to no line of the ASN is refused as
// UnknownGtin (the flat restatement of ADR 0014's rule) and books nothing.
func TestReceiveFlatCartonWithUnresolvableGTINIsRefused(t *testing.T) {
	r := flatReceipt(t)
	res, err := r.ReceiveFlatCarton(flatScan("003456789012345670", "10001234567557", 4), flatAt)
	if err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	if res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0].Kind != KindUnknownGtin {
		t.Fatalf("result = accepted %v discrepancies %+v, want UnknownGtin", res.Accepted, res.Discrepancies)
	}
	if d := res.Discrepancies[0]; d.SSCC.String() != "003456789012345670" || d.Actual != "10001234567557" {
		t.Fatalf("finding = %+v, want the SSCC and the GTIN", d)
	}
	if r.Lines()[0].Received() != 0 || r.Lines()[1].Received() != 0 {
		t.Fatal("nothing may be booked on a refused flat scan")
	}
}

// An SSCC already accepted on the flat receipt is DuplicateSscc, exactly like
// a manifest carton's; a scan seen elsewhere on the site too.
func TestReceiveFlatCartonDuplicateSscc(t *testing.T) {
	r := flatReceipt(t)
	scan := flatScan("003456789012345670", "10001234567892", 6)
	if _, err := r.ReceiveFlatCarton(scan, flatAt); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	res, err := r.ReceiveFlatCarton(scan, flatAt)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0].Kind != KindDuplicateSscc {
		t.Fatalf("result = accepted %v discrepancies %+v, want DuplicateSscc", res.Accepted, res.Discrepancies)
	}
	if line := r.Lines()[0]; line.Received() != 6 {
		t.Fatalf("line 1 received = %d, want the single booking of 6", line.Received())
	}
}

// A flat scan against a closed receipt is an error and changes nothing.
func TestReceiveFlatCartonOnClosedReceipt(t *testing.T) {
	r := flatReceipt(t)
	if _, err := r.Close(flatAt); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := r.ReceiveFlatCarton(flatScan("003456789012345670", "10001234567892", 6), flatAt); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("error = %v, want ErrReceiptClosed", err)
	}
}

// Close is unchanged for a flat ASN: expected quantities stay, Short/Over are
// computed from what was booked, and no MissingCarton can arise (no carton
// was ever Expected).
func TestFlatReceiptClose(t *testing.T) {
	r := flatReceipt(t)
	if _, err := r.ReceiveFlatCarton(flatScan("003456789012345670", "10001234567892", 41), flatAt); err != nil {
		t.Fatalf("ReceiveFlatCarton: %v", err)
	}
	events, err := r.Close(flatAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	var closed ReceiptClosed
	var missing, discrepancyEvents int
	for _, ev := range events {
		switch e := ev.(type) {
		case ReceiptClosed:
			closed = e
		case CartonDiscrepancyRaised:
			discrepancyEvents++
			if e.Kind == KindMissingCarton {
				missing++
			}
		}
	}
	if missing != 0 {
		t.Fatal("a flat ASN has no manifest: no MissingCarton may be raised")
	}
	if discrepancyEvents != 0 {
		t.Fatalf("discrepancy events = %d, want 0", discrepancyEvents)
	}
	// Line 1: expected 40, received 41 -> Over. Line 2: expected 5, received 0 -> Short.
	kinds := map[int]Kind{}
	for _, d := range closed.Discrepancies {
		kinds[d.LineNo] = d.Kind
	}
	if kinds[1] != KindOver || kinds[2] != KindShort {
		t.Fatalf("discrepancies = %+v, want Over on line 1 and Short on line 2", closed.Discrepancies)
	}
}
