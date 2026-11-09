package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// The copy follows dock-scheduling's reschedules and door reassignments
// (ADR 0020): same claim, version guard and no-loop rules as Booked/Cancelled.

func rescheduledConsumer(w *world) *usecases.ApplyDockAppointmentRescheduled {
	return &usecases.ApplyDockAppointmentRescheduled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appointments}
}

func doorReassignedConsumer(w *world) *usecases.ApplyDockAppointmentDoorReassigned {
	return &usecases.ApplyDockAppointmentDoorReassigned{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appointments}
}

var rescheduledStart = time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)

func dsRescheduled(door string, version int64) usecases.DockAppointmentRescheduled {
	return usecases.DockAppointmentRescheduled{
		AppointmentID: dsAppt, DoorCode: door, WindowStart: rescheduledStart, WindowEnd: rescheduledStart.Add(90 * time.Minute), Version: version,
	}
}

func dsDoorReassigned(to string, version int64) usecases.DockAppointmentDoorReassigned {
	return usecases.DockAppointmentDoorReassigned{AppointmentID: dsAppt, FromDoorCode: "DOOR-1", ToDoorCode: to, Version: version}
}

// bookedV1 stores dsBooked(1): DOOR-1, 2026-11-06 09:00-10:00, ASN-1.
func bookedV1(t *testing.T, w *world) {
	t.Helper()
	if got, err := bookedConsumer(w).Handle(context.Background(), "evt-book", dsBooked(1)); err != nil || got != usecases.Applied {
		t.Fatalf("setup booked = %s, %v", got, err)
	}
}

func storedAppt(t *testing.T, w *world) *appointment.DockAppointment {
	t.Helper()
	d, err := w.appointments.Get(context.Background(), dsAppt)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDockAppointmentRescheduledMovesDoorAndWindowAndRaisesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	uc := rescheduledConsumer(w)

	got, err := uc.Handle(ctx, "evt-r1", dsRescheduled("DOOR-5", 2))
	wantNoErr(t, err)
	if got != usecases.Applied {
		t.Fatalf("outcome = %s, want applied", got)
	}
	d := storedAppt(t, w)
	if d.DoorCode() != "DOOR-5" || d.Version() != 2 || !d.Window().Start().Equal(rescheduledStart) ||
		!d.Window().End().Equal(rescheduledStart.Add(90*time.Minute)) {
		t.Fatalf("stored = %s v%d %v-%v", d.DoorCode(), d.Version(), d.Window().Start(), d.Window().End())
	}
	// Carrier, ASN coverage and state are not part of a reschedule.
	if d.Carrier() != "ACME Freight" || !d.Covers("ASN-1") || d.State() != appointment.Booked {
		t.Fatalf("untouched fields changed: %s %v %s", d.Carrier(), d.AsnNumbers(), d.State())
	}
	if !w.processed.seen[usecases.DockAppointmentConsumer+"/evt-r1"] {
		t.Fatalf("claim missing: %v", w.processed.seen)
	}
	// Redelivery of the same CloudEvents id is a duplicate.
	if got, _ := uc.Handle(ctx, "evt-r1", dsRescheduled("DOOR-5", 2)); got != usecases.Duplicate {
		t.Fatalf("redelivery = %s, want duplicate", got)
	}
	// NO-LOOP (ADR 0019, 0020).
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("outbox holds %d messages, want none", len(w.outbox.msgs))
	}
}

func TestDockAppointmentRescheduledIsVersionGuarded(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	uc := rescheduledConsumer(w)
	if got, _ := uc.Handle(ctx, "evt-r1", dsRescheduled("DOOR-3", 3)); got != usecases.Applied {
		t.Fatalf("v3 = %s, want applied", got)
	}
	// Equal and older versions are ignored, but their ids are claimed.
	if got, err := uc.Handle(ctx, "evt-r2", dsRescheduled("DOOR-SAME", 3)); err != nil || got != usecases.Ignored {
		t.Fatalf("equal version = %s, %v, want ignored", got, err)
	}
	if got, err := uc.Handle(ctx, "evt-r3", dsRescheduled("DOOR-OLD", 2)); err != nil || got != usecases.Ignored {
		t.Fatalf("older version = %s, %v, want ignored", got, err)
	}
	if got, _ := uc.Handle(ctx, "evt-r3", dsRescheduled("DOOR-OLD", 2)); got != usecases.Duplicate {
		t.Fatalf("an ignored event must still be claimed: %s", got)
	}
	if d := storedAppt(t, w); d.DoorCode() != "DOOR-3" || d.Version() != 3 {
		t.Fatalf("stored = %s v%d, want DOOR-3 v3", d.DoorCode(), d.Version())
	}
	// The next version applies.
	if got, _ := uc.Handle(ctx, "evt-r4", dsRescheduled("DOOR-4", 4)); got != usecases.Applied {
		t.Fatalf("v4 = %s, want applied", got)
	}
	if d := storedAppt(t, w); d.DoorCode() != "DOOR-4" || d.Version() != 4 {
		t.Fatalf("stored = %s v%d, want DOOR-4 v4", d.DoorCode(), d.Version())
	}
}

func TestDockAppointmentRescheduledOfAnUnknownAppointmentIsIgnoredAndClaimed(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	got, err := rescheduledConsumer(w).Handle(ctx, "evt-r1", dsRescheduled("DOOR-5", 2))
	wantNoErr(t, err)
	if got != usecases.Ignored {
		t.Fatalf("unknown appointment = %s, want ignored", got)
	}
	if len(w.appointments.items) != 0 {
		t.Fatal("a reschedule must never create a row")
	}
	if !w.processed.seen[usecases.DockAppointmentConsumer+"/evt-r1"] {
		t.Fatal("the claim of an ignored event must commit")
	}
}

func TestDockAppointmentRescheduledKeepsACancelledAppointmentCancelled(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	if _, err := cancelledConsumer(w).Handle(ctx, "evt-c", dsAppt, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := rescheduledConsumer(w).Handle(ctx, "evt-r1", dsRescheduled("DOOR-5", 3)); got != usecases.Applied {
		t.Fatalf("outcome = %s, want applied (data refreshed)", got)
	}
	d := storedAppt(t, w)
	if d.State() != appointment.Cancelled || d.DoorCode() != "DOOR-5" || d.Version() != 3 {
		t.Fatalf("stored = %s %s v%d, want Cancelled DOOR-5 v3", d.State(), d.DoorCode(), d.Version())
	}
}

func TestDockAppointmentRescheduledInvalidEvents(t *testing.T) {
	w := newWorld()
	bookedV1(t, w)
	claimsBefore := len(w.processed.seen)
	uc := rescheduledConsumer(w)
	for name, mutate := range map[string]func(*usecases.DockAppointmentRescheduled){
		"empty id":        func(e *usecases.DockAppointmentRescheduled) { e.AppointmentID = "" },
		"bad id":          func(e *usecases.DockAppointmentRescheduled) { e.AppointmentID = "nope" },
		"version zero":    func(e *usecases.DockAppointmentRescheduled) { e.Version = 0 },
		"negative":        func(e *usecases.DockAppointmentRescheduled) { e.Version = -1 },
		"empty door":      func(e *usecases.DockAppointmentRescheduled) { e.DoorCode = "" },
		"empty window":    func(e *usecases.DockAppointmentRescheduled) { e.WindowEnd = e.WindowStart },
		"inverted window": func(e *usecases.DockAppointmentRescheduled) { e.WindowEnd = e.WindowStart.Add(-time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			e := dsRescheduled("DOOR-5", 2)
			mutate(&e)
			_, err := uc.Handle(context.Background(), "evt-x", e)
			wantErr(t, err, usecases.ErrInvalidEvent)
		})
	}
	if len(w.processed.seen) != claimsBefore {
		t.Fatal("an invalid event must not be claimed")
	}
	if d := storedAppt(t, w); d.DoorCode() != "DOOR-1" || d.Version() != 1 {
		t.Fatalf("an invalid event changed the copy: %s v%d", d.DoorCode(), d.Version())
	}
}

func TestDockAppointmentRescheduledAcceptsVersionOne(t *testing.T) {
	// The lower bound of the version rule is 1, inclusive: a legacy row
	// (version 0) is moved by dock-scheduling's very first version.
	ctx := context.Background()
	w := newWorld()
	legacy := w.seedAppointment(appointment.Booked, 0, "DOOR-9", windowAt(w, 1), "ASN-1")
	e := dsRescheduled("DOOR-5", 1)
	e.AppointmentID = string(legacy.ID())
	if got, err := rescheduledConsumer(w).Handle(ctx, "evt-r1", e); err != nil || got != usecases.Applied {
		t.Fatalf("version 1 over a legacy row = %s, %v", got, err)
	}
}

func TestDockAppointmentRescheduledTransientFailuresRollBack(t *testing.T) {
	w := newWorld()
	uc := rescheduledConsumer(w)
	w.processed.err = errors.New("db down")
	_, err := uc.Handle(context.Background(), "evt-1", dsRescheduled("DOOR-5", 2))
	wantErr(t, err, w.processed.err)
	w.processed.err = nil
	w.appointments.applyErr = errors.New("update failed")
	_, err = uc.Handle(context.Background(), "evt-2", dsRescheduled("DOOR-5", 2))
	wantErr(t, err, w.appointments.applyErr)
}

func TestDockAppointmentDoorReassignedMovesOnlyTheDoorAndRaisesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	before := storedAppt(t, w)
	uc := doorReassignedConsumer(w)

	got, err := uc.Handle(ctx, "evt-d1", dsDoorReassigned("DOOR-7", 2))
	wantNoErr(t, err)
	if got != usecases.Applied {
		t.Fatalf("outcome = %s, want applied", got)
	}
	d := storedAppt(t, w)
	if d.DoorCode() != "DOOR-7" || d.Version() != 2 {
		t.Fatalf("stored = %s v%d, want DOOR-7 v2", d.DoorCode(), d.Version())
	}
	if !d.Window().Start().Equal(before.Window().Start()) || !d.Window().End().Equal(before.Window().End()) ||
		d.State() != appointment.Booked || !d.Covers("ASN-1") {
		t.Fatalf("a door reassignment must keep window, state and ASNs: %+v", d)
	}
	if got, _ := uc.Handle(ctx, "evt-d1", dsDoorReassigned("DOOR-7", 2)); got != usecases.Duplicate {
		t.Fatalf("redelivery = %s, want duplicate", got)
	}
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("outbox holds %d messages, want none (no loop)", len(w.outbox.msgs))
	}
}

func TestDockAppointmentDoorReassignedIsVersionGuarded(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	uc := doorReassignedConsumer(w)
	if got, _ := uc.Handle(ctx, "evt-d1", dsDoorReassigned("DOOR-3", 3)); got != usecases.Applied {
		t.Fatalf("v3 = %s, want applied", got)
	}
	if got, err := uc.Handle(ctx, "evt-d2", dsDoorReassigned("DOOR-SAME", 3)); err != nil || got != usecases.Ignored {
		t.Fatalf("equal version = %s, %v, want ignored", got, err)
	}
	if got, err := uc.Handle(ctx, "evt-d3", dsDoorReassigned("DOOR-OLD", 2)); err != nil || got != usecases.Ignored {
		t.Fatalf("older version = %s, %v, want ignored", got, err)
	}
	if d := storedAppt(t, w); d.DoorCode() != "DOOR-3" || d.Version() != 3 {
		t.Fatalf("stored = %s v%d, want DOOR-3 v3", d.DoorCode(), d.Version())
	}
	if got, _ := uc.Handle(ctx, "evt-d4", dsDoorReassigned("DOOR-4", 4)); got != usecases.Applied {
		t.Fatalf("v4 = %s, want applied", got)
	}
	// A reassignment and a reschedule share one version line.
	if got, _ := rescheduledConsumer(w).Handle(ctx, "evt-r5", dsRescheduled("DOOR-R", 4)); got != usecases.Ignored {
		t.Fatalf("a reschedule at the reassignment's version = %s, want ignored", got)
	}
}

func TestDockAppointmentDoorReassignedOfAnUnknownAppointmentIsIgnoredAndClaimed(t *testing.T) {
	w := newWorld()
	got, err := doorReassignedConsumer(w).Handle(context.Background(), "evt-d1", dsDoorReassigned("DOOR-7", 2))
	wantNoErr(t, err)
	if got != usecases.Ignored || len(w.appointments.items) != 0 {
		t.Fatalf("unknown appointment = %s, stored = %d", got, len(w.appointments.items))
	}
	if !w.processed.seen[usecases.DockAppointmentConsumer+"/evt-d1"] {
		t.Fatal("the claim of an ignored event must commit")
	}
}

func TestDockAppointmentDoorReassignedKeepsACancelledAppointmentCancelled(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	bookedV1(t, w)
	if _, err := cancelledConsumer(w).Handle(ctx, "evt-c", dsAppt, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := doorReassignedConsumer(w).Handle(ctx, "evt-d1", dsDoorReassigned("DOOR-7", 3)); got != usecases.Applied {
		t.Fatalf("outcome = %s, want applied", got)
	}
	if d := storedAppt(t, w); d.State() != appointment.Cancelled || d.DoorCode() != "DOOR-7" {
		t.Fatalf("stored = %s %s, want Cancelled DOOR-7", d.State(), d.DoorCode())
	}
}

func TestDockAppointmentDoorReassignedInvalidEvents(t *testing.T) {
	w := newWorld()
	uc := doorReassignedConsumer(w)
	for name, mutate := range map[string]func(*usecases.DockAppointmentDoorReassigned){
		"empty id":     func(e *usecases.DockAppointmentDoorReassigned) { e.AppointmentID = "" },
		"bad id":       func(e *usecases.DockAppointmentDoorReassigned) { e.AppointmentID = "nope" },
		"version zero": func(e *usecases.DockAppointmentDoorReassigned) { e.Version = 0 },
		"empty door":   func(e *usecases.DockAppointmentDoorReassigned) { e.ToDoorCode = "" },
		"bad door":     func(e *usecases.DockAppointmentDoorReassigned) { e.ToDoorCode = "DOOR 7" },
	} {
		t.Run(name, func(t *testing.T) {
			e := dsDoorReassigned("DOOR-7", 2)
			mutate(&e)
			_, err := uc.Handle(context.Background(), "evt-x", e)
			wantErr(t, err, usecases.ErrInvalidEvent)
		})
	}
	if len(w.processed.seen) != 0 {
		t.Fatal("an invalid event must not be claimed")
	}
	// The previous door is informational: an absent from_door_code is valid.
	e := dsDoorReassigned("DOOR-7", 1)
	e.FromDoorCode = ""
	if _, err := uc.Handle(context.Background(), "evt-y", e); err != nil {
		t.Fatalf("absent from_door_code = %v, want accepted", err)
	}
}

func TestDockAppointmentDoorReassignedTransientFailuresRollBack(t *testing.T) {
	w := newWorld()
	uc := doorReassignedConsumer(w)
	w.processed.err = errors.New("db down")
	_, err := uc.Handle(context.Background(), "evt-1", dsDoorReassigned("DOOR-7", 2))
	wantErr(t, err, w.processed.err)
	w.processed.err = nil
	w.appointments.applyErr = errors.New("update failed")
	_, err = uc.Handle(context.Background(), "evt-2", dsDoorReassigned("DOOR-7", 2))
	wantErr(t, err, w.appointments.applyErr)
}

// Receipts already open keep their door; the next receipt takes the new one
// (ADR 0020 point 4).
func TestAReceiptOpenedBeforeAReassignmentKeepsItsDoor(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	w.registerAsn("ASN-2", "SKU-1")
	appt := w.book("DOOR-1", windowAt(w, 1), "ASN-1", "ASN-2")
	open := &usecases.OpenReceipt{Writer: w.writer}
	first, err := open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: string(appt.ID())})
	wantNoErr(t, err)

	e := dsDoorReassigned("DOOR-7", 2)
	e.AppointmentID = string(appt.ID())
	if got, err := doorReassignedConsumer(w).Handle(ctx, "evt-d1", e); err != nil || got != usecases.Applied {
		t.Fatalf("reassign = %s, %v", got, err)
	}
	stillFirst, err := w.receipts.Get(ctx, first.ID())
	wantNoErr(t, err)
	if stillFirst.DoorCode() != "DOOR-1" {
		t.Fatalf("open receipt door = %s, want DOOR-1 (a physical fact)", stillFirst.DoorCode())
	}
	second, err := open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2", AppointmentID: string(appt.ID())})
	wantNoErr(t, err)
	if second.DoorCode() != "DOOR-7" {
		t.Fatalf("next receipt door = %s, want DOOR-7", second.DoorCode())
	}
}
