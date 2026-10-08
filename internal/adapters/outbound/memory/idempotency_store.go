package memory

import (
	"context"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
)

// IdempotencyStore is the in-memory ports.IdempotencyStore. One mutex covers
// the lookup AND the handler run, so concurrent requests with the same key
// run the handler once and the second sees the stored response.
type IdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]storedRequest
}

type storedRequest struct {
	bodyHash string
	response idempotency.Response
}

// NewIdempotencyStore constructs an empty IdempotencyStore.
func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{entries: make(map[string]storedRequest)}
}

// Do implements ports.IdempotencyStore. A handler that panics stores
// nothing; the panic propagates and the lock is released.
func (s *IdempotencyStore) Do(ctx context.Context, req idempotency.Request, handle func(ctx context.Context) idempotency.Response) (idempotency.Response, idempotency.Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.entries[req.Key]; ok {
		if prior.bodyHash != req.BodyHash {
			return idempotency.Response{}, idempotency.KeyReused, nil
		}
		return prior.response, idempotency.Replayed, nil
	}
	resp := handle(ctx)
	s.entries[req.Key] = storedRequest{bodyHash: req.BodyHash, response: resp}
	return resp, idempotency.Fresh, nil
}
