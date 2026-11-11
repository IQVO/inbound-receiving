package receipt

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// KindMissingContentGtin (ADR 0025): a carton scan against a FLAT ASN (no
// carton manifest, the S-O-I form) carried no AI 02 content GTIN, so no ASN
// line could be resolved for it. The label may be GS1-compliant on its own;
// a flat ASN simply has nothing else to resolve the carton against.
const KindMissingContentGtin Kind = "MissingContentGtin"

// ReceiveFlatCarton accepts a carton scanned against a FLAT ASN (ADR 0025):
// an ASN announced with lines but no carton manifest, the S-O-I form. There
// is no manifest to resolve the SSCC against, so the label itself says what
// is inside: its AI 02 content GTIN (resolved to a SKU by the use case, never
// here) picks the ASN line and its AI 37 count (default 1) the quantity,
// booked in the scan's condition through the receipt's own line rules. The
// carton is accepted with the scanned SSCC as its identity.
//
// A scan that cannot be resolved is NOT an error and books nothing: it raises
// its finding as CartonDiscrepancyRaised like every refused scan (no AI 02 is
// MissingContentGtin; a GTIN that resolves to no line of the ASN is
// UnknownGtin), and the receipt keeps its version history.
func (r *Receipt) ReceiveFlatCarton(in CartonScan, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	if !in.Condition.valid() {
		return ScanResult{}, ErrInvalidCondition
	}
	if idx := r.cartonIndex(in.SSCC); idx >= 0 {
		return r.reject(at, []Carton{r.cartons[idx]}, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if in.ReceivedElsewhere {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if in.LabelContentGTIN.IsZero() {
		return r.reject(at, nil, missingContentGTIN(in)), nil
	}
	lineNo, ok := r.LineOfSKU(in.LabelContentSKU)
	if !ok {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindUnknownGtin, SSCC: in.SSCC, Actual: in.LabelContentGTIN.String()}), nil
	}
	return r.acceptFlatCarton(in, lineNo, at)
}

// missingContentGTIN is the refusal of a flat-ASN scan whose label carried no
// AI 02. A raw label with AI 37 but no AI 02 never reaches here (the gs1
// package refuses it as a missing_ai violation, ADR 0024): this is the
// SSCC-only label, so the finding carries the SSCC and nothing else.
func missingContentGTIN(in CartonScan) CartonDiscrepancy {
	return CartonDiscrepancy{Kind: KindMissingContentGtin, SSCC: in.SSCC}
}

func (r *Receipt) acceptFlatCarton(in CartonScan, lineNo int, at time.Time) (ScanResult, error) {
	qty := defaultFlatCount(in.LabelCount)
	if err := shared.ValidateQuantity(qty); err != nil {
		return ScanResult{}, err
	}
	lines := r.Lines()
	if err := bookLine(lines, lineNo, qty, in.Condition); err != nil {
		return ScanResult{}, err
	}
	content := CartonContent{LineNo: lineNo, SKU: in.LabelContentSKU, GTIN: in.LabelContentGTIN, Qty: qty, Condition: in.Condition}
	c := Carton{
		lpn: in.SSCC.String(), sscc: in.SSCC, kind: asn.CartonHomogeneousCase, status: CartonAccepted,
		sealed: true, receivedAt: at.UTC(), contents: []CartonContent{content}, label: in.Label,
	}
	events := []Event{ReceiptLineReceived{
		Header: r.header(at), LineNo: lineNo, SKU: in.LabelContentSKU, Quantity: qty, Condition: in.Condition,
		OwnerRef: r.ownerRef, SSCC: in.SSCC,
	}}
	events = append(events, r.cartonReceived(c, at))
	r.lines = lines
	r.cartons = append(r.cartons, c)
	r.lastReceivedAt = at.UTC()
	r.version++
	return ScanResult{Events: events, Accepted: true, Cartons: []Carton{c}}, nil
}

// defaultFlatCount reads a label without AI 37 as ONE contained unit.
func defaultFlatCount(count int64) int64 {
	if count == 0 {
		return 1
	}
	return count
}
