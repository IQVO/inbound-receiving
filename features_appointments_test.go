package main_test

// Appointment step definitions (ADR 0019). The appointments of this context
// are a local copy of dock-scheduling's: they arrive over Kafka as
// DockAppointmentBooked / DockAppointmentCancelled, so the steps drive the
// same consumer use cases the Kafka adapter calls, and the effects are
// asserted through REST (the deprecated GETs), the receipts and the outbox.

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// apptAnnouncement is the last DockAppointmentBooked fed to the copy, kept so
// the same CloudEvents id can be redelivered.
type apptAnnouncement struct {
	eventID string
	event   usecases.DockAppointmentBooked
}

func (w *world) bookedConsumer() *usecases.ApplyDockAppointmentBooked {
	return &usecases.ApplyDockAppointmentBooked{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
}

func (w *world) feedBooked(ctx context.Context, e usecases.DockAppointmentBooked) {
	w.lastAppt = apptAnnouncement{eventID: w.nextEventID("bdd-appt"), event: e}
	w.wave2.outcome, w.wave2.err = w.bookedConsumer().Handle(ctx, w.lastAppt.eventID, e)
}

func (w *world) newAppointmentEvent(door, from, to, number string, version int64) (usecases.DockAppointmentBooked, error) {
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return usecases.DockAppointmentBooked{}, err
	}
	end, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return usecases.DockAppointmentBooked{}, err
	}
	w.apptSeq++
	return usecases.DockAppointmentBooked{
		AppointmentID: fmt.Sprintf("appt-00000000-0000-4000-8000-%012d", w.apptSeq), Direction: "INBOUND", DoorCode: door,
		Carrier: "ACME Freight", WindowStart: start, WindowEnd: end, AsnNumbers: []string{number}, Version: version,
	}, nil
}

// anAppointmentExists is the Given every scenario uses: dock-scheduling booked
// an appointment (version 1) and this context's copy applied it.
func (w *world) anAppointmentExists(ctx context.Context, door, from, to, number string) error {
	e, err := w.newAppointmentEvent(door, from, to, number, 1)
	if err != nil {
		return err
	}
	w.feedBooked(ctx, e)
	if w.wave2.err != nil {
		return fmt.Errorf("setup: feeding the booking gave %q: %w", w.wave2.outcome, w.wave2.err)
	}
	if w.wave2.outcome != usecases.Applied {
		return fmt.Errorf("setup: feeding the booking gave %q, want %q", w.wave2.outcome, usecases.Applied)
	}
	w.appointment, w.apptVersion = e.AppointmentID, e.Version
	return nil
}

func (w *world) dockSchedulingCancels(ctx context.Context) error {
	w.apptVersion++
	uc := &usecases.ApplyDockAppointmentCancelled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, w.nextEventID("bdd-appt-cancel"), w.appointment, w.apptVersion)
	return nil
}

func (w *world) dockSchedulingAnnouncesAgain(ctx context.Context, door string, version int64) error {
	e := w.lastAppt.event
	e.DoorCode, e.Version = door, version
	w.feedBooked(ctx, e)
	if w.wave2.outcome == usecases.Applied {
		w.apptVersion = version
	}
	return nil
}

func (w *world) dockSchedulingRedelivers(ctx context.Context) error {
	w.wave2.outcome, w.wave2.err = w.bookedConsumer().Handle(ctx, w.lastAppt.eventID, w.lastAppt.event)
	return nil
}

func (w *world) dockSchedulingAnnouncesOutbound(ctx context.Context, door string) error {
	e, err := w.newAppointmentEvent(door, "2026-10-09T08:00:00Z", "2026-10-09T10:00:00Z", "ASN-1", 1)
	if err != nil {
		return err
	}
	e.Direction, e.AsnNumbers = "OUTBOUND", nil
	w.feedBooked(ctx, e)
	w.appointment = e.AppointmentID
	return nil
}

func (w *world) registerAppointments(sc *godog.ScenarioContext) {
	sc.Step(`^an appointment at door "([^"]*)" from "([^"]*)" to "([^"]*)" covers the ASN "([^"]*)"$`, w.anAppointmentExists)
	sc.Step(`^dock-scheduling cancels the appointment$`, w.dockSchedulingCancels)
	sc.Step(`^dock-scheduling announces the appointment again at door "([^"]*)" with version (\d+)$`, w.dockSchedulingAnnouncesAgain)
	sc.Step(`^dock-scheduling redelivers the last appointment announcement$`, w.dockSchedulingRedelivers)
	sc.Step(`^dock-scheduling announces an outbound appointment at door "([^"]*)"$`, w.dockSchedulingAnnouncesOutbound)
}
