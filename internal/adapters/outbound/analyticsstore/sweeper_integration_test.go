//go:build integration

package analyticsstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedMark(t *testing.T, pool *pgxpool.Pool, id string, age time.Duration) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO analytics_processed_events (event_id, event_type, occurred_at, applied_at)
		VALUES ($1, 'ReceiptOpened', now() - ($2 * interval '1 second'), now() - ($2 * interval '1 second'))`,
		id, age.Seconds()); err != nil {
		t.Fatalf("seed mark %s: %v", id, err)
	}
}

func marks(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM analytics_processed_events`).Scan(&n); err != nil {
		t.Fatalf("count marks: %v", err)
	}
	return n
}

// TestSweeper_DeletesExpiredMarksButKeepsTheFreshnessRow pins the analytics
// half of gap fix D24 (ADR 0010) on a real Postgres: dedupe marks older
// than the retention go, in batches; fresh marks stay; and the newest mark
// always stays, even when it is itself past the retention, because
// max(occurred_at) is the projection's freshness (GET /reports/freshness).
func TestSweeper_DeletesExpiredMarksButKeepsTheFreshnessRow(t *testing.T) {
	_, pool := startAnalyticsDB(t)
	ctx := context.Background()
	for _, id := range []string{"old-1", "old-2", "old-3"} {
		seedMark(t, pool, id, 60*24*time.Hour)
	}
	seedMark(t, pool, "fresh", 24*time.Hour)

	s := NewSweeper(pool, WithSweepBatchSize(2))
	n, err := s.SweepOnce(ctx)
	if err != nil || n != 3 {
		t.Fatalf("SweepOnce = %d, %v; want the 3 expired marks", n, err)
	}
	if got := marks(t, pool); got != 1 {
		t.Fatalf("marks left = %d, want the fresh one", got)
	}

	// Only old marks left: the newest one is kept for the freshness.
	_, pool2 := startAnalyticsDB(t)
	seedMark(t, pool2, "older", 90*24*time.Hour)
	seedMark(t, pool2, "newest-but-old", 60*24*time.Hour)
	if n, err := NewSweeper(pool2).SweepOnce(ctx); err != nil || n != 1 {
		t.Fatalf("SweepOnce = %d, %v; want only the older mark", n, err)
	}
	var left string
	if err := pool2.QueryRow(ctx, `SELECT event_id FROM analytics_processed_events`).Scan(&left); err != nil || left != "newest-but-old" {
		t.Fatalf("kept %q (%v), want the newest mark", left, err)
	}
}

// A retention of 0 keeps every mark; an interval of 0 disables Run.
func TestSweeper_ZeroRetentionAndZeroIntervalDisable(t *testing.T) {
	_, pool := startAnalyticsDB(t)
	seedMark(t, pool, "ancient", 365*24*time.Hour)
	seedMark(t, pool, "newer", 300*24*time.Hour)

	if n, err := NewSweeper(pool, WithProcessedEventsRetention(0)).SweepOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("SweepOnce with retention 0 = %d, %v", n, err)
	}
	if err := NewSweeper(pool, WithSweepInterval(0)).Run(context.Background()); err != nil {
		t.Fatalf("disabled Run = %v", err)
	}
	if got := marks(t, pool); got != 2 {
		t.Fatalf("marks = %d, want both kept", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewSweeper(pool, WithSweepInterval(50*time.Millisecond)).Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for marks(t, pool) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("Run never swept the ancient mark")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
}
