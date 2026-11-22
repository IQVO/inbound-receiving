package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
)

// lhFailingIdemStore fails every call; the error text carries a line break.
type lhFailingIdemStore struct{}

func (lhFailingIdemStore) Do(context.Context, idempotency.Request, func(context.Context) idempotency.Response) (idempotency.Response, idempotency.Outcome, error) {
	return idempotency.Response{}, 0, errors.New(lhForged)
}

// A failed idempotency store call can carry request text, and the method is
// request-derived; the "idempotency store failed" record must keep both on one line.
func TestLogHygiene_IdempotencyStoreFailureIsLoggedOnOneLine(t *testing.T) {
	values := lhRecordLogs(t)
	r := httptest.NewRequest(http.MethodPost, "/receipts", nil)
	r.Method = "POST" + lhForged
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	serveIdempotent(httptest.NewRecorder(), r, lhFailingIdemStore{}, "key-1", next)
	lhAssertOneLine(t, *values, "a failed idempotency store call")
}
