package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const missingReceipt = "rcpt-00000000-0000-4000-8000-0000000000ff"

// openWalkIn registers ASN-1 (two lines of 10) and opens a walk-in receipt.
func openWalkIn(t *testing.T, w *world) *receipt.Receipt {
	t.Helper()
	w.registerAsn("ASN-1", "SKU-1", "SKU-2")
	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	wantNoErr(t, err)
	return r
}

func receive(t *testing.T, w *world, id string, line int, qty int64, condition string) *receipt.Receipt {
	t.Helper()
	r, err := (&usecases.ReceiveLine{Writer: w.writer}).Handle(context.Background(), usecases.ReceiveLineCommand{ReceiptID: id, LineNo: line, Quantity: qty, Condition: condition})
	wantNoErr(t, err)
	return r
}

func TestOpenWalkInReceiptMovesTheAsnToReceiving(t *testing.T) {
	w := newWorld()
	r := openWalkIn(t, w)
	if r.DoorCode() != "" || r.AppointmentID() != "" || r.State() != receipt.StateOpen {
		t.Fatalf("receipt = %+v", r)
	}
	a, _ := w.asns.Get(context.Background(), "ASN-1")
	if a.State() != asn.Receiving {
		t.Fatalf("asn state = %s", a.State())
	}
	got := w.eventTypes()
	if got[len(got)-1] != "receipt.ReceiptOpened" {
		t.Fatalf("events = %v", got)
	}
}

func TestReceiveGoodAndDamagedAccumulates(t *testing.T) {
	w := newWorld()
	id := string(openWalkIn(t, w).ID())
	receive(t, w, id, 1, 6, "Good")
	receive(t, w, id, 1, 2, "Damaged")
	r := receive(t, w, id, 2, 15, "Good")
	if r.Version() != 4 || r.Lines()[0].ReceivedGood() != 6 || r.Lines()[0].ReceivedDamaged() != 2 {
		t.Fatalf("receipt v%d lines %+v", r.Version(), r.Lines())
	}
}

func TestReceiveLineRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	id := string(openWalkIn(t, w).ID())
	uc := &usecases.ReceiveLine{Writer: w.writer}

	_, err := uc.Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: id, LineNo: 1, Quantity: 1, Condition: "Good", ExpectedVersion: 9})
	wantErr(t, err, usecases.ErrVersionMismatch)
	_, err = uc.Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: id, LineNo: 9, Quantity: 1, Condition: "Good"})
	wantErr(t, err, receipt.ErrLineNotOnAsn)
	_, err = uc.Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: id, LineNo: 1, Quantity: 1, Condition: "Broken"})
	wantErr(t, err, receipt.ErrInvalidCondition)
	_, err = uc.Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: "x", LineNo: 1, Quantity: 1, Condition: "Good"})
	wantErr(t, err, receipt.ErrInvalidID)
	_, err = uc.Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: missingReceipt, LineNo: 1, Quantity: 1, Condition: "Good"})
	wantErr(t, err, repository.ErrReceiptNotFound)
}

func TestCloseReceiptReportsShortOverAndDamaged(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	id := string(openWalkIn(t, w).ID())
	receive(t, w, id, 1, 6, "Good")
	receive(t, w, id, 1, 2, "Damaged")
	receive(t, w, id, 2, 15, "Good")
	uc := &usecases.CloseReceipt{Writer: w.writer}

	_, err := uc.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id, ExpectedVersion: 1})
	wantErr(t, err, usecases.ErrVersionMismatch)
	closed, err := uc.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id})
	wantNoErr(t, err)

	kinds := []receipt.Kind{}
	for _, d := range closed.Discrepancies() {
		kinds = append(kinds, d.Kind)
	}
	// line 1: 8 of 10 = Short + Damaged; line 2: 15 of 10 = Over
	if closed.State() != receipt.StateClosed || !reflect.DeepEqual(kinds, []receipt.Kind{receipt.KindShort, receipt.KindDamaged, receipt.KindOver}) {
		t.Fatalf("state %s kinds %v", closed.State(), kinds)
	}
	a, _ := w.asns.Get(ctx, "ASN-1")
	if a.State() != asn.Closed {
		t.Fatalf("asn state = %s", a.State())
	}
}

func TestClosedReceiptRefusesFurtherChanges(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	id := string(openWalkIn(t, w).ID())
	_, err := (&usecases.CloseReceipt{Writer: w.writer}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id})
	wantNoErr(t, err)
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id})
	wantErr(t, err, receipt.ErrReceiptClosed)
	_, err = (&usecases.ReceiveLine{Writer: w.writer}).Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: id, LineNo: 1, Quantity: 1, Condition: "Good"})
	wantErr(t, err, receipt.ErrReceiptClosed)
}

func TestReceiptFromACheckedInAppointmentCompletesItOnClose(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	d := w.book("DOOR-1", windowAt(w, 1), "ASN-1")
	w.clock.now = windowAt(w, 1)
	_, err := (&usecases.CheckInAppointment{Writer: w.writer}).Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: string(d.ID())})
	wantNoErr(t, err)

	r, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: string(d.ID())})
	wantNoErr(t, err)
	if r.DoorCode() != "DOOR-1" || r.AppointmentID() != d.ID() {
		t.Fatalf("receipt origin = %s %s", r.DoorCode(), r.AppointmentID())
	}
	before := len(w.outbox.msgs)
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(r.ID())})
	wantNoErr(t, err)
	want := []string{"dockappointment.DockAppointmentCompleted", "receipt.ReceiptClosed"}
	if got := w.eventTypes()[before:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("close events = %v, want %v", got, want)
	}
	stored, _ := w.appointments.Get(ctx, d.ID())
	if stored.State() != appointment.Completed {
		t.Fatalf("appointment state = %s", stored.State())
	}
}

func TestOpenReceiptInputRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	open := &usecases.OpenReceipt{Writer: w.writer}

	_, err := open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "bad number"})
	wantErr(t, err, asn.ErrInvalidNumber)
	_, err = open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: "bad"})
	wantErr(t, err, appointment.ErrInvalidID)
	_, err = open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-404"})
	wantErr(t, err, usecases.ErrUnknownAsn)
	_, err = open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: "appt-00000000-0000-4000-8000-0000000000ff"})
	wantErr(t, err, usecases.ErrUnknownAppointment)
}

func TestOpenReceiptAppointmentRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	w.registerAsn("ASN-2", "SKU-2")
	d := w.book("DOOR-1", windowAt(w, 1), "ASN-2")
	open := &usecases.OpenReceipt{Writer: w.writer}

	_, err := open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: string(d.ID())})
	wantErr(t, err, usecases.ErrAsnNotOnAppointment)
	_, err = open.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2", AppointmentID: string(d.ID())})
	wantErr(t, err, usecases.ErrAppointmentNotCheckedIn)
}

func TestOneOpenReceiptPerAsn(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	openWalkIn(t, w)
	_, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	wantErr(t, err, repository.ErrReceiptAlreadyOpen)
}

func TestCancelledAsnIsNotReceivable(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-2", "SKU-2")
	_, err := (&usecases.CancelAsn{Writer: w.writer}).Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-2"})
	wantNoErr(t, err)
	_, err = (&usecases.OpenReceipt{Writer: w.writer}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2"})
	wantErr(t, err, receipt.ErrAsnNotReceivable)
}

func TestReceiptUseCasesSurfaceRepositoryFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	boom := errors.New("db down")
	w.asns.getErr = boom
	_, err := (&usecases.OpenReceipt{Writer: w.writer}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	wantErr(t, err, boom)
	w.asns.getErr = nil
	w.uow.err = boom
	_, err = (&usecases.OpenReceipt{Writer: w.writer}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	wantErr(t, err, boom)
	_, err = (&usecases.ReceiveLine{Writer: w.writer}).Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: missingReceipt, LineNo: 1, Quantity: 1, Condition: "Good"})
	wantErr(t, err, boom)
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: missingReceipt})
	wantErr(t, err, boom)
}

func TestCloseReceiptRejectsInconsistentState(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	r := openWalkIn(t, w)
	uc := &usecases.CloseReceipt{Writer: w.writer}

	// the ASN vanished
	delete(w.asns.items, "ASN-1")
	_, err := uc.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(r.ID())})
	wantErr(t, err, repository.ErrAsnNotFound)

	// the ASN is back but no longer Receiving
	w.registerAsn("ASN-1", "SKU-1")
	_, err = uc.Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(r.ID())})
	wantErr(t, err, asn.ErrAsnNotReceiving)
}
