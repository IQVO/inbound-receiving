package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// PackagingView is the in-memory PackagingView (ports.PackagingDirectory and
// ports.PackagingStore), a test double with the same version rules as the
// Postgres adapter.
type PackagingView struct {
	mu     sync.Mutex
	levels map[gs1.GTIN]repository.PackLevel
}

// NewPackagingView constructs an empty PackagingView.
func NewPackagingView() *PackagingView {
	return &PackagingView{levels: make(map[gs1.GTIN]repository.PackLevel)}
}

// ByGTIN implements ports.PackagingDirectory.
func (v *PackagingView) ByGTIN(_ context.Context, gtin gs1.GTIN) (repository.PackLevel, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.levels[gtin]
	if !ok {
		return repository.PackLevel{}, repository.ErrPackLevelNotFound
	}
	return p, nil
}

// ActiveLevel implements ports.PackagingDirectory: the latest start wins.
func (v *PackagingView) ActiveLevel(_ context.Context, sku shared.SKU, level shared.PackLevel, at time.Time) (repository.PackLevel, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var found []repository.PackLevel
	for _, p := range v.levels {
		if p.SKU == sku && p.Level == level && p.ActiveAt(at) {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return repository.PackLevel{}, repository.ErrPackLevelNotFound
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].EffectiveFrom.Equal(found[j].EffectiveFrom) {
			return found[i].EffectiveFrom.After(found[j].EffectiveFrom)
		}
		return found[i].GTIN.String() < found[j].GTIN.String()
	})
	return found[0], nil
}

// ApplyDeclared implements ports.PackagingStore.
func (v *PackagingView) ApplyDeclared(_ context.Context, p repository.PackLevel) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.levels[p.GTIN]; ok && cur.Version >= p.Version {
		return false, nil
	}
	p.EffectiveTo = time.Time{}
	v.levels[p.GTIN] = p
	return true, nil
}

// ApplyRetired implements ports.PackagingStore.
func (v *PackagingView) ApplyRetired(_ context.Context, gtin gs1.GTIN, effectiveTo time.Time, version int64) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	cur, ok := v.levels[gtin]
	if !ok || cur.Version > version {
		return false, nil
	}
	cur.EffectiveTo, cur.Version = effectiveTo, version
	v.levels[gtin] = cur
	return true, nil
}

// Snapshot implements Snapshotter.
func (v *PackagingView) Snapshot() func() {
	v.mu.Lock()
	defer v.mu.Unlock()
	saved := make(map[gs1.GTIN]repository.PackLevel, len(v.levels))
	for k, p := range v.levels {
		saved[k] = p
	}
	return func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.levels = saved
	}
}
