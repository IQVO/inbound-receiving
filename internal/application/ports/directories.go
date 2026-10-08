package ports

import (
	"context"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// SkuDirectory reads the known_skus local copy of product-master's
// registrations (ADR 0003).
type SkuDirectory interface {
	// Exists reports whether the SKU has been seen.
	Exists(ctx context.Context, sku string) (bool, error)
}

// SkuStore writes the known_skus local copy; only the product consumer uses it.
type SkuStore interface {
	// Upsert records the SKU; recording a known one is a no-op.
	Upsert(ctx context.Context, sku string) error
}

// DockDoorDirectory reads the dock_doors local copy of facility-layout's
// inbound dock doors (ADR 0003).
type DockDoorDirectory interface {
	// Exists reports whether the code is a known inbound dock door.
	Exists(ctx context.Context, code appointment.DoorCode) (bool, error)
	// List returns the known doors in ascending code order.
	List(ctx context.Context) ([]repository.DockDoor, error)
}

// DockDoorStore writes the dock_doors local copy; only the dock-door
// consumer uses it.
type DockDoorStore interface {
	// Upsert records the door, replacing its flow when it is known.
	Upsert(ctx context.Context, door repository.DockDoor) error
	// Remove deletes the door; removing an unknown code is a no-op.
	Remove(ctx context.Context, code appointment.DoorCode) error
}
