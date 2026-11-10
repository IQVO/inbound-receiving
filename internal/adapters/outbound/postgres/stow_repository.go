package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// StowRepo is the pgx-backed ports.StowStore over visit_arrivals,
// carton_stows and receipt_stow_completions (ADR 0021). Both copies keep the
// EARLIEST fact per key, so a redelivery, a reordering and a move of a
// container to another bin all converge. The two receipt lookups take
// SELECT ... FOR UPDATE on the receipt rows: inside a unit of work, two
// evaluations of one receipt serialise and the later one sees the earlier
// one's committed fact.
type StowRepo struct {
	pool *pgxpool.Pool
}

// NewStowRepo constructs a StowRepo over pool.
func NewStowRepo(pool *pgxpool.Pool) *StowRepo { return &StowRepo{pool: pool} }

var _ ports.StowStore = (*StowRepo)(nil)

// ApplyArrival implements ports.StowStore.
func (r *StowRepo) ApplyArrival(ctx context.Context, id appointment.ID, visitID string, arrivedAt time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO visit_arrivals (appointment_id, arrived_at, visit_id) VALUES ($1, $2, $3)
		ON CONFLICT (appointment_id) DO UPDATE SET arrived_at = EXCLUDED.arrived_at, visit_id = EXCLUDED.visit_id, updated_at = now()
		WHERE EXCLUDED.arrived_at < visit_arrivals.arrived_at`, string(id), arrivedAt.UTC(), visitID)
	if err != nil {
		return false, fmt.Errorf("upsert visit arrival %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ApplyCartonStow implements ports.StowStore.
func (r *StowRepo) ApplyCartonStow(ctx context.Context, lpn string, stowedAt time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO carton_stows (lpn, stowed_at) VALUES ($1, $2)
		ON CONFLICT (lpn) DO UPDATE SET stowed_at = EXCLUDED.stowed_at, updated_at = now()
		WHERE EXCLUDED.stowed_at < carton_stows.stowed_at`, lpn, stowedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("upsert carton stow %s: %w", lpn, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReceiptsOfAppointment implements ports.StowStore.
func (r *StowRepo) ReceiptsOfAppointment(ctx context.Context, id appointment.ID) ([]*receipt.Receipt, error) {
	return r.lockedReceipts(ctx, `SELECT `+receiptColumns+` FROM receipts WHERE appointment_id = $1 ORDER BY id FOR UPDATE`, string(id))
}

// ReceiptsOfCarton implements ports.StowStore.
func (r *StowRepo) ReceiptsOfCarton(ctx context.Context, lpn string) ([]*receipt.Receipt, error) {
	return r.lockedReceipts(ctx, `SELECT `+receiptColumns+` FROM receipts
		WHERE id IN (SELECT receipt_id FROM receipt_cartons WHERE lpn = $1) ORDER BY id FOR UPDATE`, lpn)
}

func (r *StowRepo) lockedReceipts(ctx context.Context, sql string, arg string) ([]*receipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("lock receipts: %w", err)
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
		return nil, fmt.Errorf("lock receipts: %w", err)
	}
	rows.Close()
	return rehydrateReceipts(ctx, q, page)
}

// Evidence implements ports.StowStore.
func (r *StowRepo) Evidence(ctx context.Context, x *receipt.Receipt) (repository.StowEvidence, error) {
	q := queryFor(ctx, r.pool)
	ev := repository.StowEvidence{Stows: map[string]time.Time{}}
	if id := x.AppointmentID(); id != "" {
		var at time.Time
		err := q.QueryRow(ctx, `SELECT arrived_at FROM visit_arrivals WHERE appointment_id = $1`, string(id)).Scan(&at)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return repository.StowEvidence{}, fmt.Errorf("read visit arrival %s: %w", id, err)
		}
		if err == nil {
			ev.ArrivedAt = at.UTC()
		}
	}
	lpns := x.GoodCartonLPNs()
	if len(lpns) == 0 {
		return ev, nil
	}
	rows, err := q.Query(ctx, `SELECT lpn, stowed_at FROM carton_stows WHERE lpn = ANY($1)`, lpns)
	if err != nil {
		return repository.StowEvidence{}, fmt.Errorf("read carton stows of %s: %w", x.ID(), err)
	}
	defer rows.Close()
	for rows.Next() {
		var lpn string
		var at time.Time
		if err := rows.Scan(&lpn, &at); err != nil {
			return repository.StowEvidence{}, fmt.Errorf("scan carton stow: %w", err)
		}
		ev.Stows[lpn] = at.UTC()
	}
	if err := rows.Err(); err != nil {
		return repository.StowEvidence{}, fmt.Errorf("read carton stows of %s: %w", x.ID(), err)
	}
	return ev, nil
}

// Complete implements ports.StowStore: only the call that inserts the marker
// reports true.
func (r *StowRepo) Complete(ctx context.Context, id receipt.ID, stowedAt time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO receipt_stow_completions (receipt_id, stowed_at) VALUES ($1, $2)
		ON CONFLICT (receipt_id) DO NOTHING`, string(id), stowedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("mark stow completed %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}
