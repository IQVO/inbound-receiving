package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// The memory double follows the same ADR 0020 rules as the Postgres adapter.

func bookedRepo(t *testing.T) (*memory.AppointmentRepo, *appointment.DockAppointment, appointment.Window) {
	t.Helper()
	r := memory.NewAppointmentRepo()
	d := newAppointment(t, apptID(1), "DOOR-1", t0.Add(time.Hour)) // version 1, Booked
	if _, err := r.ApplyBooked(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	moved, err := appointment.NewWindow(t0.Add(4*time.Hour), t0.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return r, d, moved
}

func TestAppointmentRepoApplyRescheduledIsVersionGuarded(t *testing.T) {
	ctx := context.Background()
	r, d, moved := bookedRepo(t)
	applied, err := r.ApplyRescheduled(ctx, d.ID(), "DOOR-5", moved, 1)
	requireApplied(t, "a reschedule at the stored version", applied, err, false)
	applied, err = r.ApplyRescheduled(ctx, d.ID(), "DOOR-5", moved, 2)
	requireApplied(t, "reschedule v2", applied, err, true)
	got, _ := r.Get(ctx, d.ID())
	if got.DoorCode() != "DOOR-5" || !got.Window().Start().Equal(moved.Start()) || !got.Window().End().Equal(moved.End()) ||
		got.Version() != 2 || got.State() != appointment.Booked || !got.Covers("ASN-1") || got.Carrier() != "C" {
		t.Fatalf("after the reschedule = %+v", got)
	}
	applied, err = r.ApplyRescheduled(ctx, appointment.ID(apptID(9)), "DOOR-5", moved, 9)
	requireApplied(t, "rescheduling an unknown appointment", applied, err, false)
	if _, err := r.Get(ctx, appointment.ID(apptID(9))); err == nil {
		t.Fatal("an unknown appointment must not be created")
	}
}

func TestAppointmentRepoApplyDoorReassignedIsVersionGuarded(t *testing.T) {
	ctx := context.Background()
	r, d, _ := bookedRepo(t)
	applied, err := r.ApplyDoorReassigned(ctx, d.ID(), "DOOR-X", 1)
	requireApplied(t, "a reassignment at the stored version", applied, err, false)
	applied, err = r.ApplyDoorReassigned(ctx, d.ID(), "DOOR-7", 2)
	requireApplied(t, "reassignment v2", applied, err, true)
	got, _ := r.Get(ctx, d.ID())
	if got.DoorCode() != "DOOR-7" || got.Version() != 2 || !got.Window().Start().Equal(d.Window().Start()) {
		t.Fatalf("after the reassignment = %s v%d", got.DoorCode(), got.Version())
	}
	applied, err = r.ApplyDoorReassigned(ctx, appointment.ID(apptID(9)), "DOOR-5", 9)
	requireApplied(t, "reassigning an unknown appointment", applied, err, false)
}

func TestAppointmentRepoFollowsKeepACancelledRowCancelled(t *testing.T) {
	ctx := context.Background()
	r, d, moved := bookedRepo(t)
	applied, err := r.ApplyCancelled(ctx, d.ID(), 2)
	requireApplied(t, "cancel", applied, err, true)
	applied, err = r.ApplyRescheduled(ctx, d.ID(), "DOOR-8", moved, 3)
	requireApplied(t, "reschedule of a cancelled row", applied, err, true)
	applied, err = r.ApplyDoorReassigned(ctx, d.ID(), "DOOR-9", 4)
	requireApplied(t, "reassignment of a cancelled row", applied, err, true)
	if got, _ := r.Get(ctx, d.ID()); got.State() != appointment.Cancelled || got.DoorCode() != "DOOR-9" || got.Version() != 4 {
		t.Fatalf("cancelled row = %s %s v%d", got.State(), got.DoorCode(), got.Version())
	}
}
