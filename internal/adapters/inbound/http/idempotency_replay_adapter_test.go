package http_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
)

// replayCatalogue is every problem type the service answers, read from the
// production catalogue, plus ONE synthetic OUTCOME stand-in (no real problem
// type is an OUTCOME, ADR 0027: the mechanism is proven with the stand-in).
func replayCatalogue() []problemEntry {
	var out []problemEntry
	for _, e := range inboundhttp.ReplayCatalogue() {
		out = append(out, problemEntry{Slug: e.Slug, Status: e.Status, Class: e.Class})
	}
	s := inboundhttp.SyntheticOutcomeEntry()
	return append(out, problemEntry{Slug: s.Slug, Status: s.Status, Class: s.Class})
}

// refuseThroughProduction answers e through the production error writer.
func refuseThroughProduction(w http.ResponseWriter, r *http.Request, e problemEntry) {
	for _, c := range append(inboundhttp.ReplayCatalogue(), inboundhttp.SyntheticOutcomeEntry()) {
		if c.Slug == e.Slug && c.Status == e.Status {
			c.Refuse(w, r)
			return
		}
	}
	panic("no production problem " + e.Slug)
}

// storeAdapter is the replayAdapter over any ports.IdempotencyStore; what
// differs per store is only how writes and rows are made and counted.
type storeAdapter struct {
	store     ports.IdempotencyStore
	write     func(ctx context.Context, tag string) error
	committed func(t *testing.T, tag string) int
	keyRows   func(t *testing.T, key string) int
}

func (a *storeAdapter) Catalogue() []problemEntry { return replayCatalogue() }

func (a *storeAdapter) Middleware(_ *testing.T, next http.Handler) http.Handler {
	return inboundhttp.RequireIdempotencyKey(a.store)(next)
}

func (a *storeAdapter) Refuse(w http.ResponseWriter, r *http.Request, e problemEntry) {
	refuseThroughProduction(w, r, e)
}

func (a *storeAdapter) Write(ctx context.Context, tag string) error { return a.write(ctx, tag) }
func (a *storeAdapter) Committed(t *testing.T, tag string) int      { return a.committed(t, tag) }
func (a *storeAdapter) KeyRows(t *testing.T, key string) int        { return a.keyRows(t, key) }

// newMemoryReplayAdapter is the adapter over the in-memory store. The memory
// store has no transaction: a write is a counted call, and the key rows are
// read through the port itself (a probe with a request no stored key can
// match answers KeyReused only when a row exists, and stores nothing).
func newMemoryReplayAdapter(t *testing.T) replayAdapter {
	t.Helper()
	store := memory.NewIdempotencyStore()
	var mu sync.Mutex
	writes := map[string]int{}
	return &storeAdapter{
		store: store,
		write: func(_ context.Context, tag string) error {
			mu.Lock()
			defer mu.Unlock()
			writes[tag]++
			return nil
		},
		committed: func(_ *testing.T, tag string) int {
			mu.Lock()
			defer mu.Unlock()
			return writes[tag]
		},
		keyRows: func(t *testing.T, key string) int {
			t.Helper()
			_, outcome, err := store.Do(context.Background(), idempotency.Request{Key: key, BodyHash: "probe-matches-no-stored-request"},
				func(context.Context) idempotency.Response {
					return idempotency.Response{Status: http.StatusInternalServerError, Transient: true}
				})
			if err != nil {
				t.Fatalf("probe key %q: %v", key, err)
			}
			if outcome == idempotency.KeyReused {
				return 1
			}
			return 0
		},
	}
}

// TestReplayClassesMemoryStore runs the replay-class suite over the in-memory store.
func TestReplayClassesMemoryStore(t *testing.T) {
	runReplaySuite(t, newMemoryReplayAdapter)
}
