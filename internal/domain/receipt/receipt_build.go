package receipt

import (
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

func build(p Persisted) (*Receipt, error) {
	id, err := NewID(p.ID)
	if err != nil {
		return nil, err
	}
	number, err := asn.NewNumber(p.AsnNumber)
	if err != nil {
		return nil, err
	}
	appointmentID, doorCode, err := buildOrigin(p.AppointmentID, p.DoorCode)
	if err != nil {
		return nil, err
	}
	if err := validateTimestamps(p); err != nil {
		return nil, err
	}
	owner, err := shared.NewOwnerRef(p.OwnerRef)
	if err != nil {
		return nil, err
	}
	lines, err := buildLines(p.Lines)
	if err != nil {
		return nil, err
	}
	cartons, err := buildCartons(p.Cartons, lines)
	if err != nil {
		return nil, err
	}
	findings, err := buildFindings(p.CartonDiscrepancies)
	if err != nil {
		return nil, err
	}
	return &Receipt{
		id:            id,
		asnNumber:     number,
		ownerRef:      owner,
		appointmentID: appointmentID,
		doorCode:      doorCode,
		state:         p.State,
		lines:         lines,
		cartons:       cartons,
		findings:      findings,
		openedAt:      p.OpenedAt.UTC(),
		closedAt:      p.ClosedAt.UTC(),
		version:       p.Version,
	}, nil
}

func buildOrigin(appointmentID, doorCode string) (appointment.ID, appointment.DoorCode, error) {
	var id appointment.ID
	var door appointment.DoorCode
	var err error
	if appointmentID != "" {
		if id, err = appointment.NewID(appointmentID); err != nil {
			return "", "", err
		}
	}
	if doorCode != "" {
		if door, err = appointment.NewDoorCode(doorCode); err != nil {
			return "", "", err
		}
	}
	return id, door, nil
}

func validateTimestamps(p Persisted) error {
	if p.OpenedAt.IsZero() {
		return ErrInvalidTimestamps
	}
	if (p.State == StateClosed) == p.ClosedAt.IsZero() {
		return ErrInvalidTimestamps
	}
	return nil
}

func buildLines(states []LineState) ([]Line, error) {
	if len(states) == 0 {
		return nil, asn.ErrNoLines
	}
	seen := make(map[shared.SKU]struct{}, len(states))
	lines := make([]Line, 0, len(states))
	for i, s := range states {
		if s.LineNo != i+1 {
			return nil, asn.ErrInvalidLineNo
		}
		sku, err := shared.NewSKU(s.SKU)
		if err != nil {
			return nil, err
		}
		if err := shared.ValidateQuantity(s.ExpectedQty); err != nil {
			return nil, err
		}
		if err := validateReceived(s.ReceivedGood, s.ReceivedDamaged); err != nil {
			return nil, err
		}
		if _, dup := seen[sku]; dup {
			return nil, asn.ErrDuplicateSKU
		}
		seen[sku] = struct{}{}
		lines = append(lines, Line{
			lineNo:          s.LineNo,
			sku:             sku,
			expectedQty:     s.ExpectedQty,
			receivedGood:    s.ReceivedGood,
			receivedDamaged: s.ReceivedDamaged,
		})
	}
	return lines, nil
}

func validateReceived(good, damaged int64) error {
	if good < 0 || damaged < 0 {
		return ErrInvalidReceived
	}
	if good > shared.MaxQuantity || damaged > shared.MaxQuantity || good+damaged > shared.MaxQuantity {
		return ErrInvalidReceived
	}
	return nil
}
