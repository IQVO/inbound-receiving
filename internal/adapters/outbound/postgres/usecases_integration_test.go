//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

func newWriter(pool *pgxpool.Pool) usecases.Writer {
	return usecases.Writer{
		Asns: postgres.NewAsnRepo(pool), Appointments: postgres.NewAppointmentRepo(pool), Receipts: postgres.NewReceiptRepo(pool),
		Outbox: postgres.NewOutboxRepo(pool), Encoder: outboundkafka.NewFanoutEncoder(), UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT event_type FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func registerAsn(t *testing.T, w usecases.Writer, number string) {
	t.Helper()
	_, err := (&usecases.RegisterAsn{Writer: w}).Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestUseCasesCommitStateAndOutboxAtomically drives the real use cases over
// Postgres: the aggregate rows and their outbox rows (FULL type) commit
// together, and a failing unit of work leaves neither behind.
func TestUseCasesCommitStateAndOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)
	registerAsn(t, w, "ASN-1")
	_, err := (&usecases.CancelAsn{Writer: w}).Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1", Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "com.warehouse.wms.inbound-receiving.asn."
	if got := outboxTypes(t, pool); len(got) != 4 || got[0] != prefix+"ASNRegistered" || got[2] != prefix+"ASNCancelled" || got[1] != got[0] || got[3] != got[2] {
		t.Fatalf("outbox = %v (want each event on both topics)", got)
	}

	boom := errors.New("boom")
	err = w.UoW.Do(ctx, func(ctx context.Context) error {
		a, _, err := asn.Register("ASN-2", "ACME", time.Time{}, []asn.LineInput{{LineNo: 1, SKU: "S", ExpectedQty: 1}}, time.Now())
		if err != nil {
			return err
		}
		if err := w.Asns.Save(ctx, a, 0); err != nil {
			return err
		}
		if err := w.Outbox.Insert(ctx, outbox.Message{EventID: "x", Topic: "t", EventType: "x", Subject: "ASN-2", DataSchema: "x", Value: []byte("{}")}); err != nil {
			return err
		}
		return boom
	})
	wantErr(t, err, boom)
	if _, err := w.Asns.Get(ctx, "ASN-2"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatal("the ASN of a rolled-back unit of work survived")
	}
	if got := outboxTypes(t, pool); len(got) != 4 {
		t.Fatalf("the outbox row of a rolled-back unit of work survived: %v", got)
	}
}

// TestBookAppointmentOverlapIsAtomicUnderConcurrentBookings fires many
// bookings of the SAME door window at once: the per-door advisory lock must
// let exactly one through and refuse the rest as overlaps.
func TestBookAppointmentOverlapIsAtomicUnderConcurrentBookings(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)
	const bookers = 8
	for i := 0; i < bookers; i++ {
		registerAsn(t, w, "ASN-"+string(rune('A'+i)))
	}
	start := time.Now().Add(24 * time.Hour).Truncate(time.Minute)

	var wg sync.WaitGroup
	results := make(chan error, bookers)
	for i := 0; i < bookers; i++ {
		wg.Add(1)
		go func(asnNumber string) {
			defer wg.Done()
			_, err := (&usecases.BookAppointment{Writer: w}).Handle(ctx, usecases.BookAppointmentCommand{
				DoorCode: "DOOR-1", Carrier: "C", WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{asnNumber},
			})
			results <- err
		}("ASN-" + string(rune('A'+i)))
	}
	wg.Wait()
	close(results)

	booked, overlaps := 0, 0
	for err := range results {
		switch {
		case err == nil:
			booked++
		case errors.Is(err, appointment.ErrWindowOverlap):
			overlaps++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if booked != 1 || overlaps != bookers-1 {
		t.Fatalf("booked=%d overlaps=%d, want 1 and %d", booked, overlaps, bookers-1)
	}
	active, err := w.Appointments.ActiveOnDoor(ctx, "DOOR-1")
	if err != nil || len(active) != 1 {
		t.Fatalf("active on door = %d %v", len(active), err)
	}
}

// TestBookAppointmentDifferentDoorsDoNotBlockEachOther: the lock is per
// door, so concurrent bookings of other doors all succeed.
func TestBookAppointmentDifferentDoorsDoNotBlockEachOther(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)
	registerAsn(t, w, "ASN-1")
	start := time.Now().Add(24 * time.Hour)
	doors := []string{"DOOR-1", "DOOR-2", "DOOR-3", "DOOR-4"}
	var wg sync.WaitGroup
	errs := make(chan error, len(doors))
	for _, door := range doors {
		wg.Add(1)
		go func(door string) {
			defer wg.Done()
			_, err := (&usecases.BookAppointment{Writer: w}).Handle(ctx, usecases.BookAppointmentCommand{
				DoorCode: door, Carrier: "C", WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{"ASN-1"},
			})
			errs <- err
		}(door)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("booking on a free door failed: %v", err)
		}
	}
}

func TestIdempotencyStoreFreshReplayAndReuse(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewIdempotencyStore(startPostgresPool(t))
	calls := 0
	handle := func(context.Context) idempotency.Response {
		calls++
		return idempotency.Response{Status: http.StatusCreated, Header: map[string][]string{"Etag": {`"1"`}}, Body: []byte(`{"ok":true}`)}
	}
	req := idempotency.Request{Key: "k1", BodyHash: "h1"}

	resp, outcome, err := store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Fresh || resp.Status != 201 || calls != 1 {
		t.Fatalf("fresh: %+v %v %v calls=%d", resp, outcome, err, calls)
	}
	resp, outcome, err = store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Replayed || resp.Status != 201 || string(resp.Body) != `{"ok":true}` || resp.Header["Etag"][0] != `"1"` || calls != 1 {
		t.Fatalf("replay: %+v %v %v calls=%d", resp, outcome, err, calls)
	}
	resp, outcome, err = store.Do(ctx, idempotency.Request{Key: "k1", BodyHash: "other"}, handle)
	if err != nil || outcome != idempotency.KeyReused || resp.Status != 0 || calls != 1 {
		t.Fatalf("reuse: %+v %v %v calls=%d", resp, outcome, err, calls)
	}
}

// TestIdempotencyStoreConcurrentSameKeyRunsTheHandlerOnce: the unique index
// serialises two requests racing on one key.
func TestIdempotencyStoreConcurrentSameKeyRunsTheHandlerOnce(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewIdempotencyStore(startPostgresPool(t))
	var mu sync.Mutex
	calls := 0
	handle := func(context.Context) idempotency.Response {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return idempotency.Response{Status: http.StatusCreated, Body: []byte("once")}
	}
	var wg sync.WaitGroup
	outcomes := make(chan idempotency.Outcome, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, outcome, err := store.Do(ctx, idempotency.Request{Key: "race", BodyHash: "h"}, handle)
			if err != nil || resp.Status != http.StatusCreated {
				t.Errorf("do: %+v %v", resp, err)
			}
			outcomes <- outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	fresh := 0
	for o := range outcomes {
		if o == idempotency.Fresh {
			fresh++
		}
	}
	if calls != 1 || fresh != 1 {
		t.Fatalf("handler ran %d times, %d fresh outcomes; want 1 and 1", calls, fresh)
	}
}

// TestIdempotencyStoreRollsBackAPanickingHandler: a panic stores nothing, so
// the retry runs the handler again.
func TestIdempotencyStoreRollsBackAPanickingHandler(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	store := postgres.NewIdempotencyStore(pool)
	w := newWriter(pool)
	req := idempotency.Request{Key: "boom", BodyHash: "h"}
	func() {
		defer func() { _ = recover() }()
		_, _, _ = store.Do(ctx, req, func(ctx context.Context) idempotency.Response {
			_, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
				AsnNumber: "ASN-PANIC", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}},
			})
			if err != nil {
				t.Errorf("register inside the handler: %v", err)
			}
			panic("boom")
		})
	}()
	if _, err := w.Asns.Get(ctx, "ASN-PANIC"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatalf("the write of a panicking request survived: %v", err)
	}
	_, outcome, err := store.Do(ctx, req, func(context.Context) idempotency.Response { return idempotency.Response{Status: 200} })
	if err != nil || outcome != idempotency.Fresh {
		t.Fatalf("after a panic the key must be fresh again: %v %v", outcome, err)
	}
}

// TestIdempotencyStoreJoinedUseCaseRefusalStaysStorable proves the unit of
// work joins the store's transaction inside a savepoint: a use case that
// writes and then fails leaves nothing behind, and the refusal itself is
// still stored and replayed.
func TestIdempotencyStoreJoinedUseCaseRefusalStaysStorable(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	store := postgres.NewIdempotencyStore(pool)
	w := newWriter(pool)
	registerAsn(t, w, "ASN-1")
	_, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	if err != nil {
		t.Fatal(err)
	}

	handle := func(ctx context.Context) idempotency.Response {
		// ASN-1 already has an open receipt: the insert violates the partial
		// unique index inside the joined transaction.
		_, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
		if !errors.Is(err, repository.ErrReceiptAlreadyOpen) {
			t.Errorf("open receipt: %v", err)
		}
		return idempotency.Response{Status: http.StatusConflict, Body: []byte("receipt-already-open")}
	}
	req := idempotency.Request{Key: "dup-open", BodyHash: "h"}
	resp, outcome, err := store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Fresh || resp.Status != http.StatusConflict {
		t.Fatalf("fresh refusal: %+v %v %v", resp, outcome, err)
	}
	resp, outcome, err = store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Replayed || string(resp.Body) != "receipt-already-open" {
		t.Fatalf("replayed refusal: %+v %v %v", resp, outcome, err)
	}
	if got := outboxTypes(t, pool); len(got) != 4 {
		t.Fatalf("outbox = %v, want only ASNRegistered and ReceiptOpened on both topics", got)
	}
}

// TestIdempotencyStoreTransientResponseRollsBackAndIsNotStored: a 5xx stores
// nothing and undoes the handler's writes, so the retry runs again.
func TestIdempotencyStoreTransientResponseRollsBackAndIsNotStored(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	store := postgres.NewIdempotencyStore(pool)
	w := newWriter(pool)
	req := idempotency.Request{Key: "flaky", BodyHash: "h"}
	resp, outcome, err := store.Do(ctx, req, func(ctx context.Context) idempotency.Response {
		_, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
			AsnNumber: "ASN-T", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}},
		})
		if err != nil {
			t.Errorf("register inside the handler: %v", err)
		}
		return idempotency.Response{Status: http.StatusInternalServerError, Transient: true}
	})
	if err != nil || outcome != idempotency.Fresh || resp.Status != http.StatusInternalServerError {
		t.Fatalf("transient: %+v %v %v", resp, outcome, err)
	}
	if _, err := w.Asns.Get(ctx, "ASN-T"); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatalf("the write of a failed request survived: %v", err)
	}
	_, outcome, err = store.Do(ctx, req, func(context.Context) idempotency.Response { return idempotency.Response{Status: 201} })
	if err != nil || outcome != idempotency.Fresh {
		t.Fatalf("the retry must run fresh: %v %v", outcome, err)
	}
}
