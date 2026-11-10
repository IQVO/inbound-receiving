package usecases

import (
	"context"
	"errors"
	"strings"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// OpenReceiptCommand is the input of OpenReceipt. AppointmentID is empty
// for a walk-in delivery.
type OpenReceiptCommand struct {
	AsnNumber     string
	AppointmentID string
}

// OpenReceipt starts receiving an ASN: it opens a receipt against a snapshot
// of the ASN's lines, moves the ASN to Receiving and, with an appointment,
// takes the door from it. The appointment is validated against the local
// copy of dock-scheduling's appointments (ADR 0019): it must exist, cover the
// ASN and not be cancelled. One open receipt per ASN is enforced here and by
// a partial unique index.
type OpenReceipt struct {
	Writer
}

// Handle runs the use case.
func (uc *OpenReceipt) Handle(ctx context.Context, cmd OpenReceiptCommand) (*receipt.Receipt, error) {
	number, err := asn.NewNumber(cmd.AsnNumber)
	if err != nil {
		return nil, err
	}
	var apptID appointment.ID
	if cmd.AppointmentID != "" {
		if apptID, err = appointment.NewID(cmd.AppointmentID); err != nil {
			return nil, err
		}
	}
	var out *receipt.Receipt
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.open(ctx, number, apptID)
		out = r
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// open is the body of the unit of work: load, validate, open, save.
func (uc *OpenReceipt) open(ctx context.Context, number asn.Number, apptID appointment.ID) (*receipt.Receipt, error) {
	a, err := uc.Asns.Get(ctx, number)
	if errors.Is(err, repository.ErrAsnNotFound) {
		return nil, unknownAsn(string(number))
	}
	if err != nil {
		return nil, err
	}
	door, err := uc.checkAppointment(ctx, apptID, number)
	if err != nil {
		return nil, err
	}
	r, events, err := receipt.Open(uc.IDs.NewReceiptID(), a.Snapshot(), string(apptID), string(door), uc.now())
	if err != nil {
		return nil, err
	}
	if err := uc.requireNoOpenReceipt(ctx, number); err != nil {
		return nil, err
	}
	if err := uc.beginReceiving(ctx, a); err != nil {
		return nil, err
	}
	if err := uc.saveReceipt(ctx, r, 0, events); err != nil {
		return nil, err
	}
	return r, nil
}

// beginReceiving moves the ASN to Receiving and saves it; an ASN already
// Receiving is left as it is.
func (uc *OpenReceipt) beginReceiving(ctx context.Context, a *asn.Asn) error {
	loaded := a.Version()
	if err := a.BeginReceiving(); err != nil {
		return err
	}
	if a.Version() == loaded {
		return nil
	}
	return uc.saveAsn(ctx, a, loaded, nil)
}

// checkAppointment validates the optional appointment against the local
// copy and returns its door ("" without one). It is not required to be
// checked in: the arrival is a yard-management fact this context does not
// see (ADR 0019).
func (uc *OpenReceipt) checkAppointment(ctx context.Context, id appointment.ID, number asn.Number) (appointment.DoorCode, error) {
	if id == "" {
		return "", nil
	}
	d, err := uc.Appointments.Get(ctx, id)
	if errors.Is(err, repository.ErrAppointmentNotFound) {
		return "", unknownAppointment(string(id))
	}
	if err != nil {
		return "", err
	}
	if !d.Covers(number) {
		return "", ErrAsnNotOnAppointment
	}
	if d.State() == appointment.Cancelled {
		return "", ErrAppointmentCancelled
	}
	return d.DoorCode(), nil
}

func (uc *OpenReceipt) requireNoOpenReceipt(ctx context.Context, number asn.Number) error {
	_, err := uc.Receipts.OpenByAsn(ctx, number)
	if err == nil {
		return repository.ErrReceiptAlreadyOpen
	}
	if errors.Is(err, repository.ErrReceiptNotFound) {
		return nil
	}
	return err
}

// ReceiveLineCommand is the input of ReceiveLine.
type ReceiveLineCommand struct {
	ReceiptID       string
	LineNo          int
	Quantity        int64
	Condition       string
	ExpectedVersion int64
}

// ReceiveLine records a quantity received against a receipt line. It is
// not naturally idempotent: the HTTP adapter requires an Idempotency-Key.
type ReceiveLine struct {
	Writer
	metrics ports.ReceiptMetrics
}

// WithMetrics attaches the business-metric port (ADR-0013 Tier 2). Optional:
// a nil or never-called WithMetrics means "record nothing".
func (uc *ReceiveLine) WithMetrics(m ports.ReceiptMetrics) *ReceiveLine {
	uc.metrics = m
	return uc
}

// Handle runs the use case and returns the receipt after the change.
func (uc *ReceiveLine) Handle(ctx context.Context, cmd ReceiveLineCommand) (*receipt.Receipt, error) {
	id, err := receipt.NewID(cmd.ReceiptID)
	if err != nil {
		return nil, err
	}
	condition, err := receipt.ParseCondition(cmd.Condition)
	if err != nil {
		return nil, err
	}
	var out *receipt.Receipt
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.Receipts.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := r.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		events, err := r.ReceiveLine(cmd.LineNo, cmd.Quantity, condition, uc.now())
		if err != nil {
			return err
		}
		if err := uc.saveReceipt(ctx, r, loaded, events); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Recorded here, once the unit of work has committed, so the metric
	// tracks units actually accepted onto the receipt -- the real domain
	// event -- not merely the request.
	if uc.metrics != nil {
		uc.metrics.LineReceived(ctx, strings.ToLower(string(condition)), cmd.Quantity)
	}
	return out, nil
}

// CloseReceiptCommand is the input of CloseReceipt.
type CloseReceiptCommand struct {
	ReceiptID       string
	ExpectedVersion int64
}

// CloseReceipt ends receiving. The receipt close and the ASN completion are
// ONE unit of work, so their events reach the outbox together. It no longer
// completes the appointment: the door release recorded by yard-management
// does, through dock-scheduling (ADR 0019).
type CloseReceipt struct {
	Writer
}

// Handle runs the use case and returns the closed receipt.
func (uc *CloseReceipt) Handle(ctx context.Context, cmd CloseReceiptCommand) (*receipt.Receipt, error) {
	id, err := receipt.NewID(cmd.ReceiptID)
	if err != nil {
		return nil, err
	}
	var out *receipt.Receipt
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.Receipts.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := r.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		now := uc.now()
		events, err := r.Close(now)
		if err != nil {
			return err
		}
		if err := uc.completeAsn(ctx, r.AsnNumber()); err != nil {
			return err
		}
		if err := uc.saveReceipt(ctx, r, loaded, events); err != nil {
			return err
		}
		// Stows and the arrival may have been seen while the receipt was
		// open: evaluate now that it is closed (ADR 0021).
		if err := uc.completeStow(ctx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (uc *CloseReceipt) completeAsn(ctx context.Context, number asn.Number) error {
	a, err := uc.Asns.Get(ctx, number)
	if err != nil {
		return err
	}
	loaded := a.Version()
	if err := a.Complete(); err != nil {
		return err
	}
	return uc.saveAsn(ctx, a, loaded, nil)
}
