package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

const dsAppt = "appt-aaaaaaaa-0000-4000-8000-000000000001"

func bookedConsumer(w *world) *usecases.ApplyDockAppointmentBooked {
	return &usecases.ApplyDockAppointmentBooked{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appointments}
}

func cancelledConsumer(w *world) *usecases.ApplyDockAppointmentCancelled {
	return &usecases.ApplyDockAppointmentCancelled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appointments}
}

func dsBooked(version int64) usecases.DockAppointmentBooked {
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	return usecases.DockAppointmentBooked{
		AppointmentID: dsAppt, Direction: "INBOUND", DoorCode: "DOOR-1", Carrier: "ACME Freight",
		WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{"ASN-1"}, Version: version,
	}
}

func TestDockAppointmentBookedInsertsOnceAndRaisesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := bookedConsumer(w)
	got, err := uc.Handle(ctx, "evt-1", dsBooked(1))
	wantNoErr(t, err)
	if got != usecases.Applied {
		t.Fatalf("outcome = %s", got)
	}
	stored, err := w.appointments.Get(ctx, dsAppt)
	wantNoErr(t, err)
	if stored.State() != appointment.Booked || stored.Version() != 1 || stored.DoorCode() != "DOOR-1" || !stored.Covers("ASN-1") {
		t.Fatalf("stored = %s v%d %s", stored.State(), stored.Version(), stored.DoorCode())
	}
	got, err = uc.Handle(ctx, "evt-1", dsBooked(1))
	wantNoErr(t, err)
	if got != usecases.Duplicate {
		t.Fatalf("redelivery outcome = %s, want duplicate", got)
	}
	// NO-LOOP (ADR 0019): applying a sibling's event never reaches the outbox.
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("outbox holds %d messages, want none", len(w.outbox.msgs))
	}
}

func TestDockAppointmentBookedIsVersionGuarded(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := bookedConsumer(w)
	_, err := uc.Handle(ctx, "evt-1", dsBooked(3))
	wantNoErr(t, err)

	older := dsBooked(2)
	older.DoorCode = "DOOR-OLD"
	got, err := uc.Handle(ctx, "evt-2", older)
	wantNoErr(t, err)
	if got != usecases.Ignored {
		t.Fatalf("older version outcome = %s, want ignored", got)
	}
	same := dsBooked(3)
	same.DoorCode = "DOOR-SAME"
	if got, _ = uc.Handle(ctx, "evt-3", same); got != usecases.Ignored {
		t.Fatalf("equal version outcome = %s, want ignored", got)
	}
	newer := dsBooked(4)
	newer.DoorCode = "DOOR-NEW"
	newer.AsnNumbers = []string{"ASN-1", "ASN-2"}
	if got, _ = uc.Handle(ctx, "evt-4", newer); got != usecases.Applied {
		t.Fatalf("newer version outcome = %s, want applied", got)
	}
	stored, _ := w.appointments.Get(ctx, dsAppt)
	if stored.DoorCode() != "DOOR-NEW" || stored.Version() != 4 || !stored.Covers("ASN-2") {
		t.Fatalf("stored = %s v%d", stored.DoorCode(), stored.Version())
	}
}

func TestDockAppointmentBookedWinsOverALegacyRowButKeepsItsState(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	legacy := w.seedAppointment(appointment.CheckedIn, 0, "DOOR-9", windowAt(w, 1), "ASN-1")

	e := dsBooked(1)
	e.AppointmentID = string(legacy.ID())
	got, err := bookedConsumer(w).Handle(ctx, "evt-1", e)
	wantNoErr(t, err)
	if got != usecases.Applied {
		t.Fatalf("outcome = %s: any dock-scheduling version must win over a legacy version 0", got)
	}
	stored, _ := w.appointments.Get(ctx, legacy.ID())
	if stored.Version() != 1 || stored.DoorCode() != "DOOR-1" {
		t.Fatalf("stored = %s v%d", stored.DoorCode(), stored.Version())
	}
	if stored.State() != appointment.CheckedIn {
		t.Fatalf("state = %s: a Booked event must not downgrade a legacy CheckedIn row", stored.State())
	}
}

func TestDockAppointmentBookedNeverRevivesACancelledAppointment(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	_, err := bookedConsumer(w).Handle(ctx, "evt-1", dsBooked(1))
	wantNoErr(t, err)
	_, err = cancelledConsumer(w).Handle(ctx, "evt-2", dsAppt, 2)
	wantNoErr(t, err)
	// A re-announcement of the booking with a higher version than the cancel
	// (it cannot come from dock-scheduling, where cancelled is terminal) must
	// still leave the row cancelled.
	if _, err = bookedConsumer(w).Handle(ctx, "evt-3", dsBooked(5)); err != nil {
		t.Fatal(err)
	}
	stored, _ := w.appointments.Get(ctx, dsAppt)
	if stored.State() != appointment.Cancelled || stored.Version() != 5 {
		t.Fatalf("stored = %s v%d, want Cancelled v5", stored.State(), stored.Version())
	}
}

func TestDockAppointmentBookedIgnoresOutbound(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	e := dsBooked(1)
	e.Direction = "OUTBOUND"
	e.AsnNumbers = nil // an outbound appointment lists shipment refs, not ASNs
	got, err := bookedConsumer(w).Handle(ctx, "evt-1", e)
	wantNoErr(t, err)
	if got != usecases.Ignored || len(w.appointments.items) != 0 || len(w.processed.seen) != 0 {
		t.Fatalf("outcome = %s, stored = %d, claims = %d", got, len(w.appointments.items), len(w.processed.seen))
	}
}

func TestDockAppointmentBookedInvalidEvents(t *testing.T) {
	w := newWorld()
	uc := bookedConsumer(w)
	for name, mutate := range map[string]func(*usecases.DockAppointmentBooked){
		"no direction":      func(e *usecases.DockAppointmentBooked) { e.Direction = "" },
		"unknown direction": func(e *usecases.DockAppointmentBooked) { e.Direction = "SIDEWAYS" },
		"version zero":      func(e *usecases.DockAppointmentBooked) { e.Version = 0 },
		"bad id":            func(e *usecases.DockAppointmentBooked) { e.AppointmentID = "nope" },
		"bad door":          func(e *usecases.DockAppointmentBooked) { e.DoorCode = "" },
		"no asns":           func(e *usecases.DockAppointmentBooked) { e.AsnNumbers = nil },
		"inverted window":   func(e *usecases.DockAppointmentBooked) { e.WindowEnd = e.WindowStart.Add(-time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			e := dsBooked(1)
			mutate(&e)
			_, err := uc.Handle(context.Background(), "evt-x", e)
			wantErr(t, err, usecases.ErrInvalidEvent)
		})
	}
	if len(w.processed.seen) != 0 {
		t.Fatal("an invalid event must not be claimed")
	}
}

func TestDockAppointmentBookedTransientFailuresRollBack(t *testing.T) {
	w := newWorld()
	uc := bookedConsumer(w)
	w.processed.err = errors.New("db down")
	_, err := uc.Handle(context.Background(), "evt-1", dsBooked(1))
	wantErr(t, err, w.processed.err)
	w.processed.err = nil
	w.appointments.applyErr = errors.New("upsert failed")
	_, err = uc.Handle(context.Background(), "evt-2", dsBooked(1))
	wantErr(t, err, w.appointments.applyErr)
}

func TestDockAppointmentCancelled(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	_, err := bookedConsumer(w).Handle(ctx, "evt-1", dsBooked(1))
	wantNoErr(t, err)
	uc := cancelledConsumer(w)

	if got, err := uc.Handle(ctx, "evt-2", dsAppt, 2); err != nil || got != usecases.Applied {
		t.Fatalf("cancel = %s, %v", got, err)
	}
	stored, _ := w.appointments.Get(ctx, dsAppt)
	if stored.State() != appointment.Cancelled || stored.Version() != 2 {
		t.Fatalf("stored = %s v%d", stored.State(), stored.Version())
	}
	if got, _ := uc.Handle(ctx, "evt-2", dsAppt, 2); got != usecases.Duplicate {
		t.Fatalf("redelivery = %s", got)
	}
	if got, _ := uc.Handle(ctx, "evt-3", dsAppt, 2); got != usecases.Ignored {
		t.Fatalf("same version = %s, want ignored", got)
	}
	if got, _ := uc.Handle(ctx, "evt-4", "appt-bbbbbbbb-0000-4000-8000-000000000002", 9); got != usecases.Ignored {
		t.Fatalf("unknown appointment = %s, want ignored", got)
	}
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("outbox holds %d messages, want none (no loop)", len(w.outbox.msgs))
	}
}

func TestDockAppointmentCancelledInvalidAndTransient(t *testing.T) {
	w := newWorld()
	uc := cancelledConsumer(w)
	_, err := uc.Handle(context.Background(), "evt-1", "nope", 2)
	wantErr(t, err, usecases.ErrInvalidEvent)
	_, err = uc.Handle(context.Background(), "evt-2", dsAppt, 0)
	wantErr(t, err, usecases.ErrInvalidEvent)
	w.processed.err = errors.New("db down")
	_, err = uc.Handle(context.Background(), "evt-3", dsAppt, 2)
	wantErr(t, err, w.processed.err)
	w.processed.err = nil
	w.appointments.applyErr = errors.New("update failed")
	_, err = uc.Handle(context.Background(), "evt-4", dsAppt, 2)
	wantErr(t, err, w.appointments.applyErr)
}
