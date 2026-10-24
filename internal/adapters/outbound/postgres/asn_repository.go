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
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// AsnRepo is the pgx-backed ports.AsnRepository over asns, asn_lines,
// asn_cartons and asn_carton_contents. Save is version-guarded: an insert
// never overwrites an existing number and an update only applies when the
// stored version is the loaded one. Lines and cartons never change after
// registration, so only the header row is updated (and the cartons are
// deactivated when the ASN is cancelled).
type AsnRepo struct {
	pool *pgxpool.Pool
}

// NewAsnRepo constructs an AsnRepo over pool.
func NewAsnRepo(pool *pgxpool.Pool) *AsnRepo { return &AsnRepo{pool: pool} }

var _ ports.AsnRepository = (*AsnRepo)(nil)

const asnColumns = `asn_number, supplier_ref, owner_ref, expected_arrival, state, version`

type asnRow struct {
	number          string
	supplierRef     string
	ownerRef        *string
	expectedArrival *time.Time
	state           string
	version         int64
}

func (r *asnRow) targets() []any {
	return []any{&r.number, &r.supplierRef, &r.ownerRef, &r.expectedArrival, &r.state, &r.version}
}

func (r asnRow) toAsn(lines []asn.LineInput, cartons []asn.CartonInput) (*asn.Asn, error) {
	var arrival time.Time
	if r.expectedArrival != nil {
		arrival = r.expectedArrival.UTC()
	}
	a, err := asn.RehydrateDraft(asn.Persisted{
		Draft: asn.Draft{
			Number: r.number, SupplierRef: r.supplierRef, OwnerRef: deref(r.ownerRef),
			ExpectedArrival: arrival, Lines: lines, Cartons: cartons,
		},
		State: asn.State(r.state), Version: r.version,
	})
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
	out, err := rehydrateAsns(ctx, q, []asnRow{row})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// rehydrateAsns loads the lines and cartons of the given rows and rebuilds them.
func rehydrateAsns(ctx context.Context, q querier, rows []asnRow) ([]*asn.Asn, error) {
	numbers := make([]string, 0, len(rows))
	for _, row := range rows {
		numbers = append(numbers, row.number)
	}
	lines, err := loadAsnLines(ctx, q, numbers)
	if err != nil {
		return nil, err
	}
	cartons, err := loadAsnCartons(ctx, q, numbers)
	if err != nil {
		return nil, err
	}
	out := make([]*asn.Asn, 0, len(rows))
	for _, row := range rows {
		a, err := row.toAsn(lines[row.number], cartons[row.number])
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
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

type cartonKey struct{ asn, sscc string }

// loadAsnCartons reads the cartons and their contents of the given ASNs, in
// announced order.
func loadAsnCartons(ctx context.Context, q querier, numbers []string) (map[string][]asn.CartonInput, error) {
	rows, err := q.Query(ctx, `SELECT asn_number, sscc, parent_sscc, kind, gross_weight_g, length_mm, width_mm, height_mm
		FROM asn_cartons WHERE asn_number = ANY($1) ORDER BY asn_number, position`, numbers)
	if err != nil {
		return nil, fmt.Errorf("load asn cartons: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]asn.CartonInput, len(numbers))
	index := map[cartonKey]int{}
	for rows.Next() {
		var number string
		var parent *string
		var weight, length, width, height *int64
		var c asn.CartonInput
		if err := rows.Scan(&number, &c.SSCC, &parent, &c.Kind, &weight, &length, &width, &height); err != nil {
			return nil, fmt.Errorf("scan asn carton: %w", err)
		}
		c.ParentSSCC = deref(parent)
		if weight != nil {
			c.GrossWeightG = *weight
		}
		if length != nil && width != nil && height != nil {
			c.Dims = &asn.DimsInput{LengthMm: *length, WidthMm: *width, HeightMm: *height}
		}
		index[cartonKey{number, c.SSCC}] = len(out[number])
		out[number] = append(out[number], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load asn cartons: %w", err)
	}
	rows.Close()
	if len(index) == 0 {
		return out, nil
	}
	return out, loadAsnCartonContents(ctx, q, numbers, out, index)
}

func loadAsnCartonContents(ctx context.Context, q querier, numbers []string, out map[string][]asn.CartonInput, index map[cartonKey]int) error {
	rows, err := q.Query(ctx, `SELECT asn_number, sscc, line_no, sku, gtin, qty, lot, expiry
		FROM asn_carton_contents WHERE asn_number = ANY($1) ORDER BY asn_number, sscc, position`, numbers)
	if err != nil {
		return fmt.Errorf("load asn carton contents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var number, sscc string
		var lot, expiry *string
		var c asn.ContentInput
		if err := rows.Scan(&number, &sscc, &c.LineNo, &c.SKU, &c.GTIN, &c.Qty, &lot, &expiry); err != nil {
			return fmt.Errorf("scan asn carton content: %w", err)
		}
		c.Lot, c.Expiry = deref(lot), deref(expiry)
		i := index[cartonKey{number, sscc}]
		out[number][i].Contents = append(out[number][i].Contents, c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load asn carton contents: %w", err)
	}
	return nil
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
		if a.State() != asn.Cancelled {
			return nil
		}
		// A cancelled ASN releases its SSCCs: they may be announced again.
		if _, err := q.Exec(ctx, `UPDATE asn_cartons SET active = FALSE WHERE asn_number = $1`, string(a.Number())); err != nil {
			return fmt.Errorf("release cartons of asn %s: %w", a.Number(), err)
		}
		return nil
	})
}

func insertAsn(ctx context.Context, q querier, a *asn.Asn) error {
	var arrival *time.Time
	if t := a.ExpectedArrival(); !t.IsZero() {
		arrival = &t
	}
	tag, err := q.Exec(ctx, `INSERT INTO asns (`+asnColumns+`) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (asn_number) DO NOTHING`,
		string(a.Number()), a.SupplierRef(), optString(string(a.OwnerRef())), arrival, string(a.State()), a.Version())
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
	return insertAsnCartons(ctx, q, a)
}

func insertAsnCartons(ctx context.Context, q querier, a *asn.Asn) error {
	for i, c := range a.Cartons() {
		var weight *int64
		if g := c.GrossWeightG(); g > 0 {
			weight = &g
		}
		var length, width, height *int64
		if d, ok := c.Dims(); ok {
			l, w, h := d.LengthMm(), d.WidthMm(), d.HeightMm()
			length, width, height = &l, &w, &h
		}
		if _, err := q.Exec(ctx, `INSERT INTO asn_cartons (asn_number, position, sscc, parent_sscc, kind, gross_weight_g, length_mm, width_mm, height_mm)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			string(a.Number()), i+1, c.SSCC().String(), optString(c.ParentSSCC().String()), string(c.Kind()), weight, length, width, height); err != nil {
			return fmt.Errorf("insert carton %s of %s: %w", c.SSCC(), a.Number(), err)
		}
		for j, x := range c.Contents() {
			if _, err := q.Exec(ctx, `INSERT INTO asn_carton_contents (asn_number, sscc, position, line_no, sku, gtin, qty, lot, expiry)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				string(a.Number()), c.SSCC().String(), j+1, x.LineNo(), string(x.SKU()), x.GTIN().String(), x.Qty(), optString(x.Lot()), optString(x.Expiry())); err != nil {
				return fmt.Errorf("insert content of carton %s of %s: %w", c.SSCC(), a.Number(), err)
			}
		}
	}
	return nil
}

// ExistingSsccs implements ports.AsnRepository: the given SSCCs found as a
// carton or a pallet SSCC on an ASN that is not cancelled.
func (r *AsnRepo) ExistingSsccs(ctx context.Context, ssccs []gs1.SSCC) ([]gs1.SSCC, error) {
	raw := make([]string, 0, len(ssccs))
	for _, s := range ssccs {
		raw = append(raw, s.String())
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT sscc FROM asn_cartons WHERE active AND sscc = ANY($1)
		UNION
		SELECT parent_sscc FROM asn_cartons WHERE active AND parent_sscc = ANY($1)
		ORDER BY 1`, raw)
	if err != nil {
		return nil, fmt.Errorf("existing ssccs: %w", err)
	}
	defer rows.Close()
	return scanSsccs(rows)
}

func scanSsccs(rows pgx.Rows) ([]gs1.SSCC, error) {
	var out []gs1.SSCC
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan sscc: %w", err)
		}
		s, err := gs1.ParseSSCC(raw)
		if err != nil {
			return nil, fmt.Errorf("stored sscc %q: %w", raw, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan ssccs: %w", err)
	}
	return out, nil
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
	for rows.Next() {
		var row asnRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan asn: %w", err)
		}
		page = append(page, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list asns: %w", err)
	}
	rows.Close()
	return rehydrateAsns(ctx, q, page)
}
