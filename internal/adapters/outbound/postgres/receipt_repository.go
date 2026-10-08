package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// uniqueViolation is the SQLSTATE of a unique-index violation.
const uniqueViolation = "23505"

// oneOpenPerAsnIndex is the partial unique index of ADR 0002.
const oneOpenPerAsnIndex = "receipts_one_open_per_asn"

// ReceiptRepo is the pgx-backed ports.ReceiptRepository over receipts and
// receipt_lines. Save is version-guarded like the other repositories; the
// partial unique index receipts_one_open_per_asn makes "one open receipt per
// ASN" a database fact.
type ReceiptRepo struct {
	pool *pgxpool.Pool
}

// NewReceiptRepo constructs a ReceiptRepo over pool.
func NewReceiptRepo(pool *pgxpool.Pool) *ReceiptRepo { return &ReceiptRepo{pool: pool} }

var _ ports.ReceiptRepository = (*ReceiptRepo)(nil)

const receiptColumns = `id, asn_number, appointment_id, door_code, state, opened_at, closed_at, version`

type receiptRow struct {
	id            string
	asnNumber     string
	appointmentID *string
	doorCode      *string
	state         string
	openedAt      time.Time
	closedAt      *time.Time
	version       int64
}

func (r *receiptRow) targets() []any {
	return []any{&r.id, &r.asnNumber, &r.appointmentID, &r.doorCode, &r.state, &r.openedAt, &r.closedAt, &r.version}
}

func (r receiptRow) toReceipt(lines []receipt.LineState) (*receipt.Receipt, error) {
	p := receipt.Persisted{
		ID: r.id, AsnNumber: r.asnNumber, AppointmentID: deref(r.appointmentID), DoorCode: deref(r.doorCode),
		State: receipt.State(r.state), Lines: lines, OpenedAt: r.openedAt.UTC(), Version: r.version,
	}
	if r.closedAt != nil {
		p.ClosedAt = r.closedAt.UTC()
	}
	x, err := receipt.Rehydrate(p)
	if err != nil {
		return nil, fmt.Errorf("rehydrate receipt %s: %w", r.id, err)
	}
	return x, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Get implements ports.ReceiptRepository.
func (r *ReceiptRepo) Get(ctx context.Context, id receipt.ID) (*receipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	var row receiptRow
	err := q.QueryRow(ctx, `SELECT `+receiptColumns+` FROM receipts WHERE id = $1`, string(id)).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get receipt %s: %w", id, err)
	}
	return r.withLines(ctx, q, row)
}

// OpenByAsn implements ports.ReceiptRepository.
func (r *ReceiptRepo) OpenByAsn(ctx context.Context, number asn.Number) (*receipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	var row receiptRow
	err := q.QueryRow(ctx, `SELECT `+receiptColumns+` FROM receipts WHERE asn_number = $1 AND state = 'Open'`, string(number)).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open receipt of asn %s: %w", number, err)
	}
	return r.withLines(ctx, q, row)
}

func (r *ReceiptRepo) withLines(ctx context.Context, q querier, row receiptRow) (*receipt.Receipt, error) {
	lines, err := loadReceiptLines(ctx, q, []string{row.id})
	if err != nil {
		return nil, err
	}
	return row.toReceipt(lines[row.id])
}

func loadReceiptLines(ctx context.Context, q querier, ids []string) (map[string][]receipt.LineState, error) {
	rows, err := q.Query(ctx, `SELECT receipt_id, line_no, sku, expected_qty, received_good, received_damaged
		FROM receipt_lines WHERE receipt_id = ANY($1) ORDER BY receipt_id, line_no`, ids)
	if err != nil {
		return nil, fmt.Errorf("load receipt lines: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]receipt.LineState, len(ids))
	for rows.Next() {
		var id string
		var l receipt.LineState
		if err := rows.Scan(&id, &l.LineNo, &l.SKU, &l.ExpectedQty, &l.ReceivedGood, &l.ReceivedDamaged); err != nil {
			return nil, fmt.Errorf("scan receipt line: %w", err)
		}
		out[id] = append(out[id], l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load receipt lines: %w", err)
	}
	return out, nil
}

// Save implements ports.ReceiptRepository (see AsnRepo.Save for the
// version-guard contract). An insert that violates the one-open-receipt
// index is repository.ErrReceiptAlreadyOpen.
func (r *ReceiptRepo) Save(ctx context.Context, x *receipt.Receipt, loadedVersion int64) error {
	return inTx(ctx, r.pool, func(q querier) error {
		if loadedVersion == 0 {
			return insertReceipt(ctx, q, x)
		}
		tag, err := q.Exec(ctx, `UPDATE receipts SET state = $2, closed_at = $3, version = $4, updated_at = now()
			WHERE id = $1 AND version = $5`, string(x.ID()), string(x.State()), closedAt(x), x.Version(), loadedVersion)
		if err != nil {
			return fmt.Errorf("update receipt %s: %w", x.ID(), err)
		}
		if tag.RowsAffected() != 1 {
			return repository.ErrConcurrentModification
		}
		for _, l := range x.Lines() {
			if _, err := q.Exec(ctx, `UPDATE receipt_lines SET received_good = $3, received_damaged = $4
				WHERE receipt_id = $1 AND line_no = $2`, string(x.ID()), l.LineNo(), l.ReceivedGood(), l.ReceivedDamaged()); err != nil {
				return fmt.Errorf("update receipt line %d of %s: %w", l.LineNo(), x.ID(), err)
			}
		}
		return nil
	})
}

func closedAt(x *receipt.Receipt) *time.Time {
	if t := x.ClosedAt(); !t.IsZero() {
		return &t
	}
	return nil
}

// insertReceipt inserts inside a SAVEPOINT: a violation of the
// one-open-receipt index aborts only the savepoint, leaving the surrounding
// transaction (for example the idempotency middleware's) usable.
func insertReceipt(ctx context.Context, q querier, x *receipt.Receipt) (err error) {
	tx, ok := q.(pgx.Tx)
	if !ok {
		return errors.New("insert receipt: a transaction is required")
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin savepoint: %w", err)
	}
	defer func() { _ = sp.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := sp.Exec(ctx, `INSERT INTO receipts (`+receiptColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING`,
		string(x.ID()), string(x.AsnNumber()), optString(string(x.AppointmentID())), optString(string(x.DoorCode())),
		string(x.State()), x.OpenedAt(), closedAt(x), x.Version())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == oneOpenPerAsnIndex {
		return repository.ErrReceiptAlreadyOpen
	}
	if err != nil {
		return fmt.Errorf("insert receipt %s: %w", x.ID(), err)
	}
	if tag.RowsAffected() != 1 {
		return repository.ErrConcurrentModification
	}
	for _, l := range x.Lines() {
		if _, err := sp.Exec(ctx, `INSERT INTO receipt_lines (receipt_id, line_no, sku, expected_qty, received_good, received_damaged)
			VALUES ($1, $2, $3, $4, $5, $6)`, string(x.ID()), l.LineNo(), string(l.SKU()), l.ExpectedQty(), l.ReceivedGood(), l.ReceivedDamaged()); err != nil {
			return fmt.Errorf("insert receipt line %d of %s: %w", l.LineNo(), x.ID(), err)
		}
	}
	return sp.Commit(ctx)
}

// List implements ports.ReceiptRepository: ascending id (byte order, the
// column is COLLATE "C"), strictly after the cursor.
func (r *ReceiptRepo) List(ctx context.Context, filter repository.ReceiptFilter, after receipt.ID, limit int) ([]*receipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+receiptColumns+` FROM receipts
		WHERE id > $1 AND ($2 = '' OR asn_number = $2) AND ($3 = '' OR state = $3)
		ORDER BY id LIMIT $4`, string(after), string(filter.AsnNumber), string(filter.State), limit)
	if err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	defer rows.Close()
	var page []receiptRow
	ids := make([]string, 0, limit)
	for rows.Next() {
		var row receiptRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan receipt: %w", err)
		}
		page = append(page, row)
		ids = append(ids, row.id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	rows.Close()
	lines, err := loadReceiptLines(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*receipt.Receipt, 0, len(page))
	for _, row := range page {
		x, err := row.toReceipt(lines[row.id])
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}
