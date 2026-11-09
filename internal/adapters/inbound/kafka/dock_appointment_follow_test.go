package kafka

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// ADR 0020: the copy follows DockAppointmentRescheduled and
// DockAppointmentDoorReassigned.

// rescheduledEvent is a full dock-scheduling DockAppointmentRescheduled
// (more than the copy reads).
func rescheduledEvent(t *testing.T, id string, version int, mutate func(map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"appointment_id": dsAppointment, "site_id": "WH1", "door_code": "DOOR-5", "previous_door_code": "DOOR-1",
		"window_start": "2026-11-06T14:00:00Z", "window_end": "2026-11-06T15:30:00Z",
		"previous_window_start": "2026-11-06T09:00:00Z", "previous_window_end": "2026-11-06T10:00:00Z",
		"arrival_tolerance_minutes": 15, "version": version,
	}
	if mutate != nil {
		mutate(data)
	}
	return cloudEvent(t, id, TypeDockAppointmentRescheduled, data)
}

func doorReassignedEvent(t *testing.T, id string, version int, mutate func(map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"appointment_id": dsAppointment, "site_id": "WH1", "from_door_code": "DOOR-1", "to_door_code": "DOOR-7", "version": version,
	}
	if mutate != nil {
		mutate(data)
	}
	return cloudEvent(t, id, TypeDockAppointmentDoorReassigned, data)
}

func TestTheFollowedDockSchedulingTypesArePinned(t *testing.T) {
	if TypeDockAppointmentRescheduled != "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled" ||
		TypeDockAppointmentDoorReassigned != "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned" {
		t.Fatalf("types = %s %s", TypeDockAppointmentRescheduled, TypeDockAppointmentDoorReassigned)
	}
}

func TestDockAppointmentConsumerFollowsAReschedule(t *testing.T) {
	w, ctx := newAppointmentWorld(), context.Background()
	if err := w.consumer.HandleMessage(ctx, bookedEvent(t, "ev-1", 1, nil)); err != nil {
		t.Fatal(err)
	}
	for range 2 { // a redelivery is a duplicate
		if err := w.consumer.HandleMessage(ctx, rescheduledEvent(t, "ev-2", 2, nil)); err != nil {
			t.Fatal(err)
		}
	}
	d := w.stored(t)
	start := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	if d.DoorCode() != "DOOR-5" || !d.Window().Start().Equal(start) || !d.Window().End().Equal(start.Add(90*time.Minute)) ||
		d.Version() != 2 || d.State() != appointment.Booked || !d.Covers("ASN-1") {
		t.Fatalf("after the reschedule = %+v", d)
	}
	if !w.processed.Has(usecases.DockAppointmentConsumer, "ev-2") {
		t.Fatal("the reschedule's id must be claimed")
	}
	// A stale reschedule is committed past without changing the copy.
	if err := w.consumer.HandleMessage(ctx, rescheduledEvent(t, "ev-3", 2, func(m map[string]any) { m["door_code"] = "DOOR-OLD" })); err != nil {
		t.Fatal(err)
	}
	if d = w.stored(t); d.DoorCode() != "DOOR-5" {
		t.Fatalf("a stale reschedule changed the copy: %s", d.DoorCode())
	}
	// NO-LOOP (ADR 0019, 0020).
	if n := len(w.outbox.Messages()); n != 0 {
		t.Fatalf("the outbox holds %d messages, want none", n)
	}
}

func TestDockAppointmentConsumerFollowsADoorReassignment(t *testing.T) {
	w, ctx := newAppointmentWorld(), context.Background()
	if err := w.consumer.HandleMessage(ctx, bookedEvent(t, "ev-1", 1, nil)); err != nil {
		t.Fatal(err)
	}
	before := w.stored(t).Window()
	if err := w.consumer.HandleMessage(ctx, doorReassignedEvent(t, "ev-2", 3, nil)); err != nil {
		t.Fatal(err)
	}
	d := w.stored(t)
	if d.DoorCode() != "DOOR-7" || d.Version() != 3 || !d.Window().Start().Equal(before.Start()) ||
		!w.processed.Has(usecases.DockAppointmentConsumer, "ev-2") {
		t.Fatalf("after the reassignment = %s v%d", d.DoorCode(), d.Version())
	}
	if err := w.consumer.HandleMessage(ctx, doorReassignedEvent(t, "ev-3", 2, func(m map[string]any) { m["to_door_code"] = "DOOR-OLD" })); err != nil {
		t.Fatal(err)
	}
	if d = w.stored(t); d.DoorCode() != "DOOR-7" || d.Version() != 3 {
		t.Fatalf("a stale reassignment changed the copy: %s v%d", d.DoorCode(), d.Version())
	}
	if n := len(w.outbox.Messages()); n != 0 {
		t.Fatalf("the outbox holds %d messages, want none", n)
	}
}

// recordingAppliers records which use case a message was routed to.
type recordingAppliers struct {
	calls       []string
	rescheduled usecases.DockAppointmentRescheduled
	reassigned  usecases.DockAppointmentDoorReassigned
}

func (r *recordingAppliers) consumer() *DockAppointmentConsumer {
	return &DockAppointmentConsumer{
		Booked: bookedFunc(func(string, usecases.DockAppointmentBooked) { r.calls = append(r.calls, "booked") }),
		Cancelled: cancelledFunc(func(string, string, int64) {
			r.calls = append(r.calls, "cancelled")
		}),
		Rescheduled: rescheduledFunc(func(_ string, e usecases.DockAppointmentRescheduled) {
			r.calls, r.rescheduled = append(r.calls, "rescheduled"), e
		}),
		DoorReassigned: reassignedFunc(func(_ string, e usecases.DockAppointmentDoorReassigned) {
			r.calls, r.reassigned = append(r.calls, "reassigned"), e
		}),
		Logger: quiet(),
	}
}

type (
	bookedFunc      func(string, usecases.DockAppointmentBooked)
	cancelledFunc   func(string, string, int64)
	rescheduledFunc func(string, usecases.DockAppointmentRescheduled)
	reassignedFunc  func(string, usecases.DockAppointmentDoorReassigned)
)

func (f bookedFunc) Handle(_ context.Context, id string, e usecases.DockAppointmentBooked) (usecases.Outcome, error) {
	f(id, e)
	return usecases.Applied, nil
}

func (f cancelledFunc) Handle(_ context.Context, id, appt string, v int64) (usecases.Outcome, error) {
	f(id, appt, v)
	return usecases.Applied, nil
}

func (f rescheduledFunc) Handle(_ context.Context, id string, e usecases.DockAppointmentRescheduled) (usecases.Outcome, error) {
	f(id, e)
	return usecases.Applied, nil
}

func (f reassignedFunc) Handle(_ context.Context, id string, e usecases.DockAppointmentDoorReassigned) (usecases.Outcome, error) {
	f(id, e)
	return usecases.Applied, nil
}

func TestDockAppointmentConsumerRoutesByType(t *testing.T) {
	cases := map[string]struct {
		msg  []byte
		want string
	}{
		"booked":      {bookedEvent(t, "ev-1", 1, nil), "booked"},
		"cancelled":   {cancelledEvent(t, "ev-2", 2), "cancelled"},
		"rescheduled": {rescheduledEvent(t, "ev-3", 3, nil), "rescheduled"},
		"reassigned":  {doorReassignedEvent(t, "ev-4", 4, nil), "reassigned"},
		"no show":     {cloudEvent(t, "ev-5", "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow", map[string]any{"appointment_id": dsAppointment, "version": 2}), ""},
		"closure":     {cloudEvent(t, "ev-6", "com.warehouse.wms.dock-scheduling.dockcalendar.DoorClosureScheduled", map[string]any{"door_code": "DOOR-1"}), ""},
		"checked in":  {cloudEvent(t, "ev-7", "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCheckedIn", map[string]any{"appointment_id": dsAppointment, "version": 2}), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &recordingAppliers{}
			if err := r.consumer().HandleMessage(context.Background(), tc.msg); err != nil {
				t.Fatalf("err = %v, want nil (committed)", err)
			}
			got := strings.Join(r.calls, ",")
			if got != tc.want {
				t.Fatalf("routed to %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDockAppointmentConsumerDecodesTheFollowedPayloads(t *testing.T) {
	r := &recordingAppliers{}
	c := r.consumer()
	if err := c.HandleMessage(context.Background(), rescheduledEvent(t, "ev-1", 4, nil)); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	want := usecases.DockAppointmentRescheduled{
		AppointmentID: dsAppointment, DoorCode: "DOOR-5",
		WindowStart: start, WindowEnd: start.Add(90 * time.Minute), Version: 4,
	}
	got := r.rescheduled
	if got.AppointmentID != want.AppointmentID || got.DoorCode != want.DoorCode ||
		!got.WindowStart.Equal(want.WindowStart) || !got.WindowEnd.Equal(want.WindowEnd) || got.Version != want.Version {
		t.Fatalf("rescheduled = %+v, want %+v", got, want)
	}
	if err := c.HandleMessage(context.Background(), doorReassignedEvent(t, "ev-2", 5, nil)); err != nil {
		t.Fatal(err)
	}
	if r.reassigned != (usecases.DockAppointmentDoorReassigned{AppointmentID: dsAppointment, FromDoorCode: "DOOR-1", ToDoorCode: "DOOR-7", Version: 5}) {
		t.Fatalf("reassigned = %+v", r.reassigned)
	}
}

func TestDockAppointmentConsumerSkipsInvalidFollowedPayloadsWithAWarning(t *testing.T) {
	cases := map[string][]byte{
		"malformed rescheduled":    cloudEvent(t, "ev-1", TypeDockAppointmentRescheduled, map[string]any{"window_start": "later"}),
		"rescheduled empty id":     rescheduledEvent(t, "ev-2", 2, func(m map[string]any) { m["appointment_id"] = "" }),
		"rescheduled version zero": rescheduledEvent(t, "ev-3", 0, nil),
		"rescheduled empty door":   rescheduledEvent(t, "ev-4", 2, func(m map[string]any) { delete(m, "door_code") }),
		"rescheduled inverted":     rescheduledEvent(t, "ev-5", 2, func(m map[string]any) { m["window_end"] = "2026-11-06T13:00:00Z" }),
		"rescheduled empty window": rescheduledEvent(t, "ev-6", 2, func(m map[string]any) { m["window_end"] = m["window_start"] }),
		"malformed reassigned":     cloudEvent(t, "ev-7", TypeDockAppointmentDoorReassigned, map[string]any{"version": "three"}),
		"reassigned empty id":      doorReassignedEvent(t, "ev-8", 2, func(m map[string]any) { delete(m, "appointment_id") }),
		"reassigned version zero":  doorReassignedEvent(t, "ev-9", 0, nil),
		"reassigned empty door":    doorReassignedEvent(t, "ev-10", 2, func(m map[string]any) { m["to_door_code"] = "" }),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			w := newAppointmentWorld()
			if err := w.consumer.HandleMessage(context.Background(), bookedEvent(t, "ev-0", 1, nil)); err != nil {
				t.Fatal(err)
			}
			logs := &lockedBuffer{}
			w.consumer.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			if err := w.consumer.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if d := w.stored(t); d.DoorCode() != "DOOR-1" || d.Version() != 1 {
				t.Fatalf("a skipped message changed the copy: %s v%d", d.DoorCode(), d.Version())
			}
			if logs.lines("level=WARN") != 1 {
				t.Fatalf("want exactly one WARN, logs = %s", logs)
			}
		})
	}
}

func TestDockAppointmentConsumerIgnoresAnUnknownAppointmentQuietly(t *testing.T) {
	for name, msg := range map[string][]byte{
		"rescheduled": rescheduledEvent(t, "ev-1", 2, nil),
		"reassigned":  doorReassignedEvent(t, "ev-1", 2, nil),
	} {
		t.Run(name, func(t *testing.T) {
			w := newAppointmentWorld()
			logs := &lockedBuffer{}
			w.consumer.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			if err := w.consumer.HandleMessage(context.Background(), msg); err != nil {
				t.Fatalf("err = %v, want nil (committed past)", err)
			}
			if _, err := w.appts.Get(context.Background(), dsAppointment); err == nil {
				t.Fatal("a followed event must never create an appointment")
			}
			if !w.processed.Has(usecases.DockAppointmentConsumer, "ev-1") {
				t.Fatal("the claim of an ignored event must commit")
			}
			// An OUTBOUND appointment is never in the copy: this is normal.
			if logs.lines("level=WARN") != 0 || logs.lines("level=INFO") != 0 {
				t.Fatalf("an unknown appointment must log at DEBUG only, logs = %s", logs)
			}
			if logs.lines("level=DEBUG") != 1 || logs.lines("outcome=ignored") != 1 {
				t.Fatalf("want one DEBUG line with outcome=ignored, logs = %s", logs)
			}
		})
	}
}

func TestDockAppointmentConsumerLogsAnAppliedFollowAtInfo(t *testing.T) {
	w := newAppointmentWorld()
	if err := w.consumer.HandleMessage(context.Background(), bookedEvent(t, "ev-0", 1, nil)); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	w.consumer.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := w.consumer.HandleMessage(context.Background(), rescheduledEvent(t, "ev-1", 2, nil)); err != nil {
		t.Fatal(err)
	}
	if logs.lines("level=INFO") != 1 || logs.lines("outcome=applied") != 1 || logs.lines("level=DEBUG") != 0 {
		t.Fatalf("an applied reschedule logs one INFO line, logs = %s", logs)
	}
}

type failingRescheduled struct{ err error }

func (f failingRescheduled) Handle(context.Context, string, usecases.DockAppointmentRescheduled) (usecases.Outcome, error) {
	return "", f.err
}

type failingReassigned struct{ err error }

func (f failingReassigned) Handle(context.Context, string, usecases.DockAppointmentDoorReassigned) (usecases.Outcome, error) {
	return "", f.err
}

func TestDockAppointmentConsumerRetriesTransientFollowErrors(t *testing.T) {
	boom := errors.New("db down")
	c := &DockAppointmentConsumer{Rescheduled: failingRescheduled{boom}, DoorReassigned: failingReassigned{boom}, Logger: quiet()}
	if err := c.HandleMessage(context.Background(), rescheduledEvent(t, "ev-1", 2, nil)); !errors.Is(err, boom) {
		t.Fatalf("rescheduled: %v", err)
	}
	if err := c.HandleMessage(context.Background(), doorReassignedEvent(t, "ev-2", 2, nil)); !errors.Is(err, boom) {
		t.Fatalf("reassigned: %v", err)
	}
}

func TestDockAppointmentConsumerRunCommitsAFollowAfterApplyingIt(t *testing.T) {
	w := newAppointmentWorld()
	reader := &fakeReader{queue: []kafkago.Message{
		{Offset: 0, Value: bookedEvent(t, "ev-1", 1, nil)},
		{Offset: 1, Value: rescheduledEvent(t, "ev-2", 2, nil)},
		{Offset: 2, Value: doorReassignedEvent(t, "ev-3", 3, nil)},
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
	if d := w.stored(t); d.DoorCode() != "DOOR-7" || d.Version() != 3 {
		t.Fatalf("the reassignment must have been applied before its offset was committed: %s v%d", d.DoorCode(), d.Version())
	}
}

func TestNewDockAppointmentConsumerWiresEveryApplier(t *testing.T) {
	r := &recordingAppliers{}
	src := r.consumer()
	c := NewDockAppointmentConsumer([]string{"127.0.0.1:1"}, "group-from-env", DockAppointmentAppliers{
		Booked: src.Booked, Cancelled: src.Cancelled, Rescheduled: src.Rescheduled, DoorReassigned: src.DoorReassigned,
	}, quiet())
	defer func() { _ = c.Close() }()
	for _, msg := range [][]byte{bookedEvent(t, "ev-1", 1, nil), cancelledEvent(t, "ev-2", 2), rescheduledEvent(t, "ev-3", 3, nil), doorReassignedEvent(t, "ev-4", 4, nil)} {
		if err := c.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(r.calls, ","); got != "booked,cancelled,rescheduled,reassigned" {
		t.Fatalf("calls = %s", got)
	}
}
