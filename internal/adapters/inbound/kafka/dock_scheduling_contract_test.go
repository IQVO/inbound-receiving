package kafka

// Pinned-contract tests for dock-scheduling (E1-E4). The pin is a byte-copy
// of IQVO/dock-scheduling's apis/asyncapi.yaml@develop; golden envelopes are
// the spec's own message examples, driven through the REAL consumer and the
// REAL use cases over the memory adapters.

import (
	"context"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

func dockPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "dock-scheduling.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, DockSchedulingTopic)
	if name != DockSchedulingTopic {
		t.Fatalf("channel key %q, want the topic constant %q", name, DockSchedulingTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: the pinned dock-scheduling spec documents the integration topic this
// repo's reader subscribes to (the topic constant) with every type the
// decoder routes on.
func TestPinnedDockSchedulingSpecCoversSubscriptions(t *testing.T) {
	_, _, types := dockPinned(t)
	for _, typ := range []string{
		TypeDockAppointmentBooked, TypeDockAppointmentCancelled,
		TypeDockAppointmentRescheduled, TypeDockAppointmentDoorReassigned,
	} {
		if _, ok := types[typ]; !ok {
			t.Errorf("routed type %s is not documented on channel %s", typ, DockSchedulingTopic)
		}
	}
}

// E2: each golden example decodes and dispatches through the real consumer
// and use cases; the id is claimed (proof the switch routed) and the local
// copy reflects the golden's own field values.
func TestDockSchedulingGoldenEventsDecode(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := dockPinned(t)
	booked := pinnedGolden(t, doc, ch, TypeDockAppointmentBooked)

	t.Run("booked applies the appointment", func(t *testing.T) {
		w := newAppointmentWorld()
		assertRoutedAndClaimed(t, func(b []byte) error {
			return w.consumer.HandleMessage(ctx, b)
		}, func(id string) bool { return w.processed.Has(usecases.DockAppointmentConsumer, id) },
			booked["id"].(string), booked)
		apt := dockGet(t, w, booked)
		if got := string(apt.DoorCode()); got != envelopeDataString(t, booked, "door_code") {
			t.Errorf("door = %q, want the golden's %q", got, envelopeDataString(t, booked, "door_code"))
		}
		if apt.State() != appointment.Booked || apt.Version() != 1 {
			t.Errorf("state/version = %s/%d, want Booked/1", apt.State(), apt.Version())
		}
	})

	followUps := []struct {
		name, typ, door string
	}{
		{"rescheduled moves the door", TypeDockAppointmentRescheduled, "WH1-DOCK-IN-02"},
		{"door reassigned moves the door", TypeDockAppointmentDoorReassigned, "WH1-DOCK-IN-02"},
	}
	for _, tc := range followUps {
		t.Run(tc.name, func(t *testing.T) {
			w := newAppointmentWorld()
			dockApply(t, ctx, w, booked)
			golden := pinnedGolden(t, doc, ch, tc.typ)
			dockApply(t, ctx, w, golden)
			if apt := dockGet(t, w, booked); string(apt.DoorCode()) != tc.door {
				t.Errorf("door = %q, want %q", apt.DoorCode(), tc.door)
			}
		})
	}

	t.Run("cancelled cancels the appointment", func(t *testing.T) {
		w := newAppointmentWorld()
		dockApply(t, ctx, w, booked)
		dockApply(t, ctx, w, pinnedGolden(t, doc, ch, TypeDockAppointmentCancelled))
		if apt := dockGet(t, w, booked); apt.State() != appointment.Cancelled || apt.Version() != 2 {
			t.Errorf("state/version = %s/%d, want Cancelled/2", apt.State(), apt.Version())
		}
	})

	t.Run("replay does not grow the local copy", func(t *testing.T) {
		w := newAppointmentWorld()
		dockApply(t, ctx, w, booked)
		before := len(dockList(t, w))
		dockApply(t, ctx, w, booked) // same bytes: the id is the idempotency key
		if after := len(dockList(t, w)); after != before {
			t.Fatalf("replay grew the copy: %d -> %d", before, after)
		}
	})
}

// E3: every routed golden carries the CloudEvents 1.0 invariants.
func TestDockSchedulingEnvelopeInvariants(t *testing.T) {
	doc, ch, types := dockPinned(t)
	for _, typ := range []string{
		TypeDockAppointmentBooked, TypeDockAppointmentCancelled,
		TypeDockAppointmentRescheduled, TypeDockAppointmentDoorReassigned,
	} {
		t.Run(types[typ], func(t *testing.T) {
			assertCEEnvelope(t, pinnedGolden(t, doc, ch, typ), "dock-scheduling", typ)
		})
	}
}

// E4: unknown attributes and data keys are tolerated; documented types this
// consumer does not route on, and non-CloudEvents bytes, are skipped.
func TestDockSchedulingUnknownTolerated(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := dockPinned(t)
	booked := pinnedGolden(t, doc, ch, TypeDockAppointmentBooked)

	w := newAppointmentWorld()
	if err := w.consumer.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t, booked))); err != nil {
		t.Fatalf("unknown keys broke decode: %v", err)
	}
	if apt := dockGet(t, w, booked); apt == nil {
		t.Fatal("the perturbed golden was not applied")
	}

	noShow := pinnedGolden(t, doc, ch, "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentNoShow")
	if err := w.consumer.HandleMessage(ctx, goldenBytes(t, noShow)); err != nil {
		t.Fatalf("documented-but-unrouted type: %v", err)
	}
	if got := len(dockList(t, w)); got != 1 {
		t.Errorf("local copy size = %d, want 1 (exactly-once routing)", got)
	}
	if err := w.consumer.HandleMessage(ctx, []byte(`not a cloud event`)); err != nil {
		t.Fatalf("non-CloudEvents must be skipped, not errored: %v", err)
	}
}

func dockApply(t *testing.T, ctx context.Context, w *appointmentWorld, env map[string]any) {
	t.Helper()
	if err := w.consumer.HandleMessage(ctx, goldenBytes(t, env)); err != nil {
		t.Fatalf("HandleMessage(%s): %v", env["type"], err)
	}
}

func dockGet(t *testing.T, w *appointmentWorld, env map[string]any) *appointment.DockAppointment {
	t.Helper()
	apt, err := w.appts.Get(context.Background(), appointment.ID(envelopeDataString(t, env, "appointment_id")))
	if err != nil {
		t.Fatalf("get appointment: %v", err)
	}
	if apt == nil {
		t.Fatal("appointment not in the local copy (existence on the pointer, not err)")
	}
	return apt
}

func dockList(t *testing.T, w *appointmentWorld) []*appointment.DockAppointment {
	t.Helper()
	rows, err := w.appts.List(context.Background(), repository.AppointmentFilter{}, repository.AppointmentCursor{}, 50)
	if err != nil {
		t.Fatalf("list appointments: %v", err)
	}
	return rows
}
