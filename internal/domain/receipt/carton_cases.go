package receipt

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// ReceiveCases receives cases scanned by their case GTIN: Cases x
// UnitsPerPack eaches are booked on the SKU's line by ONE ReceiptLineReceived,
// and every case becomes its own sealed carton with an internal license plate
// on ReceiptCartonReceived. Against a carton ASN a case without an SSCC also
// raises UnlabelledCarton; against a flat ASN nobody promised a label.
func (r *Receipt) ReceiveCases(in CaseScan, at time.Time) (ScanResult, error) {
	if !in.AssignedSSCC.IsZero() {
		return ScanResult{}, ErrScanMismatch // an assigned SSCC goes through AssignSscc
	}
	return r.receiveCases(in, at)
}

// AssignSscc receives ONE case that arrived without a label under the SSCC the
// receiver allocated (ADR 0024): it is booked exactly like a case scan of one
// case, but the carton gets the assigned SSCC as its SSCC and its LPN and is
// marked receiver-assigned; against a carton ASN it still raises UnlabelledCarton,
// because the carton did arrive without a label. The SSCC must be new to the ledger.
func (r *Receipt) AssignSscc(in CaseScan, at time.Time) (ScanResult, error) {
	if in.AssignedSSCC.IsZero() || in.Cases != 1 || len(in.LPNs) != 1 || in.LPNs[0] != in.AssignedSSCC.String() {
		return ScanResult{}, ErrScanMismatch
	}
	if r.cartonIndex(in.AssignedSSCC) >= 0 {
		return ScanResult{}, ErrScanMismatch
	}
	return r.receiveCases(in, at)
}

func (r *Receipt) receiveCases(in CaseScan, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	if !in.Condition.valid() {
		return ScanResult{}, ErrInvalidCondition
	}
	if in.Cases < 1 || in.Cases > MaxCasesPerScan {
		return ScanResult{}, ErrInvalidCases
	}
	if int64(len(in.LPNs)) != in.Cases {
		return ScanResult{}, ErrScanMismatch
	}
	lineNo, ok := r.LineOfSKU(in.SKU)
	if !ok {
		return ScanResult{}, ErrLineNotOnAsn
	}
	if err := shared.ValidateQuantity(in.Pack.UnitsPerPack); err != nil {
		return ScanResult{}, err
	}
	qty := in.Cases * in.Pack.UnitsPerPack
	lines := r.Lines()
	if err := bookLine(lines, lineNo, qty, in.Condition); err != nil {
		return ScanResult{}, err
	}
	labelled := r.hasManifest()
	events := []Event{ReceiptLineReceived{
		Header: r.header(at), LineNo: lineNo, SKU: in.SKU, Quantity: qty, Condition: in.Condition,
		OwnerRef: r.ownerRef, PackGTIN: in.Pack.GTIN, Cases: in.Cases, SSCC: in.AssignedSSCC,
	}}
	res := ScanResult{Accepted: true}
	for _, lpn := range in.LPNs {
		c := Carton{
			lpn: lpn, sscc: in.AssignedSSCC, kind: asn.CartonHomogeneousCase, status: CartonAccepted, sealed: true, receivedAt: at.UTC(),
			label:    in.Label,
			contents: []CartonContent{{LineNo: lineNo, SKU: in.SKU, GTIN: in.ChildGTIN, Qty: in.Pack.UnitsPerPack, Lot: in.Lot, Expiry: in.Expiry, Condition: in.Condition}},
		}
		if !in.AssignedSSCC.IsZero() {
			c.ssccSource = SsccReceiver
		}
		r.cartons = append(r.cartons, c)
		res.Cartons = append(res.Cartons, c)
		events = append(events, r.cartonReceived(c, at))
		if labelled {
			res.Discrepancies = append(res.Discrepancies, CartonDiscrepancy{Kind: KindUnlabelledCarton, Actual: lpn})
		}
	}
	res.Events = events
	res.Events = append(res.Events, r.raise(res.Discrepancies, at)...)
	r.lines = lines
	r.lastReceivedAt = at.UTC()
	r.version++
	return res, nil
}

// RejectUnknownGtin records that a scanned GTIN does not resolve in the
// PackagingView: nothing is booked, the finding is raised.
func (r *Receipt) RejectUnknownGtin(gtin gs1.GTIN, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	return r.reject(at, nil, CartonDiscrepancy{Kind: KindUnknownGtin, Actual: gtin.String()}), nil
}

func (r *Receipt) requireOpen() error {
	if r.state != StateOpen {
		return ErrReceiptClosed
	}
	return nil
}

// reject raises the findings of a scan that was not accepted.
func (r *Receipt) reject(at time.Time, cartons []Carton, findings ...CartonDiscrepancy) ScanResult {
	r.version++
	return ScanResult{Events: r.raise(findings, at), Cartons: cartons, Discrepancies: findings}
}

// missingCartonEvents raises MissingCarton for every manifest carton that was
// never accepted.
func (r *Receipt) missingCartonEvents(at time.Time) []Event {
	var missing []CartonDiscrepancy
	for _, c := range r.cartons {
		if c.status == CartonExpected {
			missing = append(missing, CartonDiscrepancy{Kind: KindMissingCarton, Expected: c.sscc.String()})
		}
	}
	return r.raise(missing, at)
}

// bookLine adds qty to the line of lines in the given condition, enforcing
// the quantity bound. lines is a COPY of the receipt's lines, so a failure
// part-way through a carton leaves the receipt untouched.
func bookLine(lines []Line, lineNo int, qty int64, condition Condition) error {
	line := &lines[lineNo-1]
	if err := shared.ValidateQuantity(qty); err != nil {
		return err
	}
	if line.Received()+qty > shared.MaxQuantity {
		return shared.ErrInvalidQuantity
	}
	if condition == ConditionGood {
		line.receivedGood += qty
	} else {
		line.receivedDamaged += qty
	}
	return nil
}
