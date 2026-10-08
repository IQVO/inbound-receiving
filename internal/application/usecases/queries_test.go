package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// seededWorld has ASN-1..3 registered and ASN-3 cancelled.
func seededWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld()
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		w.registerAsn(n, "SKU-"+n)
	}
	_, err := (&usecases.CancelAsn{Writer: w.writer}).Handle(context.Background(), usecases.CancelAsnCommand{AsnNumber: "ASN-3"})
	wantNoErr(t, err)
	return w
}

func TestListAsnsPaging(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	list := &usecases.ListAsns{Asns: w.asns}
	page, err := list.Handle(ctx, usecases.ListAsnsQuery{Limit: 2})
	wantNoErr(t, err)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %d items, cursor %q", len(page.Items), page.NextCursor)
	}
	page2, err := list.Handle(ctx, usecases.ListAsnsQuery{Limit: 2, Cursor: page.NextCursor})
	wantNoErr(t, err)
	if len(page2.Items) != 1 || page2.NextCursor != "" || page2.Items[0].Number() != "ASN-3" {
		t.Fatalf("page2 = %v", page2)
	}
	all, err := list.Handle(ctx, usecases.ListAsnsQuery{})
	wantNoErr(t, err)
	if len(all.Items) != 3 {
		t.Fatalf("default page = %d", len(all.Items))
	}
}

func TestListAsnsStateFilter(t *testing.T) {
	w := seededWorld(t)
	got, err := (&usecases.ListAsns{Asns: w.asns}).Handle(context.Background(), usecases.ListAsnsQuery{State: "Cancelled"})
	wantNoErr(t, err)
	if len(got.Items) != 1 || got.Items[0].State() != asn.Cancelled {
		t.Fatalf("filtered = %v", got.Items)
	}
}

func TestListAsnsInvalidQueries(t *testing.T) {
	w := seededWorld(t)
	list := &usecases.ListAsns{Asns: w.asns}
	for _, q := range []usecases.ListAsnsQuery{{Limit: 501}, {Limit: -1}, {Cursor: "!!"}, {Cursor: "Zm9vIGJhcg"}, {State: "Nope"}} {
		_, err := list.Handle(context.Background(), q)
		wantErr(t, err, usecases.ErrInvalidListQuery)
	}
}

func TestGetAsn(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	get := &usecases.GetAsn{Asns: w.asns}
	a, err := get.Handle(ctx, "ASN-1")
	wantNoErr(t, err)
	if a.Number() != "ASN-1" {
		t.Fatal("wrong asn")
	}
	_, err = get.Handle(ctx, "ASN-404")
	wantErr(t, err, repository.ErrAsnNotFound)
	_, err = get.Handle(ctx, "bad!")
	wantErr(t, err, asn.ErrInvalidNumber)
}

func TestListAppointmentsPagingAndFilters(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	d1 := w.book("DOOR-1", windowAt(w, 24), "ASN-1")
	d2 := w.book("DOOR-2", windowAt(w, 24), "ASN-2")
	d3 := w.book("DOOR-1", windowAt(w, 30), "ASN-1")
	list := &usecases.ListAppointments{Appointments: w.appointments}

	page, err := list.Handle(ctx, usecases.ListAppointmentsQuery{Limit: 2})
	wantNoErr(t, err)
	ids := map[appointment.ID]bool{}
	for _, it := range page.Items {
		ids[it.ID()] = true
	}
	if len(page.Items) != 2 || page.NextCursor == "" || !ids[d1.ID()] || !ids[d2.ID()] {
		t.Fatalf("page = %v / %q", ids, page.NextCursor)
	}
	page2, err := list.Handle(ctx, usecases.ListAppointmentsQuery{Limit: 2, Cursor: page.NextCursor})
	wantNoErr(t, err)
	if len(page2.Items) != 1 || page2.Items[0].ID() != d3.ID() || page2.NextCursor != "" {
		t.Fatalf("page2 = %v", page2.Items)
	}
	byDoor, err := list.Handle(ctx, usecases.ListAppointmentsQuery{Door: "DOOR-2", State: "Booked", From: windowAt(w, 0), To: windowAt(w, 100)})
	wantNoErr(t, err)
	if len(byDoor.Items) != 1 || byDoor.Items[0].ID() != d2.ID() {
		t.Fatalf("byDoor = %v", byDoor.Items)
	}
}

func TestListAppointmentsInvalidQueries(t *testing.T) {
	w := seededWorld(t)
	list := &usecases.ListAppointments{Appointments: w.appointments}
	cases := []usecases.ListAppointmentsQuery{
		{Door: "a b"}, {State: "Nope"}, {Limit: 501},
		{Cursor: "!!"}, {Cursor: "Zm9v"}, {Cursor: "Zm9vfGJhcg"}, {Cursor: "MjAyNi0xMC0wOVQwNzowMDowMFp8YmFy"},
	}
	for _, q := range cases {
		_, err := list.Handle(context.Background(), q)
		wantErr(t, err, usecases.ErrInvalidListQuery)
	}
}

func TestGetAppointment(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	d := w.book("DOOR-1", windowAt(w, 24), "ASN-1")
	get := &usecases.GetAppointment{Appointments: w.appointments}
	got, err := get.Handle(ctx, string(d.ID()))
	wantNoErr(t, err)
	if got.ID() != d.ID() {
		t.Fatal("wrong appointment")
	}
	_, err = get.Handle(ctx, "nope")
	wantErr(t, err, appointment.ErrInvalidID)
}

// twoReceipts opens receipts for ASN-1 and ASN-2 and closes the second.
func twoReceipts(t *testing.T, w *world) (open, closed *receipt.Receipt) {
	t.Helper()
	ctx := context.Background()
	uc := &usecases.OpenReceipt{Writer: w.writer}
	r1, err := uc.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	wantNoErr(t, err)
	r2, err := uc.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2"})
	wantNoErr(t, err)
	r2, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(r2.ID())})
	wantNoErr(t, err)
	return r1, r2
}

func TestListReceiptsPagingAndFilters(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	r1, r2 := twoReceipts(t, w)
	list := &usecases.ListReceipts{Receipts: w.receipts}

	page, err := list.Handle(ctx, usecases.ListReceiptsQuery{Limit: 1})
	wantNoErr(t, err)
	if len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].ID() != r1.ID() {
		t.Fatalf("page = %v / %q", page.Items, page.NextCursor)
	}
	page2, err := list.Handle(ctx, usecases.ListReceiptsQuery{Limit: 1, Cursor: page.NextCursor})
	wantNoErr(t, err)
	if len(page2.Items) != 1 || page2.NextCursor != "" || page2.Items[0].ID() != r2.ID() {
		t.Fatalf("page2 = %v", page2.Items)
	}
	closedOnly, err := list.Handle(ctx, usecases.ListReceiptsQuery{State: "Closed", AsnNumber: "ASN-2"})
	wantNoErr(t, err)
	if len(closedOnly.Items) != 1 {
		t.Fatalf("closedOnly = %d", len(closedOnly.Items))
	}
}

func TestListReceiptsInvalidQueries(t *testing.T) {
	w := seededWorld(t)
	list := &usecases.ListReceipts{Receipts: w.receipts}
	for _, q := range []usecases.ListReceiptsQuery{{Limit: 9999}, {Cursor: "!!"}, {Cursor: "Zm9v"}, {AsnNumber: "bad!"}, {State: "Nope"}} {
		_, err := list.Handle(context.Background(), q)
		wantErr(t, err, usecases.ErrInvalidListQuery)
	}
}

func TestGetReceipt(t *testing.T) {
	ctx := context.Background()
	w := seededWorld(t)
	r1, _ := twoReceipts(t, w)
	get := &usecases.GetReceipt{Receipts: w.receipts}
	got, err := get.Handle(ctx, string(r1.ID()))
	wantNoErr(t, err)
	if got.ID() != r1.ID() {
		t.Fatal("wrong receipt")
	}
	_, err = get.Handle(ctx, "nope")
	wantErr(t, err, receipt.ErrInvalidID)
	_, err = get.Handle(ctx, missingReceipt)
	wantErr(t, err, repository.ErrReceiptNotFound)
}

func TestListDocks(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := &usecases.ListDocks{Doors: w.doors, Mode: usecases.ModePermissive}
	got, err := uc.Handle(ctx)
	wantNoErr(t, err)
	if got.Mode != usecases.ModePermissive || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("empty docks = %+v", got)
	}
	w.doors.doors["B"] = repository.DockDoor{Code: "B", Flow: repository.DockFlowBoth}
	w.doors.doors["A"] = repository.DockDoor{Code: "A", Flow: repository.DockFlowInbound}
	got, err = uc.Handle(ctx)
	wantNoErr(t, err)
	if len(got.Items) != 2 || got.Items[0].Code != "A" {
		t.Fatalf("docks = %+v", got)
	}
	w.doors.err = errors.New("db down")
	_, err = uc.Handle(ctx)
	wantErr(t, err, w.doors.err)
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]usecases.Mode{"": usecases.ModePermissive, "permissive": usecases.ModePermissive, "kafka": usecases.ModeKafka} {
		got, err := usecases.ParseMode(in)
		wantNoErr(t, err)
		if got != want {
			t.Fatalf("ParseMode(%q) = %q", in, got)
		}
	}
	_, err := usecases.ParseMode("loose")
	wantErr(t, err, usecases.ErrInvalidMode)
}
