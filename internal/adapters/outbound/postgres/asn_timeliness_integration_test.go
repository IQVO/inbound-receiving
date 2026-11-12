//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// The ASN-timeliness pieces of ADR 0025 against real Postgres: the
// registered_at round-trip (and its legacy NULL), the exactly-once marker of
// AsnLateForArrival, the Arrival read the open uses, and the open itself
// raising the event through the real unit of work. The analytics side
// (migration 0004, the fold and the report read) runs in the analyticsstore
// contract tests under this tag.

// TestAsnRepoRegisteredAtRoundTrip: registered_at survives the save/load as
// the same instant that stamped ASNRegistered, and a legacy NULL row reads as
// the zero time (unknown; an unknown registration can never be late).
func TestAsnRepoRegisteredAtRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	repo := postgres.NewAsnRepo(pool)
	if err := repo.Save(ctx, newAsn(t, "ASN-REG"), 0); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := repo.Get(ctx, "ASN-REG")
	if err != nil {
		t.Fatal(err)
	}
	if !got.RegisteredAt().Equal(t0) || got.RegisteredAt().Location() != time.UTC {
		t.Fatalf("registered_at = %v, want %v (UTC)", got.RegisteredAt(), t0)
	}
	// A row written before the column existed has NULL.
	if _, err := pool.Exec(ctx, `UPDATE asns SET registered_at = NULL WHERE asn_number = $1`, "ASN-REG"); err != nil {
		t.Fatal(err)
	}
	legacy, err := repo.Get(ctx, "ASN-REG")
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.RegisteredAt().IsZero() {
		t.Fatalf("a NULL registered_at must read as the zero time, got %v", legacy.RegisteredAt())
	}
	if late := legacy.LateForArrival("appt-1", "visit-1", t0, t0); late != nil {
		t.Fatalf("an unknown registration time must never be late, got %+v", late)
	}
}

// TestAsnRepoMarkLateArrivalIsOncePerAsn: only the first marker insert wins,
// and the row keeps the first raise's appointment, visit and instant.
func TestAsnRepoMarkLateArrivalIsOncePerAsn(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	repo := postgres.NewAsnRepo(pool)
	first, err := repo.MarkLateArrival(ctx, "ASN-1", "appt-1", "visit-1", t0)
	if err != nil || !first {
		t.Fatalf("first mark = %v, %v; want the raiser", first, err)
	}
	for i, replay := range []struct{ appt, visit string }{{"appt-1", "visit-1"}, {"appt-2", "visit-2"}} {
		won, err := repo.MarkLateArrival(ctx, "ASN-1", replay.appt, replay.visit, t0.Add(time.Duration(i+1)*time.Hour))
		if err != nil || won {
			t.Fatalf("replay %d = %v, %v; a redelivery must never win", i, won, err)
		}
	}
	var appt, visit string
	var raised time.Time
	if err := pool.QueryRow(ctx, `SELECT appointment_id, visit_id, raised_at FROM asn_late_arrivals WHERE asn_number = $1`, "ASN-1").
		Scan(&appt, &visit, &raised); err != nil {
		t.Fatal(err)
	}
	if appt != "appt-1" || visit != "visit-1" || !raised.Equal(t0) {
		t.Fatalf("marker = %s %s %v, want the first raise appt-1 visit-1 %v", appt, visit, raised, t0)
	}
	other, err := repo.MarkLateArrival(ctx, "ASN-2", "appt-9", "visit-9", t0)
	if err != nil || !other {
		t.Fatalf("another ASN's first mark = %v, %v; want the raiser", other, err)
	}
}

// TestStowRepoArrivalReadsTheEarliestVisit: Arrival reports the earliest
// arrived_at and its visit id (the same copy the dock-to-stock fact reads,
// ADR 0021), and false while no visit was seen.
func TestStowRepoArrivalReadsTheEarliestVisit(t *testing.T) {
	ctx := context.Background()
	stows := postgres.NewStowRepo(startPostgresPool(t))
	id := appointment.ID("appt-0a0a0a0a-0000-4000-8000-000000000041")
	if _, known, err := stows.Arrival(ctx, id); err != nil || known {
		t.Fatalf("no arrival yet = %v, %v; want known false", known, err)
	}
	for _, in := range []struct {
		visit string
		at    time.Time
		moved bool
	}{{"visit-late", t0.Add(2 * time.Hour), true}, {"visit-early", t0, true}, {"visit-later", t0.Add(3 * time.Hour), false}} {
		moved, err := stows.ApplyArrival(ctx, id, in.visit, in.at)
		if err != nil || moved != in.moved {
			t.Fatalf("apply %s = %v, %v; want moved %v", in.visit, moved, err, in.moved)
		}
	}
	a, known, err := stows.Arrival(ctx, id)
	if err != nil || !known {
		t.Fatalf("arrival = %v, %v; want known", known, err)
	}
	if a.VisitID != "visit-early" || !a.ArrivedAt.Equal(t0) || a.ArrivedAt.Location() != time.UTC {
		t.Fatalf("arrival = %+v, want visit-early at %v (UTC)", a, t0)
	}
}

// TestOpenReceiptRaisesAsnLateForArrivalInTheSameUnitOfWork: over the real
// migration, an open under an appointment whose visit arrived before the
// ASN's registration writes AsnLateForArrival to BOTH topics' outbox rows
// once; an on-time ASN (and a walk-in) raise nothing.
func TestOpenReceiptRaisesAsnLateForArrivalInTheSameUnitOfWork(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)
	stows := postgres.NewStowRepo(pool)
	w.Stows = stows
	processed := postgres.NewProcessedEventRepo(pool)
	book := func(eventID, appt string, asns ...string) {
		t.Helper()
		if _, err := (&usecases.ApplyDockAppointmentBooked{UoW: w.UoW, ProcessedEvents: processed, Appointments: postgres.NewAppointmentRepo(pool)}).Handle(ctx, eventID, usecases.DockAppointmentBooked{
			AppointmentID: appt, Direction: "INBOUND", DoorCode: "DOOR-1", Carrier: "ACME",
			WindowStart: t0, WindowEnd: t0.Add(time.Hour), AsnNumbers: asns, Version: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	lateType := "com.warehouse.wms.inbound-receiving.asn.AsnLateForArrival"

	registerAsn(t, w, "ASN-LATE") // registered_at = the real now
	registerAsn(t, w, "ASN-ONTIME")
	earlyAppt := "appt-0a0a0a0a-0000-4000-8000-000000000042"
	laterAppt := "appt-0a0a0a0a-0000-4000-8000-000000000043"
	book("book-early", earlyAppt, "ASN-LATE", "ASN-ONTIME")
	book("book-later", laterAppt, "ASN-LATE", "ASN-ONTIME")
	if _, err := stows.ApplyArrival(ctx, appointment.ID(earlyAppt), "visit-1", time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatal(err) // arrived before the registration: late
	}
	if _, err := stows.ApplyArrival(ctx, appointment.ID(laterAppt), "visit-2", time.Now().Add(time.Hour).UTC()); err != nil {
		t.Fatal(err) // arrives after the registration: on time
	}
	if _, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-LATE", AppointmentID: earlyAppt}); err != nil {
		t.Fatal(err)
	}
	if got := countType(outboxTypes(t, pool), lateType); got != 2 {
		t.Fatalf("AsnLateForArrival outbox rows = %d, want 2 (integration + analytics, one occurrence)", got)
	}
	var ids int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT event_id) FROM outbox_events WHERE event_type = $1`, lateType).Scan(&ids); err != nil || ids != 1 {
		t.Fatalf("distinct event ids = %d (%v), want 1", ids, err)
	}

	// A second open of the same ASN is refused before the marker; nothing new.
	if _, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-LATE", AppointmentID: earlyAppt}); !errors.Is(err, repository.ErrReceiptAlreadyOpen) {
		t.Fatalf("second open = %v, want ErrReceiptAlreadyOpen", err)
	}
	if got := countType(outboxTypes(t, pool), lateType); got != 2 {
		t.Fatalf("AsnLateForArrival outbox rows after the replay = %d: exactly-once must hold", got)
	}

	// An on-time ASN under the later appointment, and a walk-in, raise none.
	if _, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-ONTIME", AppointmentID: laterAppt}); err != nil {
		t.Fatal(err)
	}
	registerAsn(t, w, "ASN-WALKIN")
	if _, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-WALKIN"}); err != nil {
		t.Fatal(err)
	}
	if got := countType(outboxTypes(t, pool), lateType); got != 2 {
		t.Fatalf("AsnLateForArrival outbox rows = %d: an on-time ASN and a walk-in must raise nothing", got)
	}
}
