package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func TestBookAppointmentLocksTheDoorAndEnqueuesBooked(t *testing.T) {
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-1", windowAt(w, 24), "ASN-1")
	if d.State() != appointment.Booked || d.Version() != 1 {
		t.Fatalf("appointment = %s v%d", d.State(), d.Version())
	}
	if !reflect.DeepEqual(w.appointments.locks, []appointment.DoorCode{"DOOR-1"}) {
		t.Fatalf("locks = %v", w.appointments.locks)
	}
	got := w.eventTypes()
	if got[len(got)-1] != "dockappointment.DockAppointmentBooked" {
		t.Fatalf("events = %v", got)
	}
}

func TestBookAppointmentRefusesAnOverlapOnTheSameDoor(t *testing.T) {
	w := newWorld()
	start := windowAt(w, 24)
	w.registerAsn("ASN-1", "SKU-1")
	w.registerAsn("ASN-2", "SKU-2")
	w.book("DOOR-1", start, "ASN-1")
	_, err := (&usecases.BookAppointment{Writer: w.writer}).Handle(context.Background(), usecases.BookAppointmentCommand{
		DoorCode: "DOOR-1", Carrier: "C", WindowStart: start.Add(time.Hour), WindowEnd: start.Add(3 * time.Hour), AsnNumbers: []string{"ASN-2"},
	})
	wantErr(t, err, appointment.ErrWindowOverlap)
	// another door and an adjacent window are fine
	w.book("DOOR-2", start, "ASN-2")
	w.book("DOOR-1", start.Add(2*time.Hour), "ASN-2")
}

func TestBookAppointmentCancelledAppointmentFreesItsWindow(t *testing.T) {
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-1", windowAt(w, 24), "ASN-1")
	_, err := (&usecases.CancelAppointment{Writer: w.writer}).Handle(context.Background(), usecases.AppointmentActionCommand{AppointmentID: string(d.ID())})
	wantNoErr(t, err)
	w.book("DOOR-1", windowAt(w, 24), "ASN-1")
}

func TestBookAppointmentAsnMustExistAndBeReceivable(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	_, err := (&usecases.CancelAsn{Writer: w.writer}).Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1"})
	wantNoErr(t, err)
	uc := &usecases.BookAppointment{Writer: w.writer}
	start := windowAt(w, 24)
	cmd := usecases.BookAppointmentCommand{DoorCode: "DOOR-1", Carrier: "C", WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{"ASN-1"}}
	_, err = uc.Handle(ctx, cmd)
	wantErr(t, err, receipt.ErrAsnNotReceivable)
	cmd.AsnNumbers = []string{"ASN-404"}
	_, err = uc.Handle(ctx, cmd)
	wantErr(t, err, usecases.ErrUnknownAsn)
	w.asns.getErr = errors.New("db down")
	_, err = uc.Handle(ctx, cmd)
	wantErr(t, err, w.asns.getErr)
}

func TestBookAppointmentKafkaModeRequiresAKnownInboundDoor(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	w.doors.doors["DOOR-1"] = repository.DockDoor{Code: "DOOR-1", Flow: repository.DockFlowInbound}
	uc := &usecases.BookAppointment{Writer: w.writer, DoorMode: usecases.ModeKafka, Doors: w.doors}
	start := windowAt(w, 24)
	cmd := usecases.BookAppointmentCommand{DoorCode: "DOOR-2", Carrier: "C", WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{"ASN-1"}}
	_, err := uc.Handle(ctx, cmd)
	wantErr(t, err, usecases.ErrUnknownDockDoor)
	cmd.DoorCode = "DOOR-1"
	_, err = uc.Handle(ctx, cmd)
	wantNoErr(t, err)
	w.doors.err = errors.New("db down")
	_, err = uc.Handle(ctx, cmd)
	wantErr(t, err, w.doors.err)
}

func TestBookAppointmentDomainValidationPassesThrough(t *testing.T) {
	w := newWorld()
	uc := &usecases.BookAppointment{Writer: w.writer}
	_, err := uc.Handle(context.Background(), usecases.BookAppointmentCommand{DoorCode: "D", Carrier: "C", WindowStart: windowAt(w, -3), WindowEnd: windowAt(w, -2), AsnNumbers: []string{"A"}})
	wantErr(t, err, appointment.ErrWindowInPast)
	_, err = uc.Handle(context.Background(), usecases.BookAppointmentCommand{DoorCode: "D", Carrier: "C", WindowStart: windowAt(w, 1), WindowEnd: windowAt(w, 9), AsnNumbers: []string{"A"}})
	wantErr(t, err, appointment.ErrInvalidWindow)
}

func TestAppointmentActionsRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-1", windowAt(w, 1), "ASN-1")
	checkIn := &usecases.CheckInAppointment{Writer: w.writer}
	id := string(d.ID())

	_, err := checkIn.Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: "bad"})
	wantErr(t, err, appointment.ErrInvalidID)
	_, err = checkIn.Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: "appt-00000000-0000-4000-8000-0000000000ff"})
	wantErr(t, err, repository.ErrAppointmentNotFound)
	_, err = checkIn.Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: id, ExpectedVersion: 5})
	wantErr(t, err, usecases.ErrVersionMismatch)
	// an hour before the window opens is outside the 30-minute lead time
	_, err = checkIn.Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: id})
	wantErr(t, err, appointment.ErrOutsideCheckInWindow)
}

func TestAppointmentCheckInThenCancelRefused(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-1", windowAt(w, 1), "ASN-1")
	id := string(d.ID())
	w.clock.now = windowAt(w, 1)

	got, err := (&usecases.CheckInAppointment{Writer: w.writer}).Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: id, ExpectedVersion: 1})
	wantNoErr(t, err)
	if got.State() != appointment.CheckedIn || got.Version() != 2 {
		t.Fatalf("appointment = %s v%d", got.State(), got.Version())
	}
	_, err = (&usecases.CancelAppointment{Writer: w.writer}).Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: id})
	wantErr(t, err, appointment.ErrNotBooked)
	types := w.eventTypes()
	if types[len(types)-1] != "dockappointment.DockAppointmentCheckedIn" {
		t.Fatalf("events = %v", types)
	}
}

func TestAppointmentCancelBooked(t *testing.T) {
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-2", windowAt(w, 5), "ASN-1")
	c, err := (&usecases.CancelAppointment{Writer: w.writer}).Handle(context.Background(), usecases.AppointmentActionCommand{AppointmentID: string(d.ID()), Reason: "delayed"})
	wantNoErr(t, err)
	if c.State() != appointment.Cancelled {
		t.Fatalf("state = %s", c.State())
	}
}
