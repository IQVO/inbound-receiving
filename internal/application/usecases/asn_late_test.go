package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// lateWorld wires the OpenReceipt lateness evaluation (ADR 0025): an
// appointment covering one ASN and an arrival fact in the visit-arrival copy.
type lateWorld struct {
	*world
	appt *appointment.DockAppointment
}

func newLateWorld(t *testing.T, arrivedAt time.Time, known bool) *lateWorld {
	t.Helper()
	w := &lateWorld{world: newWorld()}
	w.appt = w.book("DOCK-01", t0.Add(time.Hour), "ASN-L")
	if known {
		w.stows.arrivals[w.appt.ID()] = repository.Arrival{VisitID: "visit-1", ArrivedAt: arrivedAt}
	}
	return w
}

func (w *lateWorld) open() (*usecases.OpenReceipt, usecases.OpenReceiptCommand) {
	return &usecases.OpenReceipt{Writer: w.writer}, usecases.OpenReceiptCommand{AsnNumber: "ASN-L", AppointmentID: string(w.appt.ID())}
}

func (w *lateWorld) lateEvents() []string {
	var out []string
	for _, m := range w.outbox.msgs {
		if m.EventType == "asn.AsnLateForArrival" {
			out = append(out, m.Subject)
		}
	}
	return out
}

// The ASN is registered at the world's clock (t0); the visit arrived at
// arrivedAt. Registered AFTER the arrival => AsnLateForArrival at open.
func TestOpenReceiptRaisesAsnLateForArrival(t *testing.T) {
	w := newLateWorld(t, t0.Add(-time.Second), true)
	w.registerAsn("ASN-L", "SKU-1")
	uc, cmd := w.open()
	if _, err := uc.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("open: %v", err)
	}
	got := w.lateEvents()
	if len(got) != 1 || got[0] != "ASN-L" {
		t.Fatalf("AsnLateForArrival events = %v, want exactly one for ASN-L", got)
	}
}

// Both comparison sides: registered == arrived and one hour earlier are on
// time; one second later is late (the boundary is strict).
func TestOpenReceiptOnTimeComparisonSides(t *testing.T) {
	cases := []struct {
		name     string
		arrived  time.Time
		wantLate bool
	}{
		{"registered equals the arrival", t0, false},
		{"registered an hour before the arrival", t0.Add(time.Hour), false},
		{"registered one second after the arrival", t0.Add(-time.Second), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newLateWorld(t, tc.arrived, true)
			w.registerAsn("ASN-L", "SKU-1")
			uc, cmd := w.open()
			if _, err := uc.Handle(context.Background(), cmd); err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := len(w.lateEvents()); (got == 1) != tc.wantLate {
				t.Fatalf("AsnLateForArrival events = %d, want late %v", got, tc.wantLate)
			}
		})
	}
}

// No arrival fact seen yet, a walk-in receipt and a receipt whose appointment
// is unknown raise nothing.
func TestOpenReceiptRaisesNothingWithoutAnArrival(t *testing.T) {
	t.Run("no arrival seen yet", func(t *testing.T) {
		w := newLateWorld(t, t0.Add(-time.Hour), false)
		w.registerAsn("ASN-L", "SKU-1")
		uc, cmd := w.open()
		if _, err := uc.Handle(context.Background(), cmd); err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := len(w.lateEvents()); got != 0 {
			t.Fatalf("AsnLateForArrival events = %d, want 0", got)
		}
	})
	t.Run("walk-in receipt", func(t *testing.T) {
		w := newLateWorld(t, t0.Add(-time.Hour), true)
		w.registerAsn("ASN-L", "SKU-1")
		if _, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-L"}); err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := len(w.lateEvents()); got != 0 {
			t.Fatalf("AsnLateForArrival events = %d, want 0", got)
		}
	})
}

// The event is exactly once per ASN: the marker makes a repeat a no-op even
// when the open itself is replayed around the one-open-receipt rule.
func TestOpenReceiptLateEventIsExactlyOnce(t *testing.T) {
	w := newLateWorld(t, t0.Add(-time.Minute), true)
	w.registerAsn("ASN-L", "SKU-1")
	if _, err := w.asns.MarkLateArrival(context.Background(), "ASN-L", string(w.appt.ID()), "visit-1", t0); err != nil {
		t.Fatalf("seed the marker: %v", err)
	}
	uc, cmd := w.open()
	if _, err := uc.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := len(w.lateEvents()); got != 0 {
		t.Fatalf("AsnLateForArrival events = %d, want 0 behind an already-marked ASN", got)
	}
}

// A second open of the same ASN is refused and raises nothing new.
func TestOpenReceiptTwiceRaisesTheLateEventOnce(t *testing.T) {
	w := newLateWorld(t, t0.Add(-time.Minute), true)
	w.registerAsn("ASN-L", "SKU-1")
	uc, cmd := w.open()
	if _, err := uc.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := uc.Handle(context.Background(), cmd); err == nil {
		t.Fatal("second open succeeded, want ErrReceiptAlreadyOpen")
	}
	if got := len(w.lateEvents()); got != 1 {
		t.Fatalf("AsnLateForArrival events = %d, want 1", got)
	}
}
