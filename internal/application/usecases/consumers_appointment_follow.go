package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// DockAppointmentRescheduled is the part of dock-scheduling's
// DockAppointmentRescheduled payload the local copy needs (ADR 0020): the new
// door and window at the new version.
type DockAppointmentRescheduled struct {
	AppointmentID string
	DoorCode      string
	WindowStart   time.Time
	WindowEnd     time.Time
	Version       int64
}

// DockAppointmentDoorReassigned is the part of dock-scheduling's
// DockAppointmentDoorReassigned payload the local copy needs (ADR 0020).
// FromDoorCode is informational: the copy is moved to ToDoorCode whatever it
// held.
type DockAppointmentDoorReassigned struct {
	AppointmentID string
	FromDoorCode  string
	ToDoorCode    string
	Version       int64
}

// ApplyDockAppointmentRescheduled moves a stored appointment to the new door
// and window from dock-scheduling's DockAppointmentRescheduled. Like its
// siblings it claims the CloudEvents id in the same unit of work as the
// update, raises NO event and touches no outbox (ADR 0019, 0020). Receipts
// already open keep their door; only a receipt opened afterwards reads the
// new one.
type ApplyDockAppointmentRescheduled struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Appointments    ports.AppointmentStore
}

// Handle runs the use case. A payload this context would reject is wrapped
// in ErrInvalidEvent before anything is claimed. An appointment that is not
// in the copy (an OUTBOUND one, or one not seen yet) and a version that is
// not newer are Ignored, and their claim still commits. A cancelled
// appointment stays cancelled. Any other error is transient and rolled back,
// claim included.
func (uc *ApplyDockAppointmentRescheduled) Handle(ctx context.Context, eventID string, e DockAppointmentRescheduled) (Outcome, error) {
	id, err := followedID(e.AppointmentID, e.Version)
	if err != nil {
		return "", err
	}
	door, err := appointment.NewDoorCode(e.DoorCode)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	window, err := appointment.NewWindow(e.WindowStart, e.WindowEnd)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	return claimAndApply(ctx, uc.UoW, uc.ProcessedEvents, eventID, func(ctx context.Context) (bool, error) {
		return uc.Appointments.ApplyRescheduled(ctx, id, door, window, e.Version)
	})
}

// ApplyDockAppointmentDoorReassigned moves a stored appointment to another
// door from dock-scheduling's DockAppointmentDoorReassigned, with the same
// rules as ApplyDockAppointmentRescheduled. The type may not be published on
// dock-scheduling's integration topic yet: nothing depends on it arriving.
type ApplyDockAppointmentDoorReassigned struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Appointments    ports.AppointmentStore
}

// Handle runs the use case; see ApplyDockAppointmentRescheduled.Handle.
func (uc *ApplyDockAppointmentDoorReassigned) Handle(ctx context.Context, eventID string, e DockAppointmentDoorReassigned) (Outcome, error) {
	id, err := followedID(e.AppointmentID, e.Version)
	if err != nil {
		return "", err
	}
	door, err := appointment.NewDoorCode(e.ToDoorCode)
	if err != nil {
		return "", fmt.Errorf("%w: to_door_code: %w", ErrInvalidEvent, err)
	}
	return claimAndApply(ctx, uc.UoW, uc.ProcessedEvents, eventID, func(ctx context.Context) (bool, error) {
		return uc.Appointments.ApplyDoorReassigned(ctx, id, door, e.Version)
	})
}

// followedID validates the appointment id and version every followed event
// carries.
func followedID(raw string, version int64) (appointment.ID, error) {
	id, err := appointment.NewID(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if version < 1 {
		return "", fmt.Errorf("%w: version must be at least 1", ErrInvalidEvent)
	}
	return id, nil
}

// claimAndApply claims eventID for the dock-appointment consumer and runs
// apply in ONE unit of work: Duplicate when the id was already claimed,
// Ignored when apply changed nothing, Applied otherwise.
func claimAndApply(ctx context.Context, uow ports.UnitOfWork, processed ports.ProcessedEvents, eventID string, apply func(context.Context) (bool, error)) (Outcome, error) {
	return claimApply(ctx, uow, processed, DockAppointmentConsumer, eventID, apply)
}

// claimApply claims eventID in the consumer's namespace and runs apply in ONE
// unit of work: Duplicate when the id was already claimed, Ignored when apply
// changed nothing, Applied otherwise.
func claimApply(ctx context.Context, uow ports.UnitOfWork, processed ports.ProcessedEvents, consumer, eventID string, apply func(context.Context) (bool, error)) (Outcome, error) {
	outcome := Applied
	err := uow.Do(ctx, func(ctx context.Context) error {
		claimed, err := processed.Claim(ctx, consumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = Duplicate
			return nil
		}
		applied, err := apply(ctx)
		if err != nil {
			return err
		}
		if !applied {
			outcome = Ignored
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}
