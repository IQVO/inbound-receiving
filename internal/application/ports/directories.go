package ports

import (
	"context"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
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

// PackagingDirectory reads the PackagingView, the local projection of
// product-master's pack levels (contracts R8): never a call to product-master.
type PackagingDirectory interface {
	// ByGTIN returns the level the GTIN identifies, active or retired, or
	// repository.ErrPackLevelNotFound.
	ByGTIN(ctx context.Context, gtin gs1.GTIN) (repository.PackLevel, error)
	// ActiveLevel returns the level of the SKU that is in effect at the given
	// time, or repository.ErrPackLevelNotFound.
	ActiveLevel(ctx context.Context, sku shared.SKU, level shared.PackLevel, at time.Time) (repository.PackLevel, error)
}

// PackagingStore writes the PackagingView; only the packaging consumer uses it.
type PackagingStore interface {
	// ApplyDeclared upserts the level keyed by its GTIN. An event whose
	// version is older than the stored one is ignored (it reports false).
	ApplyDeclared(ctx context.Context, level repository.PackLevel) (applied bool, err error)
	// ApplyRetired end-dates the level of the GTIN. It reports false when the
	// GTIN is unknown or the event is older than the stored version.
	ApplyRetired(ctx context.Context, gtin gs1.GTIN, effectiveTo time.Time, version int64) (applied bool, err error)
}
