package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

const dsAppointment = "appt-aaaaaaaa-0000-4000-8000-000000000001"

type appointmentWorld struct {
	appts     *memory.AppointmentRepo
	processed *memory.ProcessedEventRepo
	outbox    *memory.OutboxRepo
	consumer  *DockAppointmentConsumer
}

func newAppointmentWorld() *appointmentWorld {
	appts, processed, ob := memory.NewAppointmentRepo(), memory.NewProcessedEventRepo(), memory.NewOutboxRepo()
	uow := memory.NewUnitOfWork(appts, processed, ob)
	return &appointmentWorld{
		appts: appts, processed: processed, outbox: ob,
		consumer: &DockAppointmentConsumer{
			Booked:    &usecases.ApplyDockAppointmentBooked{UoW: uow, ProcessedEvents: processed, Appointments: appts},
			Cancelled: &usecases.ApplyDockAppointmentCancelled{UoW: uow, ProcessedEvents: processed, Appointments: appts},
			Logger:    quiet(),
		},
	}
}

// bookedEvent is a dock-scheduling DockAppointmentBooked carrying MORE than
// the local copy needs (the superset of contracts-delta.md).
func bookedEvent(t *testing.T, id string, version int, mutate func(map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"appointment_id": dsAppointment, "site_id": "WH1", "direction": "INBOUND", "door_code": "DOOR-1", "carrier": "ACME Freight",
		"carrier_scac": "ACMF", "window_start": "2026-11-06T09:00:00Z", "window_end": "2026-11-06T10:00:00Z",
		"asn_numbers": []string{"ASN-1"}, "cpt_id": nil, "shipment_refs": []string{}, "expected_seal_number": "S-9",
		"provenance": "native", "version": version,
	}
	if mutate != nil {
		mutate(data)
	}
	return cloudEvent(t, id, TypeDockAppointmentBooked, data)
}

func cancelledEvent(t *testing.T, id string, version int) []byte {
	t.Helper()
	return cloudEvent(t, id, TypeDockAppointmentCancelled, map[string]any{
		"appointment_id": dsAppointment, "site_id": "WH1", "door_code": "DOOR-1", "reason": "CARRIER_REQUEST", "version": version,
	})
}

func (w *appointmentWorld) stored(t *testing.T) *appointment.DockAppointment {
	t.Helper()
	d, err := w.appts.Get(context.Background(), dsAppointment)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTheDockAppointmentTypesAreDockSchedulings(t *testing.T) {
	if TypeDockAppointmentBooked != "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked" ||
		TypeDockAppointmentCancelled != "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled" ||
		DockSchedulingTopic != "warehouse.dock-scheduling.events" {
		t.Fatalf("types = %s %s %s", TypeDockAppointmentBooked, TypeDockAppointmentCancelled, DockSchedulingTopic)
	}
}

func TestDockAppointmentConsumerFeedsAndCancelsTheCopy(t *testing.T) {
	w, ctx := newAppointmentWorld(), context.Background()
	for range 2 { // a redelivery is a duplicate
		if err := w.consumer.HandleMessage(ctx, bookedEvent(t, "ev-1", 1, nil)); err != nil {
			t.Fatal(err)
		}
	}
	d := w.stored(t)
	if d.State() != appointment.Booked || d.Version() != 1 || d.DoorCode() != "DOOR-1" || d.Carrier() != "ACME Freight" ||
		!d.Covers("ASN-1") || d.Window().Start().Hour() != 9 || !w.processed.Has(usecases.DockAppointmentConsumer, "ev-1") {
		t.Fatalf("stored = %+v", d)
	}
	if err := w.consumer.HandleMessage(ctx, cancelledEvent(t, "ev-2", 2)); err != nil {
		t.Fatal(err)
	}
	if d = w.stored(t); d.State() != appointment.Cancelled || d.Version() != 2 {
		t.Fatalf("after cancel = %s v%d", d.State(), d.Version())
	}
	// NO-LOOP (ADR 0019): applying dock-scheduling's events publishes nothing.
	if n := len(w.outbox.Messages()); n != 0 {
		t.Fatalf("the outbox holds %d messages, want none", n)
	}
}

func TestDockAppointmentConsumerKeepsOnlyInboundAppointments(t *testing.T) {
	w := newAppointmentWorld()
	outbound := bookedEvent(t, "ev-1", 1, func(m map[string]any) {
		m["direction"] = "OUTBOUND"
		m["asn_numbers"] = []string{}
		m["shipment_refs"] = []string{"SHP-1"}
	})
	if err := w.consumer.HandleMessage(context.Background(), outbound); err != nil {
		t.Fatal(err)
	}
	if _, err := w.appts.Get(context.Background(), dsAppointment); err == nil {
		t.Fatal("an OUTBOUND appointment must not be stored")
	}
}

func TestDockAppointmentConsumerIgnoresOtherTypesAndSkipsDeterministicProblems(t *testing.T) {
	cases := map[string][]byte{
		"flat legacy envelope": []byte(`{"event_id":"1","event_type":"DockAppointmentBooked","payload":{}}`),
		"not json":             []byte(`nope`),
		"rescheduled":          cloudEvent(t, "ev-2", "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled", map[string]any{"appointment_id": dsAppointment, "version": 2}),
		"no show":              cloudEvent(t, "ev-3", "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow", map[string]any{"appointment_id": dsAppointment, "version": 2}),
		"closure":              cloudEvent(t, "ev-4", "com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled", map[string]any{"door_code": "DOOR-1"}),
		"legacy inbound type":  cloudEvent(t, "ev-5", "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked", map[string]any{"appointment_id": dsAppointment}),
		"malformed booked":     cloudEvent(t, "ev-6", TypeDockAppointmentBooked, map[string]any{"window_start": "yesterday"}),
		"invalid booked":       bookedEvent(t, "ev-7", 1, func(m map[string]any) { m["asn_numbers"] = []string{} }),
		"booked without dir":   bookedEvent(t, "ev-8", 1, func(m map[string]any) { delete(m, "direction") }),
		"booked version zero":  bookedEvent(t, "ev-9", 0, nil),
		"malformed cancelled":  cloudEvent(t, "ev-10", TypeDockAppointmentCancelled, map[string]any{"version": "two"}),
		"invalid cancelled":    cloudEvent(t, "ev-11", TypeDockAppointmentCancelled, map[string]any{"appointment_id": "nope", "version": 2}),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			w := newAppointmentWorld()
			if err := w.consumer.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if _, err := w.appts.Get(context.Background(), dsAppointment); err == nil {
				t.Fatal("a skipped message must not change the copy")
			}
		})
	}
}

type failingBooked struct{ err error }

func (f failingBooked) Handle(context.Context, string, usecases.DockAppointmentBooked) (usecases.Outcome, error) {
	return "", f.err
}

type failingCancelled struct{ err error }

func (f failingCancelled) Handle(context.Context, string, string, int64) (usecases.Outcome, error) {
	return "", f.err
}

func TestDockAppointmentConsumerReturnsTransientErrorsForRetry(t *testing.T) {
	boom := errors.New("db down")
	c := &DockAppointmentConsumer{Booked: failingBooked{boom}, Cancelled: failingCancelled{boom}, Logger: quiet()}
	if err := c.HandleMessage(context.Background(), bookedEvent(t, "ev-1", 1, nil)); !errors.Is(err, boom) {
		t.Fatalf("booked: %v", err)
	}
	if err := c.HandleMessage(context.Background(), cancelledEvent(t, "ev-2", 2)); !errors.Is(err, boom) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestDockAppointmentConsumerRunCommitsAfterHandling(t *testing.T) {
	w := newAppointmentWorld()
	reader := &fakeReader{queue: []kafkago.Message{
		{Offset: 0, Value: bookedEvent(t, "ev-1", 1, nil)}, {Offset: 1, Value: []byte("junk")}, {Offset: 2, Value: cancelledEvent(t, "ev-2", 2)},
	}}
	w.consumer.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.consumer.Run(ctx) }()
	waitFor(t, func() bool { return len(reader.commits()) == 3 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if w.stored(t).State() != appointment.Cancelled {
		t.Fatal("the cancel must have been applied before its offset was committed")
	}
	if err := w.consumer.Close(); err != nil || !reader.closed {
		t.Fatalf("Close = %v closed=%v", err, reader.closed)
	}
}

func TestNewDockAppointmentConsumerDoesNotDial(t *testing.T) {
	c := NewDockAppointmentConsumer([]string{"127.0.0.1:1"}, "group-from-env", nil, nil, nil)
	if c.Reader == nil || c.Logger == nil {
		t.Fatalf("consumer = %+v", c)
	}
	_ = c.Close()
}
