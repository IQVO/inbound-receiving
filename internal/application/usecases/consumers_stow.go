package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// Consumer names namespace the claims of the two dock-to-stock consumers in
// ports.ProcessedEvents (ADR 0021).
const (
	VisitArrivalConsumer = "visit-arrival-copy"
	CartonStowConsumer   = "carton-stow-copy"
)

// VisitCheckedIn is the part of yard-management's VisitCheckedIn payload the
// arrival copy needs. AppointmentID is "" for a walk-in visit.
type VisitCheckedIn struct {
	VisitID       string
	AppointmentID string
	ArrivedAt     time.Time
	Version       int64
}

// ContainerStowed is the part of inventory-storage's ContainerStowed payload
// the stow copy needs: the license plate and when it was stowed (`at`).
type ContainerStowed struct {
	LPN      string
	StowedAt time.Time
}

// ApplyVisitCheckedIn feeds the visit_arrivals local copy from yard-
// management's VisitCheckedIn and evaluates the dock-to-stock fact of the
// receipts opened from that appointment (ADR 0021). The CloudEvents id claim,
// the upsert, the evaluation and the outbox rows are ONE unit of work. The
// Writer must carry a StowStore.
type ApplyVisitCheckedIn struct {
	Writer
	ProcessedEvents ports.ProcessedEvents
}

// Handle runs the use case. A visit without an appointment is Ignored (a
// walk-in has no arrival fact, nothing is claimed); a payload this context
// would reject is wrapped in ErrInvalidEvent before anything is claimed; an
// arrival that is not earlier than the stored one is Ignored (its claim still
// commits, so a redelivery is a Duplicate). Any other error is transient and
// rolled back, claim included.
func (uc *ApplyVisitCheckedIn) Handle(ctx context.Context, eventID string, e VisitCheckedIn) (Outcome, error) {
	if e.AppointmentID == "" {
		return Ignored, nil
	}
	id, err := appointment.NewID(e.AppointmentID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	switch {
	case e.VisitID == "":
		return "", fmt.Errorf("%w: visit_id is required", ErrInvalidEvent)
	case e.ArrivedAt.IsZero():
		return "", fmt.Errorf("%w: arrived_at is required", ErrInvalidEvent)
	case e.Version < 1:
		return "", fmt.Errorf("%w: version must be at least 1", ErrInvalidEvent)
	}
	return claimApply(ctx, uc.UoW, uc.ProcessedEvents, VisitArrivalConsumer, eventID, func(ctx context.Context) (bool, error) {
		applied, err := uc.Stows.ApplyArrival(ctx, id, e.VisitID, e.ArrivedAt.UTC())
		if err != nil || !applied {
			return false, err
		}
		receipts, err := uc.Stows.ReceiptsOfAppointment(ctx, id)
		if err != nil {
			return false, fmt.Errorf("receipts of appointment %s: %w", id, err)
		}
		return true, uc.completeStow(ctx, receipts...)
	})
}

// ApplyContainerStowed feeds the carton_stows local copy from inventory-
// storage's ContainerStowed and evaluates the dock-to-stock fact of the
// receipts that hold the container's license plate, with the same rules as
// ApplyVisitCheckedIn.
type ApplyContainerStowed struct {
	Writer
	ProcessedEvents ports.ProcessedEvents
}

// Handle runs the use case. An empty license plate or a missing stow time is
// ErrInvalidEvent; a stow that is not earlier than the stored one (a move of
// the container to another bin, a redelivery under a new id) is Ignored.
func (uc *ApplyContainerStowed) Handle(ctx context.Context, eventID string, e ContainerStowed) (Outcome, error) {
	switch {
	case e.LPN == "":
		return "", fmt.Errorf("%w: lpn is required", ErrInvalidEvent)
	case e.StowedAt.IsZero():
		return "", fmt.Errorf("%w: at is required", ErrInvalidEvent)
	}
	return claimApply(ctx, uc.UoW, uc.ProcessedEvents, CartonStowConsumer, eventID, func(ctx context.Context) (bool, error) {
		applied, err := uc.Stows.ApplyCartonStow(ctx, e.LPN, e.StowedAt.UTC())
		if err != nil || !applied {
			return false, err
		}
		receipts, err := uc.Stows.ReceiptsOfCarton(ctx, e.LPN)
		if err != nil {
			return false, fmt.Errorf("receipts of carton %s: %w", e.LPN, err)
		}
		return true, uc.completeStow(ctx, receipts...)
	})
}
