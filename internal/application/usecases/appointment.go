package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// BookAppointmentCommand is the input of BookAppointment.
type BookAppointmentCommand struct {
	DoorCode    string
	Carrier     string
	WindowStart time.Time
	WindowEnd   time.Time
	AsnNumbers  []string
}

// BookAppointment books a door window for a carrier. With DoorMode kafka
// the door must be a known inbound dock door (ADR 0003). The overlap check
// and the save run in one unit of work holding the door's lock, so two
// concurrent bookings of one door cannot both pass.
type BookAppointment struct {
	Writer
	DoorMode Mode
	Doors    ports.DockDoorDirectory
}

// Handle runs the use case.
func (uc *BookAppointment) Handle(ctx context.Context, cmd BookAppointmentCommand) (*appointment.DockAppointment, error) {
	d, events, err := appointment.Book(uc.IDs.NewAppointmentID(), cmd.DoorCode, cmd.Carrier, cmd.WindowStart, cmd.WindowEnd, cmd.AsnNumbers, uc.now())
	if err != nil {
		return nil, err
	}
	if err := uc.checkDoor(ctx, d.DoorCode()); err != nil {
		return nil, err
	}
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		if err := uc.Appointments.LockDoor(ctx, d.DoorCode()); err != nil {
			return err
		}
		for _, number := range d.AsnNumbers() {
			if err := uc.requireBookable(ctx, number); err != nil {
				return err
			}
		}
		active, err := uc.Appointments.ActiveOnDoor(ctx, d.DoorCode())
		if err != nil {
			return err
		}
		if err := (appointment.Schedule{}).CheckNoOverlap(d, active); err != nil {
			return err
		}
		return uc.saveAppointment(ctx, d, 0, events)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (uc *BookAppointment) checkDoor(ctx context.Context, door appointment.DoorCode) error {
	if uc.DoorMode != ModeKafka {
		return nil
	}
	ok, err := uc.Doors.Exists(ctx, door)
	if err != nil {
		return err
	}
	if !ok {
		return unknownDoor(string(door))
	}
	return nil
}

// requireBookable checks that a covered ASN exists and can still be
// received (Registered or Receiving).
func (uc *BookAppointment) requireBookable(ctx context.Context, number asn.Number) error {
	a, err := uc.Asns.Get(ctx, number)
	if errors.Is(err, repository.ErrAsnNotFound) {
		return unknownAsn(string(number))
	}
	if err != nil {
		return err
	}
	if !a.State().Receivable() {
		return receipt.ErrAsnNotReceivable
	}
	return nil
}

// AppointmentActionCommand is the input of CheckInAppointment and
// CancelAppointment. Reason is used by cancel only.
type AppointmentActionCommand struct {
	AppointmentID   string
	Reason          string
	ExpectedVersion int64
}

// CheckInAppointment records the carrier's arrival (server clock).
type CheckInAppointment struct {
	Writer
}

// Handle runs the use case.
func (uc *CheckInAppointment) Handle(ctx context.Context, cmd AppointmentActionCommand) (*appointment.DockAppointment, error) {
	return uc.act(ctx, cmd, func(d *appointment.DockAppointment, now time.Time) ([]appointment.Event, error) {
		return d.CheckIn(now)
	})
}

// CancelAppointment cancels a Booked appointment.
type CancelAppointment struct {
	Writer
}

// Handle runs the use case.
func (uc *CancelAppointment) Handle(ctx context.Context, cmd AppointmentActionCommand) (*appointment.DockAppointment, error) {
	return uc.act(ctx, cmd, func(d *appointment.DockAppointment, now time.Time) ([]appointment.Event, error) {
		return d.Cancel(cmd.Reason, now)
	})
}

// act is the shared "load, version check, apply, save, enqueue" of the two
// appointment actions.
func (w Writer) act(ctx context.Context, cmd AppointmentActionCommand, apply func(*appointment.DockAppointment, time.Time) ([]appointment.Event, error)) (*appointment.DockAppointment, error) {
	id, err := appointment.NewID(cmd.AppointmentID)
	if err != nil {
		return nil, err
	}
	var out *appointment.DockAppointment
	err = w.UoW.Do(ctx, func(ctx context.Context) error {
		d, err := w.Appointments.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := d.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		events, err := apply(d, w.now())
		if err != nil {
			return err
		}
		if err := w.saveAppointment(ctx, d, loaded, events); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
