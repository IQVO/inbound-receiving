package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

var t0 = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

func newAsn(t *testing.T, number string) *asn.Asn {
	t.Helper()
	a, _, err := asn.Register(number, "ACME", time.Time{}, []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 5}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newAppointment(t *testing.T, id, door string, start time.Time) *appointment.DockAppointment {
	t.Helper()
	d, _, err := appointment.Book(id, door, "C", start, start.Add(time.Hour), []string{"ASN-1"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func apptID(n int) string { return "appt-00000000-0000-4000-8000-00000000000" + string(rune('0'+n)) }
func rcptID(n int) string { return "rcpt-00000000-0000-4000-8000-00000000000" + string(rune('0'+n)) }

func newReceipt(t *testing.T, id, number string) *receipt.Receipt {
	t.Helper()
	a := newAsn(t, number)
	r, _, err := receipt.Open(id, a.Snapshot(), "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAsnRepoVersionGuard(t *testing.T) {
	ctx := context.Background()
	r := memory.NewAsnRepo()
	a := newAsn(t, "ASN-1")
	if err := r.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, a, 0); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("duplicate insert: %v", err)
	}
	got, err := r.Get(ctx, "ASN-1")
	if err != nil || got.Version() != 1 {
		t.Fatalf("get: %v %v", got, err)
	}
	if _, err := got.Cancel("", t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, got, 1); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := r.Save(ctx, got, 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("stale update: %v", err)
	}
	if err := r.Save(ctx, newAsn(t, "ASN-2"), 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("update of a missing row: %v", err)
	}
	if _, err := r.Get(ctx, "ASN-2"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestAsnRepoGetReturnsAnIndependentCopy(t *testing.T) {
	ctx := context.Background()
	r := memory.NewAsnRepo()
	if err := r.Save(ctx, newAsn(t, "ASN-1"), 0); err != nil {
		t.Fatal(err)
	}
	loaded, _ := r.Get(ctx, "ASN-1")
	if _, err := loaded.Cancel("", t0); err != nil {
		t.Fatal(err)
	}
	stored, _ := r.Get(ctx, "ASN-1")
	if stored.State() != asn.Registered {
		t.Fatal("mutating a loaded ASN changed the stored one")
	}
}

func TestAsnRepoListPagesAndFilters(t *testing.T) {
	ctx := context.Background()
	r := memory.NewAsnRepo()
	for _, n := range []string{"C", "A", "B"} {
		if err := r.Save(ctx, newAsn(t, n), 0); err != nil {
			t.Fatal(err)
		}
	}
	cancelled, _ := r.Get(ctx, "B")
	if _, err := cancelled.Cancel("", t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, cancelled, 1); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		filter repository.AsnFilter
		after  asn.Number
		limit  int
		want   string
	}{
		{"all", repository.AsnFilter{}, "", 10, "ABC"},
		{"after", repository.AsnFilter{}, "A", 10, "BC"},
		{"limit", repository.AsnFilter{}, "", 2, "AB"},
		{"state", repository.AsnFilter{State: asn.Cancelled}, "", 10, "B"},
		{"state and after", repository.AsnFilter{State: asn.Registered}, "A", 10, "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.List(ctx, tc.filter, tc.after, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			s := ""
			for _, a := range got {
				s += string(a.Number())
			}
			if s != tc.want {
				t.Fatalf("got %q, want %q", s, tc.want)
			}
		})
	}
}

func TestAppointmentRepoVersionGuardAndActive(t *testing.T) {
	ctx := context.Background()
	r := memory.NewAppointmentRepo()
	d := newAppointment(t, apptID(1), "DOOR-1", t0.Add(time.Hour))
	if err := r.Save(ctx, d, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, d, 0); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("duplicate insert: %v", err)
	}
	got, err := r.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.Cancel("", t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, got, 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := r.Get(ctx, appointment.ID(apptID(9))); !errors.Is(err, repository.ErrAppointmentNotFound) {
		t.Fatalf("missing: %v", err)
	}
	active, err := r.ActiveOnDoor(ctx, "DOOR-1")
	if err != nil || len(active) != 0 {
		t.Fatalf("a cancelled appointment is not active: %v %v", active, err)
	}
	if err := r.LockDoor(ctx, "DOOR-1"); err != nil {
		t.Fatal(err)
	}
}

func TestAppointmentRepoListOrdersByWindowStartThenID(t *testing.T) {
	ctx := context.Background()
	r := memory.NewAppointmentRepo()
	seed := []struct {
		id    string
		door  string
		start time.Time
	}{
		{apptID(3), "DOOR-1", t0.Add(3 * time.Hour)},
		{apptID(2), "DOOR-2", t0.Add(time.Hour)},
		{apptID(1), "DOOR-1", t0.Add(time.Hour)},
	}
	for _, s := range seed {
		if err := r.Save(ctx, newAppointment(t, s.id, s.door, s.start), 0); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(items []*appointment.DockAppointment) string {
		s := ""
		for _, d := range items {
			s += string(d.ID())[len(d.ID())-1:]
		}
		return s
	}
	all, _ := r.List(ctx, repository.AppointmentFilter{}, repository.AppointmentCursor{}, 10)
	if ids(all) != "123" {
		t.Fatalf("order = %s", ids(all))
	}
	after := repository.AppointmentCursor{WindowStart: t0.Add(time.Hour), ID: all[0].ID()}
	rest, _ := r.List(ctx, repository.AppointmentFilter{}, after, 10)
	if ids(rest) != "23" {
		t.Fatalf("after cursor = %s", ids(rest))
	}
	door1, _ := r.List(ctx, repository.AppointmentFilter{Door: "DOOR-1", State: appointment.Booked}, repository.AppointmentCursor{}, 1)
	if ids(door1) != "1" {
		t.Fatalf("door filter + limit = %s", ids(door1))
	}
	window, _ := r.List(ctx, repository.AppointmentFilter{From: t0.Add(2*time.Hour + time.Minute), To: t0.Add(10 * time.Hour)}, repository.AppointmentCursor{}, 10)
	if ids(window) != "3" {
		t.Fatalf("from/to filter = %s", ids(window))
	}
	none, _ := r.List(ctx, repository.AppointmentFilter{To: t0.Add(30 * time.Minute)}, repository.AppointmentCursor{}, 10)
	if len(none) != 0 {
		t.Fatalf("to before every window = %s", ids(none))
	}
}

func TestReceiptRepoOneOpenPerAsn(t *testing.T) {
	ctx := context.Background()
	r := memory.NewReceiptRepo()
	first := newReceipt(t, rcptID(1), "ASN-1")
	if err := r.Save(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, newReceipt(t, rcptID(2), "ASN-1"), 0); !errors.Is(err, repository.ErrReceiptAlreadyOpen) {
		t.Fatalf("second open receipt: %v", err)
	}
	if err := r.Save(ctx, first, 0); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("duplicate id: %v", err)
	}
	open, err := r.OpenByAsn(ctx, "ASN-1")
	if err != nil || open.ID() != first.ID() {
		t.Fatalf("OpenByAsn = %v %v", open, err)
	}
	if _, err := open.Close(t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, open, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenByAsn(ctx, "ASN-1"); !errors.Is(err, repository.ErrReceiptNotFound) {
		t.Fatalf("closed receipt must not be open: %v", err)
	}
	if err := r.Save(ctx, newReceipt(t, rcptID(3), "ASN-1"), 0); err != nil {
		t.Fatalf("a new receipt after the close: %v", err)
	}
}

func TestReceiptRepoListAndGet(t *testing.T) {
	ctx := context.Background()
	r := memory.NewReceiptRepo()
	for i, n := range []string{"ASN-3", "ASN-1", "ASN-2"} {
		if err := r.Save(ctx, newReceipt(t, rcptID(3-i), n), 0); err != nil {
			t.Fatal(err)
		}
	}
	closed, _ := r.Get(ctx, receipt.ID(rcptID(2)))
	if _, err := closed.Close(t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, closed, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, receipt.ID(rcptID(9))); !errors.Is(err, repository.ErrReceiptNotFound) {
		t.Fatalf("missing: %v", err)
	}
	last := func(items []*receipt.Receipt) string {
		s := ""
		for _, x := range items {
			s += string(x.ID())[len(x.ID())-1:]
		}
		return s
	}
	all, _ := r.List(ctx, repository.ReceiptFilter{}, "", 10)
	paged, _ := r.List(ctx, repository.ReceiptFilter{}, receipt.ID(rcptID(1)), 1)
	byAsn, _ := r.List(ctx, repository.ReceiptFilter{AsnNumber: "ASN-3"}, "", 10)
	byState, _ := r.List(ctx, repository.ReceiptFilter{State: receipt.StateClosed}, "", 10)
	if last(all) != "123" || last(paged) != "2" || last(byAsn) != "3" || last(byState) != "2" {
		t.Fatalf("all=%s paged=%s byAsn=%s byState=%s", last(all), last(paged), last(byAsn), last(byState))
	}
}

func TestDirectories(t *testing.T) {
	ctx := context.Background()
	skus := memory.NewKnownSkus()
	if ok, _ := skus.Exists(ctx, "SKU-1"); ok {
		t.Fatal("empty directory knows a sku")
	}
	_ = skus.Upsert(ctx, "SKU-1")
	_ = skus.Upsert(ctx, "SKU-1")
	if ok, _ := skus.Exists(ctx, "SKU-1"); !ok {
		t.Fatal("upserted sku is unknown")
	}

	doors := memory.NewDockDoors()
	_ = doors.Upsert(ctx, repository.DockDoor{Code: "B", Flow: repository.DockFlowBoth})
	_ = doors.Upsert(ctx, repository.DockDoor{Code: "A", Flow: repository.DockFlowInbound})
	_ = doors.Upsert(ctx, repository.DockDoor{Code: "A", Flow: repository.DockFlowBoth})
	list, _ := doors.List(ctx)
	if len(list) != 2 || list[0].Code != "A" || list[0].Flow != repository.DockFlowBoth {
		t.Fatalf("list = %+v", list)
	}
	_ = doors.Remove(ctx, "A")
	_ = doors.Remove(ctx, "NEVER")
	if ok, _ := doors.Exists(ctx, "A"); ok {
		t.Fatal("removed door still exists")
	}
	if ok, _ := doors.Exists(ctx, "B"); !ok {
		t.Fatal("kept door disappeared")
	}
}

func TestUnitOfWorkRollsBackEveryParticipant(t *testing.T) {
	ctx := context.Background()
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed)
	boom := errors.New("boom")
	err := uow.Do(ctx, func(ctx context.Context) error {
		_ = asns.Save(ctx, newAsn(t, "ASN-1"), 0)
		_ = appts.Save(ctx, newAppointment(t, apptID(1), "DOOR-1", t0.Add(time.Hour)), 0)
		_ = receipts.Save(ctx, newReceipt(t, rcptID(1), "ASN-9"), 0)
		_ = skus.Upsert(ctx, "SKU-1")
		_ = doors.Upsert(ctx, repository.DockDoor{Code: "D", Flow: repository.DockFlowBoth})
		_ = ob.Insert(ctx, outbox.Message{EventType: "x"})
		_, _ = processed.Claim(ctx, "c", "id")
		// A nested Do joins the outer unit of work.
		return uow.Do(ctx, func(context.Context) error { return boom })
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := asns.Get(ctx, "ASN-1"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatal("asn survived the rollback")
	}
	if _, err := appts.Get(ctx, appointment.ID(apptID(1))); !errors.Is(err, repository.ErrAppointmentNotFound) {
		t.Fatal("appointment survived the rollback")
	}
	if _, err := receipts.Get(ctx, receipt.ID(rcptID(1))); !errors.Is(err, repository.ErrReceiptNotFound) {
		t.Fatal("receipt survived the rollback")
	}
	if ok, _ := skus.Exists(ctx, "SKU-1"); ok {
		t.Fatal("sku survived the rollback")
	}
	if list, _ := doors.List(ctx); len(list) != 0 {
		t.Fatal("door survived the rollback")
	}
	if len(ob.Messages()) != 0 || processed.Has("c", "id") {
		t.Fatal("outbox or claim survived the rollback")
	}

	if err := uow.Do(ctx, func(ctx context.Context) error { return asns.Save(ctx, newAsn(t, "ASN-1"), 0) }); err != nil {
		t.Fatal(err)
	}
	if _, err := asns.Get(ctx, "ASN-1"); err != nil {
		t.Fatal("committed asn missing")
	}
}

func TestOutboxRepoDrainStopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	ob := memory.NewOutboxRepo()
	_ = ob.Insert(ctx, outbox.Message{EventType: "a"}, outbox.Message{EventType: "b"}, outbox.Message{EventType: "c"})
	boom := errors.New("boom")
	var sent []string
	n, err := ob.Drain(ctx, 10, func(_ context.Context, m outbox.Message) error {
		if m.EventType == "b" {
			return boom
		}
		sent = append(sent, m.EventType)
		return nil
	})
	if n != 1 || !errors.Is(err, boom) || len(sent) != 1 || ob.Unpublished() != 2 || ob.LastError(1) == "" {
		t.Fatalf("n=%d err=%v sent=%v unpublished=%d", n, err, sent, ob.Unpublished())
	}
	n, err = ob.Drain(ctx, 1, func(context.Context, outbox.Message) error { return nil })
	if n != 1 || err != nil || ob.Unpublished() != 1 || ob.LastError(1) != "" {
		t.Fatalf("limited drain: n=%d err=%v unpublished=%d", n, err, ob.Unpublished())
	}
}

func TestProcessedEventRepoClaimsOnce(t *testing.T) {
	r := memory.NewProcessedEventRepo()
	ctx := context.Background()
	if ok, _ := r.Claim(ctx, "c", "1"); !ok {
		t.Fatal("first claim must succeed")
	}
	if ok, _ := r.Claim(ctx, "c", "1"); ok {
		t.Fatal("second claim must fail")
	}
	if ok, _ := r.Claim(ctx, "other", "1"); !ok {
		t.Fatal("claims are per consumer")
	}
}

func TestIdempotencyStore(t *testing.T) {
	ctx := context.Background()
	s := memory.NewIdempotencyStore()
	calls := 0
	handle := func(context.Context) idempotency.Response {
		calls++
		return idempotency.Response{Status: 201, Body: []byte("created")}
	}
	req := idempotency.Request{Key: "k1", BodyHash: "h1"}

	resp, outcome, err := s.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Fresh || resp.Status != 201 || calls != 1 {
		t.Fatalf("fresh: %+v %v %v calls=%d", resp, outcome, err, calls)
	}
	resp, outcome, _ = s.Do(ctx, req, handle)
	if outcome != idempotency.Replayed || string(resp.Body) != "created" || calls != 1 {
		t.Fatalf("replay: %+v %v calls=%d", resp, outcome, calls)
	}
	resp, outcome, _ = s.Do(ctx, idempotency.Request{Key: "k1", BodyHash: "other"}, handle)
	if outcome != idempotency.KeyReused || resp.Status != 0 || calls != 1 {
		t.Fatalf("reused: %+v %v calls=%d", resp, outcome, calls)
	}
}

func TestIdempotencyStorePanicStoresNothing(t *testing.T) {
	ctx := context.Background()
	s := memory.NewIdempotencyStore()
	req := idempotency.Request{Key: "k", BodyHash: "h"}
	func() {
		defer func() { _ = recover() }()
		_, _, _ = s.Do(ctx, req, func(context.Context) idempotency.Response { panic("boom") })
	}()
	calls := 0
	_, outcome, _ := s.Do(ctx, req, func(context.Context) idempotency.Response {
		calls++
		return idempotency.Response{Status: 200}
	})
	if outcome != idempotency.Fresh || calls != 1 {
		t.Fatalf("after a panic the key must be fresh again: %v calls=%d", outcome, calls)
	}
}

func TestIdempotencyStoreTransientResponsesAreNotStored(t *testing.T) {
	ctx := context.Background()
	s := memory.NewIdempotencyStore()
	req := idempotency.Request{Key: "k", BodyHash: "h"}
	calls := 0
	failing := func(context.Context) idempotency.Response {
		calls++
		return idempotency.Response{Status: 500, Transient: true}
	}
	for i := 0; i < 2; i++ {
		if resp, outcome, _ := s.Do(ctx, req, failing); outcome != idempotency.Fresh || resp.Status != 500 {
			t.Fatalf("attempt %d: %+v %v", i, resp, outcome)
		}
	}
	if calls != 2 {
		t.Fatalf("a transient failure must be retried: handler ran %d times", calls)
	}
}
