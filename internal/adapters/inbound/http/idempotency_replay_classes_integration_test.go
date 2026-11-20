//go:build integration

package http_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres/pgtx"
)

// newPostgresReplayAdapter is the adapter over the REAL Postgres store: the
// key row lives in the request's transaction, a write goes through the
// transaction the store binds into the handler's context, Committed counts on
// the pool (another connection, so only committed rows count) and KeyRows
// counts the idempotency table. Releasing a key is the rollback, so 0 rows and
// 0 committed writes are real proofs.
func newPostgresReplayAdapter(t *testing.T) replayAdapter {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, migratedDB(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE TABLE replay_probe (tag text NOT NULL)`); err != nil {
		t.Fatalf("create the probe table: %v", err)
	}
	count := func(t *testing.T, query string, arg string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, arg).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	return &storeAdapter{
		store: postgres.NewIdempotencyStore(pool),
		write: func(ctx context.Context, tag string) error {
			tx, ok := pgtx.From(ctx)
			if !ok {
				return fmt.Errorf("the store bound no transaction into the request context")
			}
			_, err := tx.Exec(ctx, `INSERT INTO replay_probe (tag) VALUES ($1)`, tag)
			return err
		},
		committed: func(t *testing.T, tag string) int {
			return count(t, `SELECT count(*) FROM replay_probe WHERE tag = $1`, tag)
		},
		keyRows: func(t *testing.T, key string) int {
			return count(t, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
		},
	}
}

// TestReplayClassesPostgresStore runs the same replay-class suite over the
// Postgres store.
func TestReplayClassesPostgresStore(t *testing.T) {
	runReplaySuite(t, newPostgresReplayAdapter)
}
