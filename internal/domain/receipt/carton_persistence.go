package receipt

import (
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// manifestStates turns the ASN's cartons into Expected ledger entries.
func manifestStates(cartons []asn.Carton) []CartonState {
	out := make([]CartonState, 0, len(cartons))
	for _, c := range cartons {
		contents := make([]CartonContentState, 0, len(c.Contents()))
		for _, x := range c.Contents() {
			contents = append(contents, CartonContentState{
				LineNo: x.LineNo(), SKU: string(x.SKU()), GTIN: x.GTIN().String(), Qty: x.Qty(), Lot: x.Lot(), Expiry: x.Expiry(),
			})
		}
		out = append(out, CartonState{
			LPN: c.SSCC().String(), SSCC: c.SSCC().String(), ParentSSCC: c.ParentSSCC().String(),
			Kind: string(c.Kind()), Status: CartonExpected, Contents: contents,
		})
	}
	return out
}

// buildCartons rebuilds the ledger from its stored shape, re-validating it
// against the lines.
func buildCartons(states []CartonState, lines []Line) ([]Carton, error) {
	if len(states) == 0 {
		return nil, nil
	}
	out := make([]Carton, 0, len(states))
	for _, s := range states {
		c, err := buildCarton(s, len(lines))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func buildCarton(s CartonState, lineCount int) (Carton, error) {
	if s.LPN == "" || !s.Status.valid() || len(s.Contents) == 0 {
		return Carton{}, ErrInvalidCarton
	}
	accepted := s.Status == CartonAccepted
	if accepted == s.ReceivedAt.IsZero() || (s.Sealed && !accepted) {
		return Carton{}, ErrInvalidCarton
	}
	sscc, err := optionalSSCC(s.SSCC)
	if err != nil {
		return Carton{}, err
	}
	parent, err := optionalSSCC(s.ParentSSCC)
	if err != nil {
		return Carton{}, err
	}
	kind, err := asn.ParseCartonKind(s.Kind)
	if err != nil {
		return Carton{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
	}
	contents := make([]CartonContent, 0, len(s.Contents))
	for _, x := range s.Contents {
		c, err := buildCartonContent(x, accepted, lineCount)
		if err != nil {
			return Carton{}, err
		}
		contents = append(contents, c)
	}
	return Carton{
		lpn: s.LPN, sscc: sscc, parentSscc: parent, kind: kind, status: s.Status, sealed: s.Sealed,
		contents: contents, receivedAt: s.ReceivedAt.UTC(),
	}, nil
}

func optionalSSCC(value string) (gs1.SSCC, error) {
	if value == "" {
		return gs1.SSCC{}, nil
	}
	s, err := gs1.ParseSSCC(value)
	if err != nil {
		return gs1.SSCC{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
	}
	return s, nil
}

func buildCartonContent(s CartonContentState, accepted bool, lineCount int) (CartonContent, error) {
	if s.LineNo < 1 || s.LineNo > lineCount {
		return CartonContent{}, ErrInvalidCarton
	}
	sku, err := shared.NewSKU(s.SKU)
	if err != nil {
		return CartonContent{}, err
	}
	gtin, err := gs1.ParseGTIN(s.GTIN)
	if err != nil {
		return CartonContent{}, err
	}
	if err := shared.ValidateQuantity(s.Qty); err != nil {
		return CartonContent{}, err
	}
	var condition Condition
	if accepted {
		if condition, err = ParseCondition(s.Condition); err != nil {
			return CartonContent{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
		}
	} else if s.Condition != "" {
		return CartonContent{}, ErrInvalidCarton
	}
	return CartonContent{LineNo: s.LineNo, SKU: sku, GTIN: gtin, Qty: s.Qty, Lot: s.Lot, Expiry: s.Expiry, Condition: condition}, nil
}

// Persisted returns the stored shape of the receipt, the inverse of Rehydrate.
func (r *Receipt) Persisted() Persisted {
	lines := make([]LineState, 0, len(r.lines))
	for _, l := range r.lines {
		lines = append(lines, LineState{
			LineNo: l.lineNo, SKU: string(l.sku), ExpectedQty: l.expectedQty,
			ReceivedGood: l.receivedGood, ReceivedDamaged: l.receivedDamaged,
		})
	}
	cartons := make([]CartonState, 0, len(r.cartons))
	for _, c := range r.cartons {
		cartons = append(cartons, c.state())
	}
	return Persisted{
		ID: string(r.id), AsnNumber: string(r.asnNumber), OwnerRef: string(r.ownerRef),
		AppointmentID: string(r.appointmentID), DoorCode: string(r.doorCode), State: r.state,
		Lines: lines, Cartons: cartons, CartonDiscrepancies: findingStates(r.findings), OpenedAt: r.openedAt, ClosedAt: r.closedAt, Version: r.version,
	}
}

func (c Carton) state() CartonState {
	contents := make([]CartonContentState, 0, len(c.contents))
	for _, x := range c.contents {
		contents = append(contents, CartonContentState{
			LineNo: x.LineNo, SKU: string(x.SKU), GTIN: x.GTIN.String(), Qty: x.Qty,
			Lot: x.Lot, Expiry: x.Expiry, Condition: string(x.Condition),
		})
	}
	return CartonState{
		LPN: c.lpn, SSCC: c.sscc.String(), ParentSSCC: c.parentSscc.String(), Kind: string(c.kind),
		Status: c.status, Sealed: c.sealed, Contents: contents, ReceivedAt: c.receivedAt,
	}
}
