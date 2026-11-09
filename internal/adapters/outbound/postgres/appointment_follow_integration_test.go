//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

func mustWindow(t *testing.T, start, end time.Time) appointment.Window {
	t.Helper()
	w, err := appointment.NewWindow(start, end)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// TestAppointmentRepoFollowsReschedulesAndDoorReassignments pins the two
// ADR 0020 writes on real Postgres: both are guarded by version (strictly
// greater), touch only their own columns, never create a row and never
// change the state.
func TestAppointmentRepoFollowsReschedulesAndDoorReassignments(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	repo := postgres.NewAppointmentRepo(pool)
	start := t0.Add(24 * time.Hour)
	d := newAppointment(t, apptID(1), "DOOR-1", start, "ASN-1", "ASN-2")
	if ok, err := repo.ApplyBooked(ctx, d); err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	moved := mustWindow(t, start.Add(3*time.Hour), start.Add(5*time.Hour))

	// Reschedule: an equal version is refused, a greater one applies.
	if ok, err := repo.ApplyRescheduled(ctx, d.ID(), "DOOR-5", moved, 1); err != nil || ok {
		t.Fatalf("reschedule at the stored version must not apply: %v %v", ok, err)
	}
	if ok, err := repo.ApplyRescheduled(ctx, d.ID(), "DOOR-5", moved, 2); err != nil || !ok {
		t.Fatalf("reschedule v2: %v %v", ok, err)
	}
	got, err := repo.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	numbers := got.AsnNumbers()
	if got.DoorCode() != "DOOR-5" || !got.Window().Start().Equal(moved.Start()) || !got.Window().End().Equal(moved.End()) ||
		got.Version() != 2 || got.State() != appointment.Booked || got.Carrier() != "ACME Freight" ||
		len(numbers) != 2 || numbers[0] != "ASN-1" || numbers[1] != "ASN-2" {
		t.Fatalf("after the reschedule = %+v %v", got, numbers)
	}
	// An older reschedule changes nothing.
	if ok, err := repo.ApplyRescheduled(ctx, d.ID(), "DOOR-OLD", mustWindow(t, start, start.Add(time.Hour)), 1); err != nil || ok {
		t.Fatalf("an older reschedule must not apply: %v %v", ok, err)
	}

	// Door reassignment: same guard, only the door moves.
	if ok, err := repo.ApplyDoorReassigned(ctx, d.ID(), "DOOR-X", 2); err != nil || ok {
		t.Fatalf("reassignment at the stored version must not apply: %v %v", ok, err)
	}
	if ok, err := repo.ApplyDoorReassigned(ctx, d.ID(), "DOOR-7", 3); err != nil || !ok {
		t.Fatalf("reassignment v3: %v %v", ok, err)
	}
	got, _ = repo.Get(ctx, d.ID())
	if got.DoorCode() != "DOOR-7" || got.Version() != 3 || !got.Window().Start().Equal(moved.Start()) || got.State() != appointment.Booked {
		t.Fatalf("after the reassignment = %s v%d %v %s", got.DoorCode(), got.Version(), got.Window().Start(), got.State())
	}

	// Unknown ids are never created.
	ghost := appointment.ID(apptID(9))
	if ok, err := repo.ApplyRescheduled(ctx, ghost, "DOOR-5", moved, 9); err != nil || ok {
		t.Fatalf("rescheduling an unknown appointment must not apply: %v %v", ok, err)
	}
	if ok, err := repo.ApplyDoorReassigned(ctx, ghost, "DOOR-5", 9); err != nil || ok {
		t.Fatalf("reassigning an unknown appointment must not apply: %v %v", ok, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM appointments`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("appointments rows = %d, %v; want 1", rows, err)
	}

	// A cancelled appointment stays cancelled; its data still follows.
	if ok, err := repo.ApplyCancelled(ctx, d.ID(), 4); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if ok, err := repo.ApplyRescheduled(ctx, d.ID(), "DOOR-8", moved, 5); err != nil || !ok {
		t.Fatalf("reschedule of a cancelled row: %v %v", ok, err)
	}
	if ok, err := repo.ApplyDoorReassigned(ctx, d.ID(), "DOOR-9", 6); err != nil || !ok {
		t.Fatalf("reassignment of a cancelled row: %v %v", ok, err)
	}
	got, _ = repo.Get(ctx, d.ID())
	if got.State() != appointment.Cancelled || got.DoorCode() != "DOOR-9" || got.Version() != 6 {
		t.Fatalf("cancelled row = %s %s v%d, want Cancelled DOOR-9 v6", got.State(), got.DoorCode(), got.Version())
	}
	// A later Booked/Cancelled is guarded against the version the moves left.
	if ok, err := repo.ApplyCancelled(ctx, d.ID(), 6); err != nil || ok {
		t.Fatalf("a cancel at the reassignment's version must not apply: %v %v", ok, err)
	}
}
