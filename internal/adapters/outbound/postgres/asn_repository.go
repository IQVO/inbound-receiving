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
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// AsnRepo is the pgx-backed ports.AsnRepository over asns and asn_lines.
// Save is version-guarded: an insert never overwrites an existing number and
// an update only applies when the stored version is the loaded one. Lines
// never change after registration, so only the header row is updated.
type AsnRepo struct {
	pool *pgxpool.Pool
}

// NewAsnRepo constructs an AsnRepo over pool.
func NewAsnRepo(pool *pgxpool.Pool) *AsnRepo { return &AsnRepo{pool: pool} }

var _ ports.AsnRepository = (*AsnRepo)(nil)

const asnColumns = `asn_number, supplier_ref, expected_arrival, state, version`

type asnRow struct {
	number          string
	supplierRef     string
	expectedArrival *time.Time
	state           string
	version         int64
}

func (r *asnRow) targets() []any {
	return []any{&r.number, &r.supplierRef, &r.expectedArrival, &r.state, &r.version}
}

func (r asnRow) toAsn(lines []asn.LineInput) (*asn.Asn, error) {
	var arrival time.Time
	if r.expectedArrival != nil {
		arrival = r.expectedArrival.UTC()
	}
	a, err := asn.Rehydrate(r.number, r.supplierRef, arrival, lines, asn.State(r.state), r.version)
	if err != nil {
		return nil, fmt.Errorf("rehydrate asn %s: %w", r.number, err)
	}
	return a, nil
}

// Get implements ports.AsnRepository.
func (r *AsnRepo) Get(ctx context.Context, number asn.Number) (*asn.Asn, error) {
	q := queryFor(ctx, r.pool)
	var row asnRow
	err := q.QueryRow(ctx, `SELECT `+asnColumns+` FROM asns WHERE asn_number = $1`, string(number)).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrAsnNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asn %s: %w", number, err)
	}
	lines, err := loadAsnLines(ctx, q, []string{row.number})
	if err != nil {
		return nil, err
	}
	return row.toAsn(lines[row.number])
}

func loadAsnLines(ctx context.Context, q querier, numbers []string) (map[string][]asn.LineInput, error) {
	rows, err := q.Query(ctx, `SELECT asn_number, line_no, sku, expected_qty FROM asn_lines
		WHERE asn_number = ANY($1) ORDER BY asn_number, line_no`, numbers)
	if err != nil {
		return nil, fmt.Errorf("load asn lines: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]asn.LineInput, len(numbers))
	for rows.Next() {
		var number string
		var l asn.LineInput
		if err := rows.Scan(&number, &l.LineNo, &l.SKU, &l.ExpectedQty); err != nil {
			return nil, fmt.Errorf("scan asn line: %w", err)
		}
		out[number] = append(out[number], l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load asn lines: %w", err)
	}
	return out, nil
}

// Save implements ports.AsnRepository: loadedVersion 0 inserts (an existing
// number affects no row), anything else updates only the row whose stored
// version is still loadedVersion. No affected row means another writer won:
// repository.ErrConcurrentModification.
func (r *AsnRepo) Save(ctx context.Context, a *asn.Asn, loadedVersion int64) error {
	return inTx(ctx, r.pool, func(q querier) error {
		if loadedVersion == 0 {
			return insertAsn(ctx, q, a)
		}
		tag, err := q.Exec(ctx, `UPDATE asns SET state = $2, version = $3, updated_at = now()
			WHERE asn_number = $1 AND version = $4`, string(a.Number()), string(a.State()), a.Version(), loadedVersion)
		if err != nil {
			return fmt.Errorf("update asn %s: %w", a.Number(), err)
		}
		if tag.RowsAffected() != 1 {
			return repository.ErrConcurrentModification
		}
		return nil
	})
}

func insertAsn(ctx context.Context, q querier, a *asn.Asn) error {
	var arrival *time.Time
	if t := a.ExpectedArrival(); !t.IsZero() {
		arrival = &t
	}
	tag, err := q.Exec(ctx, `INSERT INTO asns (`+asnColumns+`) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (asn_number) DO NOTHING`,
		string(a.Number()), a.SupplierRef(), arrival, string(a.State()), a.Version())
	if err != nil {
		return fmt.Errorf("insert asn %s: %w", a.Number(), err)
	}
	if tag.RowsAffected() != 1 {
		return repository.ErrConcurrentModification
	}
	for _, l := range a.Lines() {
		if _, err := q.Exec(ctx, `INSERT INTO asn_lines (asn_number, line_no, sku, expected_qty) VALUES ($1, $2, $3, $4)`,
			string(a.Number()), l.LineNo(), string(l.SKU()), l.ExpectedQty()); err != nil {
			return fmt.Errorf("insert asn line %d of %s: %w", l.LineNo(), a.Number(), err)
		}
	}
	return nil
}

// List implements ports.AsnRepository: ascending number (byte order, the
// column is COLLATE "C"), strictly after the cursor.
func (r *AsnRepo) List(ctx context.Context, filter repository.AsnFilter, after asn.Number, limit int) ([]*asn.Asn, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+asnColumns+` FROM asns
		WHERE asn_number > $1 AND ($2 = '' OR state = $2)
		ORDER BY asn_number LIMIT $3`, string(after), string(filter.State), limit)
	if err != nil {
		return nil, fmt.Errorf("list asns: %w", err)
	}
	defer rows.Close()
	var page []asnRow
	numbers := make([]string, 0, limit)
	for rows.Next() {
		var row asnRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan asn: %w", err)
		}
		page = append(page, row)
		numbers = append(numbers, row.number)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list asns: %w", err)
	}
	rows.Close()
	lines, err := loadAsnLines(ctx, q, numbers)
	if err != nil {
		return nil, err
	}
	out := make([]*asn.Asn, 0, len(page))
	for _, row := range page {
		a, err := row.toAsn(lines[row.number])
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
