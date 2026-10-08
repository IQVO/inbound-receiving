package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
)

// IdempotencyStore is the pgx-backed ports.IdempotencyStore over
// idempotency_keys.
//
// # Why a committed row always has its outcome
//
// Do inserts the key, runs the handler on the SAME transaction (the ctx it
// hands over carries it, see package pgtx), writes status_code, body and
// headers with one UPDATE and only then commits. A row is therefore either
// not committed at all (rolled back, invisible to everyone) or committed
// with its outcome set: a reader never sees a half-written row and needs no
// "in progress" state, polling or timeout.
//
// # Concurrency
//
// Two requests with one key race on INSERT ... ON CONFLICT DO NOTHING. The
// second inserter blocks on the primary-key index until the first
// transaction ends, so when it sees zero affected rows the first has already
// resolved: the row either holds the stored response or does not exist and
// this call's own insert succeeds.
type IdempotencyStore struct {
	pool *pgxpool.Pool
}

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore { return &IdempotencyStore{pool: pool} }

var _ ports.IdempotencyStore = (*IdempotencyStore)(nil)

// Do implements ports.IdempotencyStore.
func (s *IdempotencyStore) Do(ctx context.Context, req idempotency.Request, handle func(ctx context.Context) idempotency.Response) (idempotency.Response, idempotency.Outcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("begin idempotent request: %w", err)
	}
	// A panic in handle, or any early return, rolls the transaction back, so
	// neither the key nor any write handle made survives; Rollback after a
	// successful Commit is a no-op.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (key, request_hash) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, req.Key, req.BodyHash)
	if err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("claim idempotency key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		_ = tx.Rollback(ctx)
		return s.replay(ctx, req)
	}

	resp := handle(pgtx.With(ctx, tx))
	headers, err := json.Marshal(resp.Header)
	if err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("encode idempotent response headers: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE idempotency_keys SET status_code = $2, response_body = $3, response_headers = $4, completed_at = now() WHERE key = $1`,
		req.Key, resp.Status, resp.Body, headers); err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("store idempotent response: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("commit idempotent request: %w", err)
	}
	return resp, idempotency.Fresh, nil
}

// replay answers a key that already exists. The inserting transaction has
// resolved by now (see the type comment), so a plain read is enough.
func (s *IdempotencyStore) replay(ctx context.Context, req idempotency.Request) (idempotency.Response, idempotency.Outcome, error) {
	var (
		hash    string
		status  *int
		body    []byte
		headers []byte
	)
	err := s.pool.QueryRow(ctx, `SELECT request_hash, status_code, response_body, response_headers FROM idempotency_keys WHERE key = $1`, req.Key).
		Scan(&hash, &status, &body, &headers)
	if errors.Is(err, pgx.ErrNoRows) {
		// The first transaction rolled back after our insert lost the race
		// and before our read; the caller may simply retry.
		return idempotency.Response{}, 0, errors.New("idempotency key vanished while replaying; retry the request")
	}
	if err != nil {
		return idempotency.Response{}, 0, fmt.Errorf("read idempotency key: %w", err)
	}
	if hash != req.BodyHash {
		return idempotency.Response{}, idempotency.KeyReused, nil
	}
	if status == nil {
		return idempotency.Response{}, 0, errors.New("idempotency row committed without an outcome")
	}
	resp := idempotency.Response{Status: *status, Body: body}
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &resp.Header); err != nil {
			return idempotency.Response{}, 0, fmt.Errorf("decode stored response headers: %w", err)
		}
	}
	return resp, idempotency.Replayed, nil
}
