package ports

import (
	"context"

	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
)

// IdempotencyStore makes a resource-creating request run at most once per
// Idempotency-Key.
//
// Do runs handle exactly once for a new key, stores the response it returns
// and reports idempotency.Fresh. For a key seen with the same request it
// returns the stored response (idempotency.Replayed) WITHOUT calling handle;
// for a key seen with another request it returns idempotency.KeyReused and
// a zero response. The ctx handed to handle carries the unit of work the
// store opened: every write handle makes with it commits together with the
// stored response, or rolls back together with it (a panic stores nothing
// and propagates).
type IdempotencyStore interface {
	Do(ctx context.Context, req idempotency.Request, handle func(ctx context.Context) idempotency.Response) (idempotency.Response, idempotency.Outcome, error)
}
