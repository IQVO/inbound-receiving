package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// AdviceRepo is the pgx-backed ports.AdviceStore over receipt_advices (ADR
// 0023): the two markers of one receipt share one row, and each is inserted
// with ON CONFLICT so only the first call reports true. The candidate lookup
// takes FOR UPDATE SKIP LOCKED on the receipt rows, so concurrent sweepers
// never take the same receipt.
type AdviceRepo struct {
	pool *pgxpool.Pool
}

// NewAdviceRepo constructs an AdviceRepo over pool.
func NewAdviceRepo(pool *pgxpool.Pool) *AdviceRepo { return &AdviceRepo{pool: pool} }

var _ ports.AdviceStore = (*AdviceRepo)(nil)

// MarkIssued implements ports.AdviceStore.
func (r *AdviceRepo) MarkIssued(ctx context.Context, id receipt.ID, issuedAt time.Time, withinSLA bool) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO receipt_advices (receipt_id, issued_at, within_sla) VALUES ($1, $2, $3)
		ON CONFLICT (receipt_id) DO UPDATE SET issued_at = EXCLUDED.issued_at, within_sla = EXCLUDED.within_sla
		WHERE receipt_advices.issued_at IS NULL`, string(id), issuedAt.UTC(), withinSLA)
	if err != nil {
		return false, fmt.Errorf("mark advice issued %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkOverdue implements ports.AdviceStore.
func (r *AdviceRepo) MarkOverdue(ctx context.Context, id receipt.ID, at time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO receipt_advices (receipt_id, overdue_at) VALUES ($1, $2)
		ON CONFLICT (receipt_id) DO UPDATE SET overdue_at = EXCLUDED.overdue_at
		WHERE receipt_advices.overdue_at IS NULL`, string(id), at.UTC())
	if err != nil {
		return false, fmt.Errorf("mark advice overdue %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// OverdueCandidates implements ports.AdviceStore. The advice clock is the last
// unit received, else the opening: the same expression as the partial index.
func (r *AdviceRepo) OverdueCandidates(ctx context.Context, cutoff time.Time, limit int) ([]*receipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+receiptColumns+` FROM receipts r
		WHERE r.state = 'Open' AND COALESCE(r.last_received_at, r.opened_at) < $1
		  AND NOT EXISTS (SELECT 1 FROM receipt_advices a WHERE a.receipt_id = r.id AND a.overdue_at IS NOT NULL)
		ORDER BY r.id LIMIT $2 FOR UPDATE OF r SKIP LOCKED`, cutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("overdue advice candidates: %w", err)
	}
	defer rows.Close()
	var page []receiptRow
	for rows.Next() {
		var row receiptRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan receipt: %w", err)
		}
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("overdue advice candidates: %w", err)
	}
	rows.Close()
	return rehydrateReceipts(ctx, q, page)
}
