package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

func republisher(w *world) *usecases.RepublishAppointments {
	return &usecases.RepublishAppointments{Records: w.appointments, Encoder: fakeLegacyEncoder{}, Outbox: w.outbox, UoW: w.uow}
}

func TestRepublishEnqueuesTheLegacyHistoryOfEveryAppointmentInIdOrder(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	booked := w.seedAppointment(appointment.Booked, 0, "DOOR-1", windowAt(w, 1), "ASN-1")
	checkedIn := w.seedAppointment(appointment.CheckedIn, 0, "DOOR-1", windowAt(w, 2), "ASN-2")
	completed := w.seedAppointment(appointment.Completed, 0, "DOOR-1", windowAt(w, 3), "ASN-3")
	cancelled := w.seedAppointment(appointment.Cancelled, 0, "DOOR-1", windowAt(w, 4), "ASN-4")

	n, err := republisher(w).Execute(ctx)
	wantNoErr(t, err)
	if n != 4 {
		t.Fatalf("republished %d, want 4", n)
	}
	type ev struct{ typ, subject string }
	var got []ev
	for _, m := range w.outbox.msgs {
		got = append(got, ev{m.EventType, m.Subject})
	}
	// ids are minted in ascending order by fakeIDs, so id order is creation order.
	want := []ev{
		{"dockappointment.DockAppointmentBooked", string(booked.ID())},
		{"dockappointment.DockAppointmentBooked", string(checkedIn.ID())},
		{"dockappointment.DockAppointmentCheckedIn", string(checkedIn.ID())},
		{"dockappointment.DockAppointmentBooked", string(completed.ID())},
		{"dockappointment.DockAppointmentCheckedIn", string(completed.ID())},
		{"dockappointment.DockAppointmentCompleted", string(completed.ID())},
		{"dockappointment.DockAppointmentBooked", string(cancelled.ID())},
		{"dockappointment.DockAppointmentCancelled", string(cancelled.ID())},
	}
	if len(got) != len(want) {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRepublishWithNothingStoredEnqueuesNothing(t *testing.T) {
	w := newWorld()
	n, err := republisher(w).Execute(context.Background())
	wantNoErr(t, err)
	if n != 0 || len(w.outbox.msgs) != 0 {
		t.Fatalf("republished %d, enqueued %d", n, len(w.outbox.msgs))
	}
}

func TestRepublishPagesThroughMoreThanOneBatch(t *testing.T) {
	w := newWorld()
	total := usecases.RepublishBatchSize*2 + 7
	for i := 0; i < total; i++ {
		w.seedAppointment(appointment.Booked, 0, "DOOR-1", windowAt(w, 1), fmt.Sprintf("ASN-%d", i))
	}
	n, err := republisher(w).Execute(context.Background())
	wantNoErr(t, err)
	if n != total || len(w.outbox.msgs) != total {
		t.Fatalf("republished %d, enqueued %d, want %d each", n, len(w.outbox.msgs), total)
	}
	if w.uow.calls != 3 {
		t.Fatalf("units of work = %d, want 3 batches (500 + 500 + 7)", w.uow.calls)
	}
}

func TestRepublishAnExactMultipleOfTheBatchSizeEndsWithAnEmptyBatch(t *testing.T) {
	w := newWorld()
	for i := 0; i < usecases.RepublishBatchSize; i++ {
		w.seedAppointment(appointment.Booked, 0, "DOOR-1", windowAt(w, 1), fmt.Sprintf("ASN-%d", i))
	}
	n, err := republisher(w).Execute(context.Background())
	wantNoErr(t, err)
	if n != usecases.RepublishBatchSize || w.uow.calls != 2 {
		t.Fatalf("republished %d in %d units of work, want %d in 2", n, w.uow.calls, usecases.RepublishBatchSize)
	}
}

func TestRepublishIsRepeatable(t *testing.T) {
	w := newWorld()
	w.seedAppointment(appointment.Booked, 0, "DOOR-1", windowAt(w, 1), "ASN-1")
	uc := republisher(w)
	for run := 1; run <= 2; run++ {
		n, err := uc.Execute(context.Background())
		wantNoErr(t, err)
		if n != 1 {
			t.Fatalf("run %d republished %d, want 1", run, n)
		}
	}
	if len(w.outbox.msgs) != 2 {
		t.Fatalf("outbox = %d, want the same history enqueued twice", len(w.outbox.msgs))
	}
}

func TestRepublishFailures(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(w *world) *usecases.RepublishAppointments{
		"list": func(w *world) *usecases.RepublishAppointments {
			w.appointments.listErr = boom
			return republisher(w)
		},
		"encode": func(w *world) *usecases.RepublishAppointments {
			uc := republisher(w)
			uc.Encoder = fakeLegacyEncoder{err: boom}
			return uc
		},
		"outbox": func(w *world) *usecases.RepublishAppointments {
			w.outbox.err = boom
			return republisher(w)
		},
		"unit of work": func(w *world) *usecases.RepublishAppointments {
			w.uow.err = boom
			return republisher(w)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			w.seedAppointment(appointment.Booked, 0, "DOOR-1", windowAt(w, 1), "ASN-1")
			n, err := setup(w).Execute(context.Background())
			wantErr(t, err, boom)
			if n != 0 {
				t.Fatalf("reported %d republished after a failure in the first batch", n)
			}
		})
	}
}
