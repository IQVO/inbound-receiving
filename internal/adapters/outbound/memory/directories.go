package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// KnownSkus is the in-memory known_skus local copy (ports.SkuDirectory and
// ports.SkuStore).
type KnownSkus struct {
	mu   sync.Mutex
	skus map[string]struct{}
}

// NewKnownSkus constructs an empty KnownSkus.
func NewKnownSkus() *KnownSkus { return &KnownSkus{skus: make(map[string]struct{})} }

// Exists implements ports.SkuDirectory.
func (k *KnownSkus) Exists(_ context.Context, sku string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.skus[sku]
	return ok, nil
}

// Upsert implements ports.SkuStore.
func (k *KnownSkus) Upsert(_ context.Context, sku string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.skus[sku] = struct{}{}
	return nil
}

// Snapshot implements Snapshotter.
func (k *KnownSkus) Snapshot() func() {
	k.mu.Lock()
	defer k.mu.Unlock()
	saved := make(map[string]struct{}, len(k.skus))
	for s := range k.skus {
		saved[s] = struct{}{}
	}
	return func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.skus = saved
	}
}

// DockDoors is the in-memory dock_doors local copy (ports.DockDoorDirectory
// and ports.DockDoorStore).
type DockDoors struct {
	mu    sync.Mutex
	doors map[appointment.DoorCode]repository.DockDoor
}

// NewDockDoors constructs an empty DockDoors.
func NewDockDoors() *DockDoors {
	return &DockDoors{doors: make(map[appointment.DoorCode]repository.DockDoor)}
}

// Exists implements ports.DockDoorDirectory.
func (d *DockDoors) Exists(_ context.Context, code appointment.DoorCode) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.doors[code]
	return ok, nil
}

// List implements ports.DockDoorDirectory: ascending door code.
func (d *DockDoors) List(_ context.Context) ([]repository.DockDoor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]repository.DockDoor, 0, len(d.doors))
	for _, door := range d.doors {
		out = append(out, door)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// Upsert implements ports.DockDoorStore.
func (d *DockDoors) Upsert(_ context.Context, door repository.DockDoor) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.doors[door.Code] = door
	return nil
}

// Remove implements ports.DockDoorStore.
func (d *DockDoors) Remove(_ context.Context, code appointment.DoorCode) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.doors, code)
	return nil
}

// Snapshot implements Snapshotter.
func (d *DockDoors) Snapshot() func() {
	d.mu.Lock()
	defer d.mu.Unlock()
	saved := make(map[appointment.DoorCode]repository.DockDoor, len(d.doors))
	for k, v := range d.doors {
		saved[k] = v
	}
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.doors = saved
	}
}
