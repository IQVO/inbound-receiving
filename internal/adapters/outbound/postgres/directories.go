package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// KnownSkus is the pgx-backed known_skus local copy (ports.SkuDirectory and
// ports.SkuStore).
type KnownSkus struct {
	pool *pgxpool.Pool
}

// NewKnownSkus constructs a KnownSkus over pool.
func NewKnownSkus(pool *pgxpool.Pool) *KnownSkus { return &KnownSkus{pool: pool} }

var (
	_ ports.SkuDirectory = (*KnownSkus)(nil)
	_ ports.SkuStore     = (*KnownSkus)(nil)
)

// Exists implements ports.SkuDirectory.
func (k *KnownSkus) Exists(ctx context.Context, sku string) (bool, error) {
	var ok bool
	if err := queryFor(ctx, k.pool).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM known_skus WHERE sku = $1)`, sku).Scan(&ok); err != nil {
		return false, fmt.Errorf("known sku %s: %w", sku, err)
	}
	return ok, nil
}

// Upsert implements ports.SkuStore.
func (k *KnownSkus) Upsert(ctx context.Context, sku string) error {
	if _, err := queryFor(ctx, k.pool).Exec(ctx, `INSERT INTO known_skus (sku) VALUES ($1) ON CONFLICT (sku) DO NOTHING`, sku); err != nil {
		return fmt.Errorf("upsert known sku %s: %w", sku, err)
	}
	return nil
}

// DockDoors is the pgx-backed dock_doors local copy (ports.DockDoorDirectory
// and ports.DockDoorStore).
type DockDoors struct {
	pool *pgxpool.Pool
}

// NewDockDoors constructs a DockDoors over pool.
func NewDockDoors(pool *pgxpool.Pool) *DockDoors { return &DockDoors{pool: pool} }

var (
	_ ports.DockDoorDirectory = (*DockDoors)(nil)
	_ ports.DockDoorStore     = (*DockDoors)(nil)
)

// Exists implements ports.DockDoorDirectory.
func (d *DockDoors) Exists(ctx context.Context, code appointment.DoorCode) (bool, error) {
	var ok bool
	if err := queryFor(ctx, d.pool).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dock_doors WHERE door_code = $1)`, string(code)).Scan(&ok); err != nil {
		return false, fmt.Errorf("dock door %s: %w", code, err)
	}
	return ok, nil
}

// List implements ports.DockDoorDirectory: ascending door code.
func (d *DockDoors) List(ctx context.Context) ([]repository.DockDoor, error) {
	rows, err := queryFor(ctx, d.pool).Query(ctx, `SELECT door_code, dock_flow FROM dock_doors ORDER BY door_code COLLATE "C"`)
	if err != nil {
		return nil, fmt.Errorf("list dock doors: %w", err)
	}
	defer rows.Close()
	out := make([]repository.DockDoor, 0)
	for rows.Next() {
		var code, flow string
		if err := rows.Scan(&code, &flow); err != nil {
			return nil, fmt.Errorf("scan dock door: %w", err)
		}
		out = append(out, repository.DockDoor{Code: appointment.DoorCode(code), Flow: repository.DockFlow(flow)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dock doors: %w", err)
	}
	return out, nil
}

// Upsert implements ports.DockDoorStore.
func (d *DockDoors) Upsert(ctx context.Context, door repository.DockDoor) error {
	if _, err := queryFor(ctx, d.pool).Exec(ctx, `INSERT INTO dock_doors (door_code, dock_flow) VALUES ($1, $2)
		ON CONFLICT (door_code) DO UPDATE SET dock_flow = EXCLUDED.dock_flow, updated_at = now()`, string(door.Code), string(door.Flow)); err != nil {
		return fmt.Errorf("upsert dock door %s: %w", door.Code, err)
	}
	return nil
}

// Remove implements ports.DockDoorStore.
func (d *DockDoors) Remove(ctx context.Context, code appointment.DoorCode) error {
	if _, err := queryFor(ctx, d.pool).Exec(ctx, `DELETE FROM dock_doors WHERE door_code = $1`, string(code)); err != nil {
		return fmt.Errorf("remove dock door %s: %w", code, err)
	}
	return nil
}
