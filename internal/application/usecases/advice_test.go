package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const sla = 24 * time.Hour

// fakeAdvice is the in-memory ports.AdviceStore of the use case tests.
type fakeAdvice struct {
	issued   map[receipt.ID]bool // within_sla by receipt
	overdue  map[receipt.ID]time.Time
	receipts *fakeReceipts

	issueErr, candidatesErr, overdueErr error
}

func newFakeAdvice(r *fakeReceipts) *fakeAdvice {
	return &fakeAdvice{issued: map[receipt.ID]bool{}, overdue: map[receipt.ID]time.Time{}, receipts: r}
}

func (a *fakeAdvice) MarkIssued(_ context.Context, id receipt.ID, _ time.Time, within bool) (bool, error) {
	if a.issueErr != nil {
		return false, a.issueErr
	}
	if _, ok := a.issued[id]; ok {
		return false, nil
	}
	a.issued[id] = within
	return true, nil
}

func (a *fakeAdvice) OverdueCandidates(_ context.Context, cutoff time.Time, limit int) ([]*receipt.Receipt, error) {
	if a.candidatesErr != nil {
		return nil, a.candidatesErr
	}
	ids := make([]string, 0, len(a.receipts.items))
	for id := range a.receipts.items {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	var out []*receipt.Receipt
	for _, id := range ids {
		r := a.receipts.items[receipt.ID(id)]
		if _, done := a.overdue[r.ID()]; done || r.State() != receipt.StateOpen || !r.AdviceClock().Before(cutoff) {
			continue
		}
		out = append(out, cloneReceipt(r))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (a *fakeAdvice) MarkOverdue(_ context.Context, id receipt.ID, at time.Time) (bool, error) {
	if a.overdueErr != nil {
		return false, a.overdueErr
	}
	if _, ok := a.overdue[id]; ok {
		return false, nil
	}
	a.overdue[id] = at
	return true, nil
}

// capturingEncoder keeps the receipt events it is asked to encode.
type capturingEncoder struct {
	fakeEncoder
	events []receipt.Event
}

func (c *capturingEncoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	c.events = append(c.events, events...)
	return c.fakeEncoder.EncodeReceipt(events...)
}

func (c *capturingEncoder) issued() []receipt.ReceivingAdviceIssued {
	var out []receipt.ReceivingAdviceIssued
	for _, e := range c.events {
		if a, ok := e.(receipt.ReceivingAdviceIssued); ok {
			out = append(out, a)
		}
	}
	return out
}

func (c *capturingEncoder) overdue() []receipt.ReceivingAdviceOverdue {
	var out []receipt.ReceivingAdviceOverdue
	for _, e := range c.events {
		if a, ok := e.(receipt.ReceivingAdviceOverdue); ok {
			out = append(out, a)
		}
	}
	return out
}

type fakeAdviceMetrics struct {
	within, late, overdue int
}

func (m *fakeAdviceMetrics) AdviceIssued(_ context.Context, within bool) {
	if within {
		m.within++
	} else {
		m.late++
	}
}
func (m *fakeAdviceMetrics) AdviceOverdue(context.Context) { m.overdue++ }

// openWalkIn2 registers and opens a walk-in receipt for the given ASN number.
func openWalkIn2(t *testing.T, w *world, number string) *receipt.Receipt {
	t.Helper()
	w.registerAsn(number, "SKU-1", "SKU-2")
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: number})
	wantNoErr(t, err)
	return r
}

type adviceWorld struct {
	*world
	advice  *fakeAdvice
	enc     *capturingEncoder
	metrics *fakeAdviceMetrics
}

func newAdviceWorld() *adviceWorld {
	w := newWorld()
	a := &adviceWorld{world: w, advice: newFakeAdvice(w.receipts), enc: &capturingEncoder{}, metrics: &fakeAdviceMetrics{}}
	w.writer.Advice, w.writer.AdviceSLA, w.writer.Encoder = a.advice, sla, a.enc
	return a
}

func (w *adviceWorld) close(t *testing.T, id string) error {
	t.Helper()
	_, err := (&usecases.CloseReceipt{Writer: w.writer}).WithMetrics(w.metrics).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: id})
	return err
}

func (w *adviceWorld) sweep(t *testing.T, batch int) int {
	t.Helper()
	n, err := (&usecases.SweepAdviceOverdue{Writer: w.writer, Batch: batch}).WithMetrics(w.metrics).Handle(context.Background())
	wantNoErr(t, err)
	return n
}

// --- at close --------------------------------------------------------------

func TestCloseIssuesTheAdviceRightAfterReceiptClosed(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	receive(t, w.world, id, 1, 6, "Good")
	before := len(w.outbox.msgs)
	wantNoErr(t, w.close(t, id))

	want := []string{"receipt.ReceiptClosed", "receipt.ReceivingAdviceIssued"}
	if got := w.eventTypes()[before:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("close events = %v, want %v", got, want)
	}
	adv := w.enc.issued()
	if len(adv) != 1 {
		t.Fatalf("%d advices", len(adv))
	}
	a := adv[0]
	if a.DespatchAdviceRef != "ASN-1" || a.SupplierRef != "ACME" || !a.WithinSLA || a.SLA != sla || !a.At.Equal(w.clock.now) {
		t.Fatalf("advice = %+v", a)
	}
	if !a.LastCartonAt.Equal(w.clock.now) {
		t.Fatalf("last_carton_at = %v, want the time of the last receiving action %v", a.LastCartonAt, w.clock.now)
	}
	if within, ok := w.advice.issued[receipt.ID(id)]; !ok || !within {
		t.Fatalf("marker = %v %v", within, ok)
	}
	if w.metrics.within != 1 || w.metrics.late != 0 {
		t.Fatalf("metrics within/late = %d/%d", w.metrics.within, w.metrics.late)
	}
}

func TestACloseMoreThanTheSLAAfterTheLastCartonIsLate(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	receive(t, w.world, id, 1, 6, "Good")
	w.clock.now = w.clock.now.Add(sla) // exactly at the SLA: on time
	wantNoErr(t, w.close(t, id))
	if a := w.enc.issued()[0]; !a.WithinSLA {
		t.Fatalf("closing exactly at the SLA is on time: %+v", a)
	}

	id2 := string(openWalkIn2(t, w.world, "ASN-2").ID())
	receive(t, w.world, id2, 1, 6, "Good")
	w.clock.now = w.clock.now.Add(sla + time.Second)
	wantNoErr(t, w.close(t, id2))
	if a := w.enc.issued()[1]; a.WithinSLA {
		t.Fatalf("a second past the SLA is late: %+v", a)
	}
	if w.metrics.within != 1 || w.metrics.late != 1 {
		t.Fatalf("metrics within/late = %d/%d", w.metrics.within, w.metrics.late)
	}
	if w.advice.issued[receipt.ID(id2)] {
		t.Fatal("the marker must record that the advice was late")
	}
}

func TestARedeliveredCloseDoesNotIssueASecondAdvice(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	wantNoErr(t, w.close(t, id))
	n := len(w.outbox.msgs)
	wantErr(t, w.close(t, id), receipt.ErrReceiptClosed)
	if len(w.outbox.msgs) != n || len(w.enc.issued()) != 1 || w.metrics.within != 1 {
		t.Fatalf("a redelivery must add nothing: %d -> %d messages, %d advices", n, len(w.outbox.msgs), len(w.enc.issued()))
	}
}

func TestTheMarkerMakesTheAdviceExactlyOnceEvenIfTheCloseIsReplayedPastTheAggregate(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	w.advice.issued[receipt.ID(id)] = true // a marker from an earlier, rolled-forward attempt
	wantNoErr(t, w.close(t, id))
	if got := w.eventTypes(); got[len(got)-1] != "receipt.ReceiptClosed" || len(w.enc.issued()) != 0 {
		t.Fatalf("with the marker present only ReceiptClosed is raised: %v", got)
	}
	if w.metrics.within != 0 || w.metrics.late != 0 {
		t.Fatal("no advice, no metric")
	}
}

func TestAnAdviceStoreFailureRollsTheCloseBack(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	boom := errors.New("advice store down")
	w.advice.issueErr = boom
	wantErr(t, w.close(t, id), boom)
	if w.metrics.within != 0 {
		t.Fatal("nothing is recorded for a failed close")
	}
}

func TestTheDefaultSLAIs24Hours(t *testing.T) {
	w := newAdviceWorld()
	w.writer.AdviceSLA = 0
	id := string(openWalkIn(t, w.world).ID())
	wantNoErr(t, w.close(t, id))
	if a := w.enc.issued()[0]; a.SLA != 24*time.Hour || usecases.DefaultAdviceSLA != 24*time.Hour {
		t.Fatalf("sla = %v", a.SLA)
	}
}

// --- Hold ------------------------------------------------------------------

func TestHoldReceiptLineDeclaresTheHoldAndRaisesNoEvent(t *testing.T) {
	ctx := context.Background()
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	receive(t, w.world, id, 1, 8, "Good")
	n := len(w.outbox.msgs)
	hold := &usecases.HoldReceiptLine{Writer: w.writer}
	r, err := hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: id, LineNo: 1, Quantity: 3})
	wantNoErr(t, err)
	if r.Lines()[0].Held() != 3 || len(w.outbox.msgs) != n {
		t.Fatalf("held = %d, new messages = %d", r.Lines()[0].Held(), len(w.outbox.msgs)-n)
	}
	stored, _ := w.receipts.Get(ctx, receipt.ID(id))
	if stored.Lines()[0].Held() != 3 {
		t.Fatal("the hold must be saved")
	}
	// The advice carries it.
	wantNoErr(t, w.close(t, id))
	line := w.enc.issued()[0].Lines[0]
	if line.Held != 3 || line.Accepted != 5 || line.Received != 8 {
		t.Fatalf("line = %+v", line)
	}
}

func TestHoldReceiptLineRefusals(t *testing.T) {
	ctx := context.Background()
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	receive(t, w.world, id, 1, 2, "Good")
	hold := &usecases.HoldReceiptLine{Writer: w.writer}

	_, err := hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: "bad", LineNo: 1, Quantity: 1})
	wantErr(t, err, receipt.ErrInvalidID)
	_, err = hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: "rcpt-00000000-0000-4000-8000-0000000000ff", LineNo: 1, Quantity: 1})
	wantErr(t, err, repository.ErrReceiptNotFound)
	_, err = hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: id, LineNo: 1, Quantity: 1, ExpectedVersion: 99})
	wantErr(t, err, usecases.ErrVersionMismatch)
	_, err = hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: id, LineNo: 1, Quantity: 3})
	wantErr(t, err, receipt.ErrHoldExceedsGood)
	wantNoErr(t, w.close(t, id))
	_, err = hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: id, LineNo: 1, Quantity: 1})
	wantErr(t, err, receipt.ErrReceiptClosed)
}

// --- the overdue sweep -------------------------------------------------------

func TestTheSweepRaisesOverdueOncePerReceipt(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	receive(t, w.world, id, 1, 1, "Good") // last carton at clock.now

	w.clock.now = w.clock.now.Add(sla) // exactly at the SLA: still on time
	if n := w.sweep(t, 10); n != 0 || len(w.enc.overdue()) != 0 {
		t.Fatalf("at the SLA nothing is overdue: %d", n)
	}
	w.clock.now = w.clock.now.Add(time.Nanosecond)
	if n := w.sweep(t, 10); n != 1 {
		t.Fatalf("a nanosecond past the SLA the receipt is overdue: %d", n)
	}
	ev := w.enc.overdue()
	if len(ev) != 1 || ev[0].ReceiptID() != receipt.ID(id) || ev[0].SLA != sla || !ev[0].OccurredAt().Equal(w.clock.now) {
		t.Fatalf("events = %+v", ev)
	}
	if got := w.eventTypes(); got[len(got)-1] != "receipt.ReceivingAdviceOverdue" {
		t.Fatalf("events = %v", got)
	}
	// Once: the next ticks find the marker.
	w.clock.now = w.clock.now.Add(10 * sla)
	if n := w.sweep(t, 10); n != 0 || len(w.enc.overdue()) != 1 || w.metrics.overdue != 1 {
		t.Fatalf("a second sweep must be silent: %d, %d events, %d counted", n, len(w.enc.overdue()), w.metrics.overdue)
	}
}

func TestTheSweepIgnoresClosedReceiptsAndFreshOnes(t *testing.T) {
	w := newAdviceWorld()
	closed := string(openWalkIn(t, w.world).ID())
	wantNoErr(t, w.close(t, closed))
	openWalkIn2(t, w.world, "ASN-2") // opened now, nothing received
	w.clock.now = w.clock.now.Add(sla - time.Second)
	if n := w.sweep(t, 10); n != 0 {
		t.Fatalf("nothing is overdue yet: %d", n)
	}
	w.clock.now = w.clock.now.Add(2 * time.Second)
	if n := w.sweep(t, 10); n != 1 {
		t.Fatalf("only the open receipt is overdue, measured from its opening: %d", n)
	}
	if ev := w.enc.overdue()[0]; ev.AsnNumber() != "ASN-2" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestTheSweepWorksThroughABacklogInBatches(t *testing.T) {
	w := newAdviceWorld()
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3", "ASN-4", "ASN-5"} {
		openWalkIn2(t, w.world, n)
	}
	w.clock.now = w.clock.now.Add(2 * sla)
	if n := w.sweep(t, 2); n != 5 || len(w.enc.overdue()) != 5 || w.metrics.overdue != 5 {
		t.Fatalf("raised %d, events %d, counted %d, want 5 across three batches", n, len(w.enc.overdue()), w.metrics.overdue)
	}
	// An exact multiple of the batch stops cleanly too.
	w2 := newAdviceWorld()
	openWalkIn2(t, w2.world, "ASN-1")
	openWalkIn2(t, w2.world, "ASN-2")
	w2.clock.now = w2.clock.now.Add(2 * sla)
	if n := w2.sweep(t, 2); n != 2 {
		t.Fatalf("raised %d, want 2", n)
	}
}

func TestAnOverdueReceiptClosedLaterIssuesALateAdvice(t *testing.T) {
	w := newAdviceWorld()
	id := string(openWalkIn(t, w.world).ID())
	w.clock.now = w.clock.now.Add(sla + time.Hour)
	w.sweep(t, 10)
	wantNoErr(t, w.close(t, id))
	if a := w.enc.issued()[0]; a.WithinSLA {
		t.Fatal("the advice of an overdue receipt is late")
	}
	if len(w.enc.overdue()) != 1 {
		t.Fatalf("the overdue event stays: %d", len(w.enc.overdue()))
	}
}

func TestTheSweepReportsStoreFailures(t *testing.T) {
	boom := errors.New("down")
	w := newAdviceWorld()
	openWalkIn(t, w.world)
	w.clock.now = w.clock.now.Add(2 * sla)

	w.advice.candidatesErr = boom
	_, err := (&usecases.SweepAdviceOverdue{Writer: w.writer, Batch: 10}).Handle(context.Background())
	wantErr(t, err, boom)
	w.advice.candidatesErr = nil

	w.advice.overdueErr = boom
	_, err = (&usecases.SweepAdviceOverdue{Writer: w.writer, Batch: 10}).Handle(context.Background())
	wantErr(t, err, boom)
	if len(w.enc.overdue()) != 0 || w.metrics.overdue != 0 {
		t.Fatal("a failed pass raises nothing")
	}
}

func TestTheSweepIsANoopWithoutAnAdviceStore(t *testing.T) {
	w := newWorld() // no AdviceStore
	openWalkIn(t, w)
	n, err := (&usecases.SweepAdviceOverdue{Writer: w.writer}).Handle(context.Background())
	wantNoErr(t, err)
	if n != 0 {
		t.Fatalf("raised %d", n)
	}
}
