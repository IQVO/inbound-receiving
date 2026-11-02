//go:build integration

package postgres_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
)

// poisonTx simulates what a mapped domain conflict does to the store's
// transaction: a statement fails on the server (a unique violation, like a
// racing writer losing on a unique index), the repository translates it to a
// typed error, the handler answers 4xx -- but the transaction is ABORTED from
// then on.
func poisonTx(t *testing.T, ctx context.Context) {
	t.Helper()
	tx, ok := pgtx.From(ctx)
	if !ok {
		t.Fatal("the store did not bind a transaction into the handler context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (key, request_hash) VALUES ('dup', 'h')`); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

// TestIdempotencyStoreAbortedTxMappedTo409CommitsTheOutcome: a 409 answered
// over an aborted transaction is recorded, returned and replayed -- the
// handler never re-runs.
func TestIdempotencyStoreAbortedTxMappedTo409CommitsTheOutcome(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	store := postgres.NewIdempotencyStore(pool)
	calls := 0
	handle := func(hctx context.Context) idempotency.Response {
		calls++
		poisonTx(t, hctx)
		return idempotency.Response{Status: http.StatusConflict, Body: []byte(`{"status":409}`)}
	}
	req := idempotency.Request{Key: "k-poison", BodyHash: "h"}

	resp, outcome, err := store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Fresh || resp.Status != http.StatusConflict {
		t.Fatalf("first = %+v %v %v, want the truthful 409", resp, outcome, err)
	}
	var status int
	if err := pool.QueryRow(ctx, `SELECT status_code FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&status); err != nil || status != http.StatusConflict {
		t.Fatalf("committed outcome = %d %v, want 409", status, err)
	}
	var dup int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = 'dup'`).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("handler's partial writes survived: %d %v", dup, err)
	}

	resp, outcome, err = store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Replayed || resp.Status != http.StatusConflict || string(resp.Body) != `{"status":409}` {
		t.Fatalf("replay = %+v %v %v, want the cached 409", resp, outcome, err)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 (replay must not re-run it)", calls)
	}
}

// TestIdempotencyStoreAbortedTxWith2xxIsNeverCached: a success whose writes
// died with the transaction is a lie; nothing is cached and the key retries.
func TestIdempotencyStoreAbortedTxWith2xxIsNeverCached(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	store := postgres.NewIdempotencyStore(pool)
	calls := 0
	handle := func(hctx context.Context) idempotency.Response {
		calls++
		if calls == 1 {
			poisonTx(t, hctx)
		}
		return idempotency.Response{Status: http.StatusCreated, Body: []byte(`{"ok":true}`)}
	}
	req := idempotency.Request{Key: "k-lie", BodyHash: "h"}

	if resp, _, err := store.Do(ctx, req, handle); err == nil {
		t.Fatalf("first = %+v, want an error (the writes never happened)", resp)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a failed success left %d key rows: %v", n, err)
	}

	resp, outcome, err := store.Do(ctx, req, handle)
	if err != nil || outcome != idempotency.Fresh || resp.Status != http.StatusCreated {
		t.Fatalf("retry = %+v %v %v, want the real work to succeed", resp, outcome, err)
	}
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2", calls)
	}
}
