//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const stowType = typeBase + "ReceiptStowCompleted"

var gateAt = time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC)

// stowWorld is a cartonWorld whose writer evaluates the dock-to-stock fact.
type stowWorld struct {
	*cartonWorld
	stows   *postgres.StowRepo
	arrive  *usecases.ApplyVisitCheckedIn
	stowed  *usecases.ApplyContainerStowed
	closeUC *usecases.CloseReceipt
	seq     int
}

func newStowWorld(t *testing.T) *stowWorld {
	t.Helper()
	cw := newCartonWorld(t)
	stows := postgres.NewStowRepo(cw.pool)
	cw.w.Stows = stows
	processed := postgres.NewProcessedEventRepo(cw.pool)
	return &stowWorld{
		cartonWorld: cw, stows: stows,
		arrive:  &usecases.ApplyVisitCheckedIn{Writer: cw.w, ProcessedEvents: processed},
		stowed:  &usecases.ApplyContainerStowed{Writer: cw.w, ProcessedEvents: processed},
		closeUC: &usecases.CloseReceipt{Writer: cw.w},
	}
}

// stowReceipt is a receipt opened from an appointment with two cartons
// accepted Good (their LPNs are the SSCCs).
type stowReceipt struct {
	id, appointment string
	lpnA, lpnB      string
}

// receiptOnAppointment books an appointment for a fresh two-carton ASN (with
// SSCCs of its own, they are unique per site), opens a receipt from it and
// accepts both cartons.
func (sw *stowWorld) receiptOnAppointment(t *testing.T) stowReceipt {
	t.Helper()
	ctx := context.Background()
	sw.seq++
	number := fmt.Sprintf("ASN-STOW-%d", sw.seq)
	sr := stowReceipt{appointment: fmt.Sprintf("appt-aaaaaaaa-0000-4000-8000-%012d", sw.seq)}
	for i, dst := range []*string{&sr.lpnA, &sr.lpnB} {
		s, err := gs1.NewSSCC(fmt.Sprintf("0034567890%04d%03d", sw.seq, i))
		if err != nil {
			t.Fatal(err)
		}
		*dst = s.String()
	}
	cmd := cartonAsn(number, sr.lpnA, sr.lpnB)
	for i := range cmd.Cartons {
		cmd.Cartons[i].ParentSSCC = "" // the shared pallet SSCC is unique per site
	}
	sw.register(t, cmd)
	booked := &usecases.ApplyDockAppointmentBooked{UoW: sw.w.UoW, ProcessedEvents: postgres.NewProcessedEventRepo(sw.pool), Appointments: postgres.NewAppointmentRepo(sw.pool)}
	if _, err := booked.Handle(ctx, "book-"+number, usecases.DockAppointmentBooked{
		AppointmentID: sr.appointment, Direction: "INBOUND", DoorCode: "DOOR-1", Carrier: "ACME",
		WindowStart: gateAt, WindowEnd: gateAt.Add(time.Hour), AsnNumbers: []string{number}, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	r, err := (&usecases.OpenReceipt{Writer: sw.w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: number, AppointmentID: sr.appointment})
	if err != nil {
		t.Fatal(err)
	}
	sr.id = string(r.ID())
	sw.scanSSCC(t, sr.id, sr.lpnA)
	sw.scanSSCC(t, sr.id, sr.lpnB)
	return sr
}

func (sw *stowWorld) closeReceipt(t *testing.T, id string) {
	t.Helper()
	if _, err := sw.closeUC.Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: id}); err != nil {
		t.Fatal(err)
	}
}

func (sw *stowWorld) arrival(t *testing.T, eventID, appointmentID string, at time.Time) usecases.Outcome {
	t.Helper()
	out, err := sw.arrive.Handle(context.Background(), eventID, usecases.VisitCheckedIn{VisitID: "visit-1", AppointmentID: appointmentID, ArrivedAt: at, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (sw *stowWorld) stow(t *testing.T, eventID, lpn string, at time.Time) usecases.Outcome {
	t.Helper()
	out, err := sw.stowed.Handle(context.Background(), eventID, usecases.ContainerStowed{LPN: lpn, StowedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countType(types []string, want string) int {
	n := 0
	for _, s := range types {
		if s == want {
			n++
		}
	}
	return n
}

// wantOneStowEvent asserts exactly one occurrence of ReceiptStowCompleted: one
// outbox row per topic (integration and analytics) sharing one event id.
func wantOneStowEvent(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if got := countType(outboxTypes(t, pool), stowType); got != 2 {
		t.Fatalf("ReceiptStowCompleted outbox rows = %d, want 2 (integration + analytics, one occurrence)", got)
	}
	var ids int
	if err := pool.QueryRow(context.Background(), `SELECT count(DISTINCT event_id) FROM outbox_events WHERE event_type = $1`, stowType).Scan(&ids); err != nil || ids != 1 {
		t.Fatalf("distinct event ids = %d (%v), want 1", ids, err)
	}
}

func TestStowStoreKeepsTheEarliestFactAndTheMarkerIsOnce(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	s := postgres.NewStowRepo(pool)
	for i, tc := range []struct {
		at   time.Time
		want bool
	}{{gateAt, true}, {gateAt.Add(time.Minute), false}, {gateAt, false}, {gateAt.Add(-time.Minute), true}} {
		if got, err := s.ApplyArrival(ctx, "appt-x", "visit-1", tc.at); err != nil || got != tc.want {
			t.Fatalf("arrival %d: applied = %v, %v, want %v", i, got, err, tc.want)
		}
		if got, err := s.ApplyCartonStow(ctx, "LPN-1", tc.at); err != nil || got != tc.want {
			t.Fatalf("stow %d: applied = %v, %v, want %v", i, got, err, tc.want)
		}
	}
	var at time.Time
	if err := pool.QueryRow(ctx, `SELECT arrived_at FROM visit_arrivals WHERE appointment_id = 'appt-x'`).Scan(&at); err != nil || !at.Equal(gateAt.Add(-time.Minute)) {
		t.Fatalf("arrival row = %v, %v", at, err)
	}
	if err := pool.QueryRow(ctx, `SELECT stowed_at FROM carton_stows WHERE lpn = 'LPN-1'`).Scan(&at); err != nil || !at.Equal(gateAt.Add(-time.Minute)) {
		t.Fatalf("stow row = %v, %v", at, err)
	}
	if first, err := s.Complete(ctx, "rcpt-1", gateAt); err != nil || !first {
		t.Fatalf("first marker = %v, %v", first, err)
	}
	if first, err := s.Complete(ctx, "rcpt-1", gateAt); err != nil || first {
		t.Fatalf("second marker = %v, %v, want false", first, err)
	}
}

func TestStowLookupsFindReceiptsByAppointmentAndByCarton(t *testing.T) {
	ctx := context.Background()
	sw := newStowWorld(t)
	sr := sw.receiptOnAppointment(t)
	err := sw.w.UoW.Do(ctx, func(ctx context.Context) error {
		byAppt, err := sw.stows.ReceiptsOfAppointment(ctx, "appt-aaaaaaaa-0000-4000-8000-000000000001")
		if err != nil || len(byAppt) != 1 || string(byAppt[0].ID()) != sr.id || len(byAppt[0].Cartons()) != 2 {
			t.Fatalf("by appointment = %v, %v", byAppt, err)
		}
		byCarton, err := sw.stows.ReceiptsOfCarton(ctx, sr.lpnB)
		if err != nil || len(byCarton) != 1 || string(byCarton[0].ID()) != sr.id {
			t.Fatalf("by carton = %v, %v", byCarton, err)
		}
		if none, err := sw.stows.ReceiptsOfCarton(ctx, "LPN-nobody"); err != nil || len(none) != 0 {
			t.Fatalf("unknown carton = %v, %v", none, err)
		}
		if none, err := sw.stows.ReceiptsOfAppointment(ctx, "appt-unknown"); err != nil || len(none) != 0 {
			t.Fatalf("unknown appointment = %v, %v", none, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStowCompletesInEveryOrderOverPostgres(t *testing.T) {
	orders := map[string]func(t *testing.T, sw *stowWorld, sr stowReceipt){
		"arrival last": func(t *testing.T, sw *stowWorld, sr stowReceipt) {
			sw.closeReceipt(t, sr.id)
			sw.stow(t, "s1", sr.lpnA, gateAt.Add(2*time.Hour))
			sw.stow(t, "s2", sr.lpnB, gateAt.Add(3*time.Hour))
			sw.arrival(t, "v1", sr.appointment, gateAt)
		},
		"last stow last": func(t *testing.T, sw *stowWorld, sr stowReceipt) {
			sw.closeReceipt(t, sr.id)
			sw.arrival(t, "v1", sr.appointment, gateAt)
			sw.stow(t, "s1", sr.lpnA, gateAt.Add(2*time.Hour))
			sw.stow(t, "s2", sr.lpnB, gateAt.Add(3*time.Hour))
		},
		"close last": func(t *testing.T, sw *stowWorld, sr stowReceipt) {
			sw.arrival(t, "v1", sr.appointment, gateAt)
			sw.stow(t, "s1", sr.lpnA, gateAt.Add(2*time.Hour))
			sw.stow(t, "s2", sr.lpnB, gateAt.Add(3*time.Hour))
			sw.closeReceipt(t, sr.id)
		},
	}
	for name, run := range orders {
		t.Run(name, func(t *testing.T) {
			sw := newStowWorld(t)
			sr := sw.receiptOnAppointment(t)
			run(t, sw, sr)
			wantOneStowEvent(t, sw.pool)
			var payload string
			if err := sw.pool.QueryRow(context.Background(), `SELECT convert_from(value, 'UTF8') FROM outbox_events WHERE event_type = $1 AND topic = 'warehouse.inbound-receiving.events'`, stowType).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"receipt_id":"` + sr.id + `"`, `"dock_to_stock_seconds":10800`, `"carton_count":2`, `"appointment_id":"` + sr.appointment + `"`} {
				if !strings.Contains(payload, want) {
					t.Fatalf("payload %s lacks %s", payload, want)
				}
			}
			// Duplicates and corrections never raise it again.
			sw.stow(t, "s3", sr.lpnA, gateAt.Add(time.Hour))
			sw.arrival(t, "v2", sr.appointment, gateAt.Add(-time.Hour))
			wantOneStowEvent(t, sw.pool)
		})
	}
}

func TestWalkInReceiptNeverCompletes(t *testing.T) {
	sw := newStowWorld(t)
	sw.register(t, cartonAsn("ASN-WALKIN", ssccA, ssccB))
	id := sw.open(t, "ASN-WALKIN")
	sw.scanSSCC(t, id, ssccA)
	sw.scanSSCC(t, id, ssccB)
	sw.closeReceipt(t, id)
	sw.stow(t, "s1", ssccA, gateAt)
	sw.stow(t, "s2", ssccB, gateAt)
	if got := countType(outboxTypes(t, sw.pool), stowType); got != 0 {
		t.Fatalf("a walk-in receipt raised %d stow rows", got)
	}
}

// TestConcurrentFactsRaiseTheEventExactlyOnce races the arrival, both stows
// and the close of one receipt, each in its own transaction, over several
// receipts: the receipt-row lock must make the last committer see the others'
// facts, and the marker must keep the event single.
func TestConcurrentFactsRaiseTheEventExactlyOnce(t *testing.T) {
	ctx := context.Background()
	sw := newStowWorld(t)
	for round := 0; round < 12; round++ {
		sr := sw.receiptOnAppointment(t)
		tag := fmt.Sprint(round)
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, 4)
		run := func(fn func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- fn()
			}()
		}
		run(func() error {
			_, err := sw.arrive.Handle(ctx, "v-"+tag, usecases.VisitCheckedIn{VisitID: "v", AppointmentID: sr.appointment, ArrivedAt: gateAt, Version: 1})
			return err
		})
		run(func() error {
			_, err := sw.stowed.Handle(ctx, "sa-"+tag, usecases.ContainerStowed{LPN: sr.lpnA, StowedAt: gateAt.Add(time.Hour)})
			return err
		})
		run(func() error {
			_, err := sw.stowed.Handle(ctx, "sb-"+tag, usecases.ContainerStowed{LPN: sr.lpnB, StowedAt: gateAt.Add(2 * time.Hour)})
			return err
		})
		run(func() error {
			_, err := sw.closeUC.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: sr.id})
			return err
		})
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		var rows int
		if err := sw.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type = $1 AND subject = $2`, stowType, sr.id).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 2 {
			t.Fatalf("round %d: ReceiptStowCompleted rows for %s = %d, want 2", round, sr.id, rows)
		}
	}
}
