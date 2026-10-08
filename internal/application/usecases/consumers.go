package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// Consumer names namespace the claims in ports.ProcessedEvents.
const (
	ProductRegistryConsumer  = "product-registry"
	DockDoorRegistryConsumer = "dock-door-registry"
)

// Outcome reports what a consumer use case did.
type Outcome string

// The consumer outcomes.
const (
	// Applied: the local copy was changed (or confirmed) and the id claimed.
	Applied Outcome = "applied"
	// Duplicate: this CloudEvents id was already processed.
	Duplicate Outcome = "duplicate"
	// Ignored: a valid event that is not about an inbound dock door.
	Ignored Outcome = "ignored"
)

// Dock roles and flows of facility-layout's LocationSlotRegistered.
const (
	roleDock    = "Dock"
	flowInbound = "Inbound"
	flowBoth    = "Both"
)

// ApplyProductRegistered feeds the known_skus local copy from product-master's
// ProductRegistered (ADR 0003). The CloudEvents id claim and the upsert run
// in ONE unit of work.
type ApplyProductRegistered struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Skus            ports.SkuStore
}

// Handle runs the use case. A SKU this context would reject is wrapped in
// ErrInvalidEvent before anything is claimed; any other error is transient
// (the unit of work rolled back, including the claim).
func (uc *ApplyProductRegistered) Handle(ctx context.Context, eventID, rawSKU string) (Outcome, error) {
	sku, err := shared.NewSKU(rawSKU)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	outcome := Applied
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		claimed, err := uc.ProcessedEvents.Claim(ctx, ProductRegistryConsumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = Duplicate
			return nil
		}
		return uc.Skus.Upsert(ctx, string(sku))
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// LocationSlot is the part of a LocationSlotRegistered payload the dock-door
// copy needs. An absent role arrives as "".
type LocationSlot struct {
	LocationCode string
	Role         string
	DockFlow     string
}

// ApplyLocationSlotRegistered feeds the dock_doors local copy from
// facility-layout's LocationSlotRegistered (ADR 0003): only role Dock with a
// dockFlow of Inbound or Both is kept.
type ApplyLocationSlotRegistered struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Doors           ports.DockDoorStore
}

// Handle runs the use case.
func (uc *ApplyLocationSlotRegistered) Handle(ctx context.Context, eventID string, slot LocationSlot) (Outcome, error) {
	if slot.Role != roleDock || (slot.DockFlow != flowInbound && slot.DockFlow != flowBoth) {
		return Ignored, nil
	}
	code, err := appointment.NewDoorCode(slot.LocationCode)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	door := repository.DockDoor{Code: code, Flow: repository.DockFlow(slot.DockFlow)}
	outcome := Applied
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		claimed, err := uc.ProcessedEvents.Claim(ctx, DockDoorRegistryConsumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = Duplicate
			return nil
		}
		return uc.Doors.Upsert(ctx, door)
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// ApplyLocationSlotDecommissioned removes a door from the dock_doors local
// copy. A code that is not a known door is a no-op success.
type ApplyLocationSlotDecommissioned struct {
	UoW             ports.UnitOfWork
	ProcessedEvents ports.ProcessedEvents
	Doors           ports.DockDoorStore
}

// Handle runs the use case.
func (uc *ApplyLocationSlotDecommissioned) Handle(ctx context.Context, eventID, locationCode string) (Outcome, error) {
	code, err := appointment.NewDoorCode(locationCode)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	outcome := Applied
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		claimed, err := uc.ProcessedEvents.Claim(ctx, DockDoorRegistryConsumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = Duplicate
			return nil
		}
		return uc.Doors.Remove(ctx, code)
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}
