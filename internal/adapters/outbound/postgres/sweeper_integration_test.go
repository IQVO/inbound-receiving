//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
)

func seedIdempotencyKey(t *testing.T, pool *pgxpool.Pool, key string, age time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO idempotency_keys (key, request_hash, status_code, created_at, completed_at)
		VALUES ($1, 'h', 201, now() - ($2 * interval '1 second'), now() - ($2 * interval '1 second'))`,
		key, age.Seconds())
	if err != nil {
		t.Fatalf("seed idempotency key %s: %v", key, err)
	}
}

// seedOutboxRow inserts one outbox row aged `age`; published rows get
// published_at = created_at (they were relayed immediately).
func seedOutboxRow(t *testing.T, pool *pgxpool.Pool, eventType string, age time.Duration, published bool) {
	t.Helper()
	pub := "NULL"
	if published {
		pub = "now() - ($2 * interval '1 second')"
	}
	_, err := pool.Exec(context.Background(), fmt.Sprintf(`
		INSERT INTO outbox_events (event_id, topic, event_type, subject, dataschema, value, created_at, published_at)
		VALUES (gen_random_uuid()::text, 't', $1, 's', 'urn:test', '\x7b7d', now() - ($2 * interval '1 second'), %s)`, pub),
		eventType, age.Seconds())
	if err != nil {
		t.Fatalf("seed outbox row %s: %v", eventType, err)
	}
}

func seedProcessedEvent(t *testing.T, pool *pgxpool.Pool, eventID string, age time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO processed_events (consumer, event_id, processed_at)
		VALUES ('product-registry', $1, now() - ($2 * interval '1 second'))`, eventID, age.Seconds())
	if err != nil {
		t.Fatalf("seed processed event %s: %v", eventID, err)
	}
}

func countWhere(t *testing.T, pool *pgxpool.Pool, table, where string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE `+where).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// seedHousekeepingFixture seeds expired and fresh rows of the three tables.
func seedHousekeepingFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for i := 0; i < 5; i++ {
		seedIdempotencyKey(t, pool, fmt.Sprintf("old-%d", i), 48*time.Hour)
	}
	seedIdempotencyKey(t, pool, "fresh-1", time.Hour)
	seedIdempotencyKey(t, pool, "fresh-2", 0)

	seedOutboxRow(t, pool, "OldPublished", 10*24*time.Hour, true)
	seedOutboxRow(t, pool, "OldPublished", 9*24*time.Hour, true)
	seedOutboxRow(t, pool, "RecentPublished", 24*time.Hour, true)
	seedOutboxRow(t, pool, "OldUnpublished", 30*24*time.Hour, false)
	seedOutboxRow(t, pool, "FreshUnpublished", 0, false)

	seedProcessedEvent(t, pool, "evt-old-1", 40*24*time.Hour)
	seedProcessedEvent(t, pool, "evt-old-2", 31*24*time.Hour)
	seedProcessedEvent(t, pool, "evt-fresh", 24*time.Hour)
}

// TestSweeper_DeletesOnlyExpiredRows is the housekeeping contract (gap fix
// D24, ADR 0010) against a real Postgres: expired idempotency keys,
// PUBLISHED outbox rows past retention and processed_events marks past
// retention go; fresh keys, recent published rows, recent marks and --
// critically -- UNPUBLISHED outbox rows of any age (the relay's work queue,
// including rows excluded by the attempt cap) stay.
func TestSweeper_DeletesOnlyExpiredRows(t *testing.T) {
	pool := startPostgresPool(t)
	ctx := context.Background()
	seedHousekeepingFixture(t, pool)

	// Batch size 2 forces the multi-batch loop over the 5 old keys.
	s := postgres.NewSweeper(pool, postgres.WithSweepBatchSize(2))
	res, err := s.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	want := postgres.SweepResult{IdempotencyKeys: 5, OutboxEvents: 2, ProcessedEvents: 2}
	if res != want {
		t.Errorf("SweepOnce = %+v, want %+v", res, want)
	}
	if got := countWhere(t, pool, "idempotency_keys", "TRUE"); got != 2 {
		t.Errorf("idempotency keys left = %d, want the 2 fresh ones", got)
	}
	if got := countWhere(t, pool, "outbox_events", "event_type = 'OldPublished'"); got != 0 {
		t.Errorf("old published outbox rows left = %d, want 0", got)
	}
	for _, kept := range []string{"RecentPublished", "OldUnpublished", "FreshUnpublished"} {
		if got := countWhere(t, pool, "outbox_events", "event_type = '"+kept+"'"); got != 1 {
			t.Errorf("%s rows left = %d, want 1 (must not be swept)", kept, got)
		}
	}
	if got := countWhere(t, pool, "processed_events", "TRUE"); got != 1 {
		t.Errorf("processed_events left = %d, want only the fresh mark", got)
	}

	// A second pass over a clean table is a no-op.
	res, err = s.SweepOnce(ctx)
	if err != nil || res != (postgres.SweepResult{}) {
		t.Errorf("second SweepOnce = %+v, %v; want zero result, nil", res, err)
	}
}

// A TTL/retention of 0 disables that part of the sweep entirely.
func TestSweeper_ZeroTTLAndRetentionsKeepEverything(t *testing.T) {
	pool := startPostgresPool(t)

	seedIdempotencyKey(t, pool, "ancient", 365*24*time.Hour)
	seedOutboxRow(t, pool, "AncientPublished", 365*24*time.Hour, true)
	seedProcessedEvent(t, pool, "evt-ancient", 365*24*time.Hour)

	s := postgres.NewSweeper(pool, postgres.WithIdempotencyKeyTTL(0), postgres.WithOutboxRetention(0), postgres.WithProcessedEventsRetention(0))
	res, err := s.SweepOnce(context.Background())
	if err != nil || res != (postgres.SweepResult{}) {
		t.Fatalf("SweepOnce = %+v, %v; want zero result, nil", res, err)
	}
	for _, table := range []string{"idempotency_keys", "outbox_events", "processed_events"} {
		if got := countWhere(t, pool, table, "TRUE"); got != 1 {
			t.Errorf("%s = %d rows, want 1 (disabled)", table, got)
		}
	}
}

// Run sweeps immediately on start, then stops cleanly on cancel; an
// interval of 0 disables the loop (Run returns at once, nothing deleted).
func TestSweeper_RunSweepsOnStartAndStopsOnCancel(t *testing.T) {
	pool := startPostgresPool(t)
	seedIdempotencyKey(t, pool, "stale", 48*time.Hour)

	if err := postgres.NewSweeper(pool, postgres.WithSweepInterval(0)).Run(context.Background()); err != nil {
		t.Fatalf("disabled Run = %v", err)
	}
	if got := countWhere(t, pool, "idempotency_keys", "TRUE"); got != 1 {
		t.Fatalf("an interval of 0 must not sweep, %d keys left", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	s := postgres.NewSweeper(pool, postgres.WithSweepInterval(50*time.Millisecond))
	go func() { done <- s.Run(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	for countWhere(t, pool, "idempotency_keys", "TRUE") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run never swept the stale idempotency key")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}
