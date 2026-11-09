package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// DockAppointmentConsumer namespaces the claims of the dock-appointment
// consumer in ports.ProcessedEvents (ADR 0019).
const DockAppointmentConsumer = "dock-appointment-copy"

// The direction values of dock-scheduling's DockAppointmentBooked. Only an
// INBOUND appointment can carry a receipt, so only those are copied.
const (
	directionInbound  = "INBOUND"
	directionOutbound = "OUTBOUND"
)

// DockAppointmentBooked is the part of dock-scheduling's DockAppointmentBooked
// payload the local copy needs (contracts-delta of plan YD-0002).
type DockAppointmentBooked struct {
	AppointmentID string
	Direction     string
	DoorCode      string
	Carrier       string
	WindowStart   time.Time
	WindowEnd     time.Time
	AsnNumbers    []string
	Version       int64
}

// ApplyDockAppointmentBooked feeds the appointments local copy from
// dock-scheduling's DockAppointmentBooked. The CloudEvents id claim and the
// upsert run in ONE unit of work. It raises NO event and touches no outbox,
// so the two contexts cannot loop (ADR 0019).
type ApplyDockAppointmentBooked struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Appointments    ports.AppointmentStore
}

// Handle runs the use case. An OUTBOUND appointment is Ignored (nothing is
// received against it); a payload this context would reject is wrapped in
// ErrInvalidEvent before anything is claimed; an event that is not newer than
// the stored version is Ignored (its claim still commits, so a redelivery is
// a Duplicate). Any other error is transient and rolled back, claim included.
func (uc *ApplyDockAppointmentBooked) Handle(ctx context.Context, eventID string, e DockAppointmentBooked) (Outcome, error) {
	switch e.Direction {
	case directionOutbound:
		return Ignored, nil
	case directionInbound:
	default:
		return "", fmt.Errorf("%w: direction must be INBOUND or OUTBOUND, got %q", ErrInvalidEvent, e.Direction)
	}
	if e.Version < 1 {
		return "", fmt.Errorf("%w: version must be at least 1", ErrInvalidEvent)
	}
	d, err := appointment.Rehydrate(e.AppointmentID, e.DoorCode, e.Carrier, e.WindowStart, e.WindowEnd, e.AsnNumbers, appointment.Booked, e.Version)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	return claimAndApply(ctx, uc.UoW, uc.ProcessedEvents, eventID, func(ctx context.Context) (bool, error) {
		return uc.Appointments.ApplyBooked(ctx, d)
	})
}

// ApplyDockAppointmentCancelled marks a stored appointment Cancelled from
// dock-scheduling's DockAppointmentCancelled. Like its sibling it raises no
// event.
type ApplyDockAppointmentCancelled struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Appointments    ports.AppointmentStore
}

// Handle runs the use case. An appointment this context never stored (an
// OUTBOUND one, or a Booked that was invalid) and a version that is not newer
// are Ignored.
func (uc *ApplyDockAppointmentCancelled) Handle(ctx context.Context, eventID, rawAppointmentID string, version int64) (Outcome, error) {
	id, err := followedID(rawAppointmentID, version)
	if err != nil {
		return "", err
	}
	return claimAndApply(ctx, uc.UoW, uc.ProcessedEvents, eventID, func(ctx context.Context) (bool, error) {
		return uc.Appointments.ApplyCancelled(ctx, id, version)
	})
}
