package analyticsstore

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Analytical housekeeping defaults (gap fix D24, ADR 0010).
// cmd/inbound-projector reads HOUSEKEEPING_INTERVAL and
// PROCESSED_EVENTS_RETENTION and passes explicit values.
const (
	// DefaultSweepInterval is how often the Sweeper wakes up.
	DefaultSweepInterval = time.Hour
	// DefaultProcessedEventsRetention is how long an
	// analytics_processed_events dedupe mark is kept. It must exceed the
	// analytics topic's retention: an older redelivery is applied again,
	// which every fact upsert tolerates (LEAST / ON CONFLICT DO NOTHING).
	DefaultProcessedEventsRetention = 30 * 24 * time.Hour
	defaultSweepBatch               = 1000
)

// sweepMarksSQL deletes up to $2 marks whose event time is older than $1
// seconds, never the newest one: max(occurred_at) is the projection's
// freshness (GET /reports/freshness), so it must survive a quiet period
// longer than the retention.
const sweepMarksSQL = `
	DELETE FROM analytics_processed_events WHERE event_id IN (
		SELECT event_id FROM analytics_processed_events
		WHERE occurred_at < now() - ($1 * interval '1 second')
		  AND occurred_at < (SELECT max(occurred_at) FROM analytics_processed_events)
		ORDER BY occurred_at
		LIMIT $2
	)`

// Sweeper bounds analytics_processed_events, the projector's append-only
// dedupe table. The fact tables are the data product and are never swept.
// A retention of 0 keeps every mark; an interval of 0 disables Run. Safe in
// several projector replicas: the bounded DELETE only removes eligible rows.
type Sweeper struct {
	pool      *pgxpool.Pool
	logger    *slog.Logger
	interval  time.Duration
	retention time.Duration
	batchSize int
}

// SweeperOption configures a Sweeper.
type SweeperOption func(*Sweeper)

// WithSweepInterval overrides the default 1h interval (0 disables Run).
func WithSweepInterval(d time.Duration) SweeperOption {
	return func(s *Sweeper) { s.interval = d }
}

// WithProcessedEventsRetention overrides the default 30d retention (<=0
// keeps every mark).
func WithProcessedEventsRetention(d time.Duration) SweeperOption {
	return func(s *Sweeper) { s.retention = d }
}

// WithSweepBatchSize overrides the default 1000-row DELETE batch.
func WithSweepBatchSize(n int) SweeperOption {
	return func(s *Sweeper) {
		if n > 0 {
			s.batchSize = n
		}
	}
}

// WithSweeperLogger overrides the default slog.Default().
func WithSweeperLogger(l *slog.Logger) SweeperOption {
	return func(s *Sweeper) { s.logger = l }
}

// NewSweeper builds a Sweeper over the projector's writer pool.
func NewSweeper(pool *pgxpool.Pool, opts ...SweeperOption) *Sweeper {
	s := &Sweeper{
		pool: pool, logger: slog.Default(), interval: DefaultSweepInterval,
		retention: DefaultProcessedEventsRetention, batchSize: defaultSweepBatch,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run sweeps once at start and then every interval until ctx is cancelled.
// A failed pass is logged and retried on the next tick; Run never returns a
// non-nil error.
func (s *Sweeper) Run(ctx context.Context) error {
	if s.interval <= 0 {
		return nil
	}
	for {
		n, err := s.SweepOnce(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.logger.Error("analytics housekeeping sweep failed", "error", err, "analytics_processed_events_deleted", n)
		case n > 0:
			s.logger.Info("analytics housekeeping sweep", "analytics_processed_events_deleted", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.interval):
		}
	}
}

// SweepOnce deletes expired marks batch by batch until a batch comes back
// short and returns how many it deleted.
func (s *Sweeper) SweepOnce(ctx context.Context) (int64, error) {
	if s.retention <= 0 {
		return 0, nil
	}
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, sweepMarksSQL, s.retention.Seconds(), s.batchSize)
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(s.batchSize) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
