package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// loadReceiptFindings reads the carton discrepancies of the given receipts, in
// raise order.
func loadReceiptFindings(ctx context.Context, q querier, ids []string) (map[string][]receipt.CartonFindingState, error) {
	rows, err := q.Query(ctx, `SELECT receipt_id, kind, sscc, expected, actual, raised_at
		FROM receipt_carton_discrepancies WHERE receipt_id = ANY($1) ORDER BY receipt_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("load receipt carton discrepancies: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]receipt.CartonFindingState, len(ids))
	for rows.Next() {
		var id string
		var sscc, expected, actual *string
		var raisedAt time.Time
		var f receipt.CartonFindingState
		if err := rows.Scan(&id, &f.Kind, &sscc, &expected, &actual, &raisedAt); err != nil {
			return nil, fmt.Errorf("scan receipt carton discrepancy: %w", err)
		}
		f.SSCC, f.Expected, f.Actual, f.At = deref(sscc), deref(expected), deref(actual), raisedAt.UTC()
		out[id] = append(out[id], f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load receipt carton discrepancies: %w", err)
	}
	return out, nil
}

// insertReceiptFindings appends the carton discrepancies of x that are not
// stored yet, in ONE statement and the caller's transaction (the same one that
// writes the receipt and its outbox events). The log is append-only and a
// stale writer is already refused by the version guard, so an existing
// position is left alone.
func insertReceiptFindings(ctx context.Context, q querier, x *receipt.Receipt) error {
	findings := x.Persisted().CartonDiscrepancies
	if len(findings) == 0 {
		return nil
	}
	n := len(findings)
	positions, kinds, ssccs := make([]int32, n), make([]string, n), make([]string, n)
	expected, actual, raisedAt := make([]string, n), make([]string, n), make([]string, n)
	for i, f := range findings {
		positions[i], kinds[i], ssccs[i] = int32(i+1), f.Kind, f.SSCC
		expected[i], actual[i], raisedAt[i] = f.Expected, f.Actual, f.At.UTC().Format(time.RFC3339Nano)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO receipt_carton_discrepancies (receipt_id, position, kind, sscc, expected, actual, raised_at)
		SELECT $1, t.position, t.kind, NULLIF(t.sscc, ''), NULLIF(t.expected, ''), NULLIF(t.actual, ''), t.raised_at::timestamptz
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
		     AS t(position, kind, sscc, expected, actual, raised_at)
		ON CONFLICT (receipt_id, position) DO NOTHING`,
		string(x.ID()), positions, kinds, ssccs, expected, actual, raisedAt); err != nil {
		return fmt.Errorf("insert carton discrepancies of receipt %s: %w", x.ID(), err)
	}
	return nil
}
