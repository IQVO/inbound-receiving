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
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// PackagingView is the pgx-backed PackagingView: the pack_levels table fed by
// the packaging consumer and read by the receiving use cases (ports.
// PackagingDirectory and ports.PackagingStore).
type PackagingView struct {
	pool *pgxpool.Pool
}

// NewPackagingView constructs a PackagingView over pool.
func NewPackagingView(pool *pgxpool.Pool) *PackagingView { return &PackagingView{pool: pool} }

var (
	_ ports.PackagingDirectory = (*PackagingView)(nil)
	_ ports.PackagingStore     = (*PackagingView)(nil)
)

const packColumns = `gtin, sku, level, child_gtin, qty_of_child, units_per_pack, effective_from, effective_to, version`

func scanPackLevel(row pgx.Row) (repository.PackLevel, error) {
	var gtin, sku, level string
	var child *string
	var to *time.Time
	var p repository.PackLevel
	if err := row.Scan(&gtin, &sku, &level, &child, &p.QtyOfChild, &p.UnitsPerPack, &p.EffectiveFrom, &to, &p.Version); err != nil {
		return repository.PackLevel{}, err
	}
	var err error
	if p.GTIN, err = gs1.ParseGTIN(gtin); err != nil {
		return repository.PackLevel{}, fmt.Errorf("stored gtin %q: %w", gtin, err)
	}
	if child != nil {
		if p.ChildGTIN, err = gs1.ParseGTIN(*child); err != nil {
			return repository.PackLevel{}, fmt.Errorf("stored child gtin %q: %w", *child, err)
		}
	}
	p.SKU, p.Level = shared.SKU(sku), shared.PackLevel(level)
	p.EffectiveFrom = p.EffectiveFrom.UTC()
	if to != nil {
		p.EffectiveTo = to.UTC()
	}
	return p, nil
}

// ByGTIN implements ports.PackagingDirectory.
func (v *PackagingView) ByGTIN(ctx context.Context, gtin gs1.GTIN) (repository.PackLevel, error) {
	p, err := scanPackLevel(queryFor(ctx, v.pool).QueryRow(ctx, `SELECT `+packColumns+` FROM pack_levels WHERE gtin = $1`, gtin.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.PackLevel{}, repository.ErrPackLevelNotFound
	}
	if err != nil {
		return repository.PackLevel{}, fmt.Errorf("pack level of gtin %s: %w", gtin, err)
	}
	return p, nil
}

// ActiveLevel implements ports.PackagingDirectory: the level of the SKU in
// effect at the given time (the latest start wins).
func (v *PackagingView) ActiveLevel(ctx context.Context, sku shared.SKU, level shared.PackLevel, at time.Time) (repository.PackLevel, error) {
	p, err := scanPackLevel(queryFor(ctx, v.pool).QueryRow(ctx, `SELECT `+packColumns+` FROM pack_levels
		WHERE sku = $1 AND level = $2 AND effective_from <= $3 AND (effective_to IS NULL OR effective_to > $3)
		ORDER BY effective_from DESC, gtin LIMIT 1`, string(sku), string(level), at))
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.PackLevel{}, repository.ErrPackLevelNotFound
	}
	if err != nil {
		return repository.PackLevel{}, fmt.Errorf("active %s level of %s: %w", level, sku, err)
	}
	return p, nil
}

// ApplyDeclared implements ports.PackagingStore: an upsert keyed by GTIN that
// only a strictly newer product version may overwrite.
func (v *PackagingView) ApplyDeclared(ctx context.Context, p repository.PackLevel) (bool, error) {
	tag, err := queryFor(ctx, v.pool).Exec(ctx, `
		INSERT INTO pack_levels (`+packColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, $8)
		ON CONFLICT (gtin) DO UPDATE SET
		    sku = EXCLUDED.sku, level = EXCLUDED.level, child_gtin = EXCLUDED.child_gtin, qty_of_child = EXCLUDED.qty_of_child,
		    units_per_pack = EXCLUDED.units_per_pack, effective_from = EXCLUDED.effective_from, effective_to = NULL,
		    version = EXCLUDED.version, updated_at = now()
		WHERE pack_levels.version < EXCLUDED.version`,
		p.GTIN.String(), string(p.SKU), string(p.Level), optString(p.ChildGTIN.String()), p.QtyOfChild, p.UnitsPerPack, p.EffectiveFrom, p.Version)
	if err != nil {
		return false, fmt.Errorf("apply declared pack level %s: %w", p.GTIN, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ApplyRetired implements ports.PackagingStore.
func (v *PackagingView) ApplyRetired(ctx context.Context, gtin gs1.GTIN, effectiveTo time.Time, version int64) (bool, error) {
	tag, err := queryFor(ctx, v.pool).Exec(ctx, `UPDATE pack_levels SET effective_to = $2, version = $3, updated_at = now()
		WHERE gtin = $1 AND version <= $3`, gtin.String(), effectiveTo, version)
	if err != nil {
		return false, fmt.Errorf("apply retired pack level %s: %w", gtin, err)
	}
	return tag.RowsAffected() == 1, nil
}
