package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
)

// IdempotencyKeyHeader is the request header a caller sends on a route
// wrapped by RequireIdempotencyKey or HonourIdempotencyKey.
const IdempotencyKeyHeader = "Idempotency-Key"

// maxIdempotencyKeyLength is the documented maximum of the header value.
const maxIdempotencyKeyLength = 255

// RequireIdempotencyKey wraps a resource-creating route (fleet rule
// fleet/idempotency-and-outbox.md): a missing key is 400
// idempotency-key-required, a retry with the same key and request replays
// the first response, the same key with another request is 422
// idempotency-key-reused. The store runs the handler in the same
// transaction that records the response, so writes and response are atomic.
func RequireIdempotencyKey(store ports.IdempotencyStore) func(http.Handler) http.Handler {
	return idempotencyMiddleware(store, true)
}

// HonourIdempotencyKey wraps an action route (cancel, check-in, close): the
// header is optional, and honoured exactly like the required one when sent.
func HonourIdempotencyKey(store ports.IdempotencyStore) func(http.Handler) http.Handler {
	return idempotencyMiddleware(store, false)
}

func idempotencyMiddleware(store ports.IdempotencyStore, required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" && !required {
				next.ServeHTTP(w, r)
				return
			}
			if key == "" {
				writeError(w, r, errIdempotencyKeyRequired)
				return
			}
			if len(key) > maxIdempotencyKeyLength {
				writeError(w, r, errMalformedRequest)
				return
			}
			serveIdempotent(w, r, store, key, next)
		})
	}
}

func serveIdempotent(w http.ResponseWriter, r *http.Request, store ports.IdempotencyStore, key string, next http.Handler) {
	// The body is read once, hashed and restored, so the handler decodes
	// exactly the bytes it would have without this middleware.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		writeError(w, r, errMalformedRequest)
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	req := idempotency.Request{Key: key, BodyHash: fingerprint(r, body)}
	resp, outcome, err := store.Do(r.Context(), req, func(ctx context.Context) idempotency.Response {
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))
		return idempotency.Response{
			Status:    rec.status,
			Header:    rec.header,
			Body:      rec.body.Bytes(),
			Transient: rec.status >= http.StatusInternalServerError,
		}
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "idempotency store failed", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, r, err)
		return
	}
	if outcome == idempotency.KeyReused {
		writeError(w, r, errIdempotencyKeyReused)
		return
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// fingerprint hashes method, path and body: the same key on another route or
// with another body is a different request.
func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// recorder captures the wrapped handler's response so the store can persist
// it before anything reaches the client.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
