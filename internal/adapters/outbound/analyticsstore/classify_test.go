package analyticsstore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

func TestClassify(t *testing.T) {
	for code, rejected := range map[string]bool{
		"22001": true,  // string data right truncation
		"23514": true,  // check violation
		"23505": true,  // unique violation
		"40001": false, // serialization failure: retry
		"57P01": false, // admin shutdown: retry
		"08006": false, // connection failure: retry
		"2":     false, // malformed code never panics
	} {
		err := classify(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code}))
		if errors.Is(err, report.ErrRejected) != rejected {
			t.Errorf("SQLSTATE %s: rejected = %v, want %v", code, !rejected, rejected)
		}
	}
	if err := classify(errors.New("dial tcp: refused")); errors.Is(err, report.ErrRejected) {
		t.Error("a non-Postgres error must stay transient")
	}
}
