package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	yardAppt = "appt-bbbbbbbb-0000-4000-8000-000000000001"
	visitID  = "visit-5b1f0c3e-7a24-4d1e-9c55-0e7d3f2a9b10"
)

func stowSSCC(payload string) gs1.SSCC {
	s, err := gs1.NewSSCC(payload)
	if err != nil {
		panic(err)
	}
	return s
}

var (
	lpnA = stowSSCC("00345678901234567").String()
	lpnB = stowSSCC("00345678901234568").String()
)

// stowReceipt stores a CLOSED receipt of two homogeneous cartons (lpnA, lpnB,
// accepted Good) opened from appt, closed 60 minutes after the world's clock.
func (w *world) stowReceipt(appt string) *receipt.Receipt {
	g, err := gs1.NewGTIN("1000123456789")
	if err != nil {
		panic(err)
	}
	a, _, err := asn.RegisterDraft(asn.Draft{
		Number: "ASN-STOW", SupplierRef: "ACME",
		Lines: []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 30}},
		Cartons: []asn.CartonInput{
			{SSCC: lpnA, Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g.String(), Qty: 20}}},
			{SSCC: lpnB, Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g.String(), Qty: 10}}},
		},
	}, w.clock.now)
	if err != nil {
		panic(err)
	}
	r, _, err := receipt.Open(w.ids.NewReceiptID(), a.Snapshot(), appt, "", w.clock.now)
	if err != nil {
		panic(err)
	}
	if err := a.BeginReceiving(); err != nil {
		panic(err)
	}
	w.asns.items[a.Number()] = a
	for _, s := range []string{lpnA, lpnB} {
		if _, err := r.AcceptCarton(receipt.CartonScan{SSCC: stowSSCC(s[:17]), Condition: receipt.ConditionGood}, w.clock.now); err != nil {
			panic(err)
		}
	}
	w.receipts.items[r.ID()] = r
	return r
}

// closeStowReceipt closes a stored receipt directly in the fake, as a past
// CloseReceipt would have left it.
func (w *world) closeStowReceipt(r *receipt.Receipt) {
	if _, err := r.Close(w.clock.now.Add(time.Hour)); err != nil {
		panic(err)
	}
	w.receipts.items[r.ID()] = r
}

func (w *world) arrivalApplier() *usecases.ApplyVisitCheckedIn {
	return &usecases.ApplyVisitCheckedIn{Writer: w.writer, ProcessedEvents: w.processed}
}

func (w *world) stowApplier() *usecases.ApplyContainerStowed {
	return &usecases.ApplyContainerStowed{Writer: w.writer, ProcessedEvents: w.processed}
}

func arrival(at time.Time) usecases.VisitCheckedIn {
	return usecases.VisitCheckedIn{VisitID: visitID, AppointmentID: yardAppt, ArrivedAt: at, Version: 1}
}

func (w *world) arrive(t *testing.T, id string, at time.Time) usecases.Outcome {
	t.Helper()
	got, err := w.arrivalApplier().Handle(context.Background(), id, arrival(at))
	wantNoErr(t, err)
	return got
}

func (w *world) stow(t *testing.T, id, lpn string, at time.Time) usecases.Outcome {
	t.Helper()
	got, err := w.stowApplier().Handle(context.Background(), id, usecases.ContainerStowed{LPN: lpn, StowedAt: at})
	wantNoErr(t, err)
	return got
}

func (w *world) stowEvents() int {
	n := 0
	for _, t := range w.eventTypes() {
		if t == "receipt.ReceiptStowCompleted" {
			n++
		}
	}
	return n
}

var gate = time.Date(2026, 10, 9, 5, 0, 0, 0, time.UTC)

func TestStowCompletesWhenTheArrivalIsTheLastFact(t *testing.T) {
	w := newWorld()
	r := w.stowReceipt(yardAppt)
	w.closeStowReceipt(r)
	w.stow(t, "s1", lpnA, gate.Add(2*time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(3*time.Hour))
	if w.stowEvents() != 0 {
		t.Fatal("no arrival yet: the receipt must not complete")
	}
	if got := w.arrive(t, "v1", gate); got != usecases.Applied {
		t.Fatalf("outcome = %s", got)
	}
	if w.stowEvents() != 1 {
		t.Fatalf("events = %v, want one ReceiptStowCompleted", w.eventTypes())
	}
	if _, done := w.stows.done[r.ID()]; !done {
		t.Fatal("the completion marker was not written")
	}
}

func TestStowCompletesWhenTheLastStowIsTheLastFact(t *testing.T) {
	w := newWorld()
	w.closeStowReceipt(w.stowReceipt(yardAppt))
	w.arrive(t, "v1", gate)
	w.stow(t, "s1", lpnA, gate.Add(2*time.Hour))
	if w.stowEvents() != 0 {
		t.Fatal("one carton is still not stowed")
	}
	if got := w.stow(t, "s2", lpnB, gate.Add(3*time.Hour)); got != usecases.Applied {
		t.Fatalf("outcome = %s", got)
	}
	if w.stowEvents() != 1 {
		t.Fatalf("events = %v", w.eventTypes())
	}
}

func TestStowEventIsRaisedExactlyOnce(t *testing.T) {
	w := newWorld()
	w.closeStowReceipt(w.stowReceipt(yardAppt))
	w.arrive(t, "v1", gate)
	w.stow(t, "s1", lpnA, gate.Add(2*time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(3*time.Hour))
	// A redelivery of the same CloudEvents id, a correction that moves a
	// fact earlier and a new event for the same fact must not raise it again.
	if got := w.stow(t, "s2", lpnB, gate.Add(3*time.Hour)); got != usecases.Duplicate {
		t.Fatalf("redelivery outcome = %s", got)
	}
	if got := w.arrive(t, "v2", gate.Add(-time.Minute)); got != usecases.Applied {
		t.Fatalf("earlier arrival outcome = %s, want applied", got)
	}
	if got := w.stow(t, "s3", lpnA, gate.Add(time.Hour)); got != usecases.Applied {
		t.Fatalf("earlier stow outcome = %s, want applied", got)
	}
	if w.stowEvents() != 1 {
		t.Fatalf("events = %v, want exactly one ReceiptStowCompleted", w.eventTypes())
	}
}

func TestStowFactsThatDoNotMoveTheCopyAreIgnoredAndRaiseNothing(t *testing.T) {
	w := newWorld()
	w.arrive(t, "v1", gate)
	if got := w.arrive(t, "v2", gate.Add(time.Minute)); got != usecases.Ignored {
		t.Fatalf("later arrival outcome = %s, want ignored: the earliest wins", got)
	}
	if got := w.arrive(t, "v3", gate); got != usecases.Ignored {
		t.Fatalf("equal arrival outcome = %s, want ignored", got)
	}
	w.stow(t, "s1", lpnA, gate.Add(time.Hour))
	if got := w.stow(t, "s2", lpnA, gate.Add(2*time.Hour)); got != usecases.Ignored {
		t.Fatalf("later stow outcome = %s, want ignored: a move is not a new stow", got)
	}
	if got := w.stow(t, "s3", lpnA, gate.Add(time.Hour)); got != usecases.Ignored {
		t.Fatalf("equal stow outcome = %s, want ignored", got)
	}
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("events = %v", w.eventTypes())
	}
}

func TestStowWaitsForTheReceiptToCloseAndCloseRaisesIt(t *testing.T) {
	w := newWorld()
	r := w.stowReceipt(yardAppt)
	w.arrive(t, "v1", gate)
	w.stow(t, "s1", lpnA, gate.Add(2*time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(3*time.Hour))
	if w.stowEvents() != 0 {
		t.Fatal("an open receipt must not complete")
	}
	closer := &usecases.CloseReceipt{Writer: w.writer}
	if _, err := closer.Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: string(r.ID())}); err != nil {
		t.Fatal(err)
	}
	got := w.eventTypes()
	if len(got) != 2 || got[0] != "receipt.ReceiptClosed" || got[1] != "receipt.ReceiptStowCompleted" {
		t.Fatalf("events = %v, want ReceiptClosed then ReceiptStowCompleted", got)
	}
	// A later duplicate fact does not raise it again.
	w.stow(t, "s9", lpnA, gate.Add(time.Hour))
	if w.stowEvents() != 1 {
		t.Fatalf("events = %v", w.eventTypes())
	}
}

func TestCloseWithoutTheFactsRaisesOnlyReceiptClosed(t *testing.T) {
	w := newWorld()
	r := w.stowReceipt(yardAppt)
	closer := &usecases.CloseReceipt{Writer: w.writer}
	if _, err := closer.Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: string(r.ID())}); err != nil {
		t.Fatal(err)
	}
	if got := w.eventTypes(); len(got) != 1 || got[0] != "receipt.ReceiptClosed" {
		t.Fatalf("events = %v", got)
	}
	// ... and the late facts complete it.
	w.arrive(t, "v1", gate)
	w.stow(t, "s1", lpnA, gate.Add(time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(time.Hour))
	if w.stowEvents() != 1 {
		t.Fatalf("events = %v", w.eventTypes())
	}
}

func TestCloseReceiptSurfacesAStowEvaluationFailure(t *testing.T) {
	w := newWorld()
	r := w.stowReceipt(yardAppt)
	boom := errors.New("evidence")
	w.stows.evidenceErr = boom
	closer := &usecases.CloseReceipt{Writer: w.writer}
	if _, err := closer.Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: string(r.ID())}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloseReceiptWorksWithoutAStowStore(t *testing.T) {
	w := newWorld()
	r := w.stowReceipt(yardAppt)
	w.writer.Stows = nil
	closer := &usecases.CloseReceipt{Writer: w.writer}
	if _, err := closer.Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: string(r.ID())}); err != nil {
		t.Fatal(err)
	}
	if got := w.eventTypes(); len(got) != 1 {
		t.Fatalf("events = %v", got)
	}
}

func TestStowIgnoresReceiptsWithoutAnAppointmentAndFactsOfOtherCartons(t *testing.T) {
	w := newWorld()
	w.closeStowReceipt(w.stowReceipt(""))
	w.arrive(t, "v1", gate)
	w.stow(t, "s1", lpnA, gate.Add(time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(time.Hour))
	w.stow(t, "s3", "LPN-nobody", gate.Add(time.Hour))
	if len(w.outbox.msgs) != 0 {
		t.Fatalf("a walk-in receipt has no arrival; events = %v", w.eventTypes())
	}
}

func TestVisitWithoutAnAppointmentIsIgnoredAndNotClaimed(t *testing.T) {
	w := newWorld()
	got, err := w.arrivalApplier().Handle(context.Background(), "v1", usecases.VisitCheckedIn{VisitID: visitID, ArrivedAt: gate, Version: 1})
	wantNoErr(t, err)
	if got != usecases.Ignored || len(w.processed.seen) != 0 || len(w.stows.arrivals) != 0 {
		t.Fatalf("outcome = %s claims = %d arrivals = %d", got, len(w.processed.seen), len(w.stows.arrivals))
	}
}

func TestStowInvalidPayloadsAreRejectedBeforeAnythingIsClaimed(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	for name, e := range map[string]usecases.VisitCheckedIn{
		"no visit id":      {AppointmentID: yardAppt, ArrivedAt: gate, Version: 1},
		"bad appointment":  {VisitID: visitID, AppointmentID: "nope", ArrivedAt: gate, Version: 1},
		"no arrival time":  {VisitID: visitID, AppointmentID: yardAppt, Version: 1},
		"version zero":     {VisitID: visitID, AppointmentID: yardAppt, ArrivedAt: gate, Version: 0},
		"negative version": {VisitID: visitID, AppointmentID: yardAppt, ArrivedAt: gate, Version: -1},
	} {
		if _, err := w.arrivalApplier().Handle(ctx, "bad", e); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	for name, e := range map[string]usecases.ContainerStowed{
		"no lpn":       {StowedAt: gate},
		"no stow time": {LPN: lpnA},
	} {
		if _, err := w.stowApplier().Handle(ctx, "bad", e); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	if len(w.processed.seen) != 0 {
		t.Fatal("an invalid payload must claim nothing")
	}
}

func TestStowTransientFailuresAreReturnedNotInvalid(t *testing.T) {
	boom := errors.New("db down")
	ctx := context.Background()
	for name, tc := range map[string]struct {
		set func(w *world)
	}{
		"claim":    {func(w *world) { w.processed.err = boom }},
		"apply":    {func(w *world) { w.stows.applyErr = boom }},
		"lookup":   {func(w *world) { w.stows.lookupErr = boom }},
		"evidence": {func(w *world) { w.stows.evidenceErr = boom }},
		"marker":   {func(w *world) { w.stows.completeErr = boom }},
		"outbox":   {func(w *world) { w.outbox.err = boom }},
		"uow":      {func(w *world) { w.uow.err = boom }},
	} {
		w := newWorld()
		w.closeStowReceipt(w.stowReceipt(yardAppt))
		w.stow(t, "s1", lpnA, gate.Add(time.Hour))
		w.stow(t, "s2", lpnB, gate.Add(time.Hour))
		tc.set(w)
		_, err := w.arrivalApplier().Handle(ctx, "v1", arrival(gate))
		if !errors.Is(err, boom) || errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("arrival %s: err = %v", name, err)
		}
		if name == "apply" || name == "claim" || name == "uow" {
			continue
		}
		w2 := newWorld()
		w2.closeStowReceipt(w2.stowReceipt(yardAppt))
		w2.arrive(t, "v1", gate)
		w2.stow(t, "s1", lpnA, gate.Add(time.Hour))
		tc.set(w2)
		_, err = w2.stowApplier().Handle(ctx, "s2", usecases.ContainerStowed{LPN: lpnB, StowedAt: gate.Add(time.Hour)})
		if !errors.Is(err, boom) {
			t.Fatalf("stow %s: err = %v", name, err)
		}
	}
}

func TestStowEncodeFailureIsReturned(t *testing.T) {
	w := newWorld()
	w.closeStowReceipt(w.stowReceipt(yardAppt))
	w.stow(t, "s1", lpnA, gate.Add(time.Hour))
	w.stow(t, "s2", lpnB, gate.Add(time.Hour))
	boom := errors.New("encode")
	w.writer.Encoder = failingEncoder{err: boom}
	_, err := w.arrivalApplier().Handle(context.Background(), "v1", arrival(gate))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
