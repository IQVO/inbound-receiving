package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// AsnRepo is an in-memory, mutex-guarded ports.AsnRepository. It stores
// rehydrated copies so callers cannot mutate stored state, and enforces the
// same version guard as the Postgres adapter.
type AsnRepo struct {
	mu    sync.Mutex
	items map[asn.Number]*asn.Asn
}

// NewAsnRepo constructs an empty AsnRepo.
func NewAsnRepo() *AsnRepo { return &AsnRepo{items: make(map[asn.Number]*asn.Asn)} }

// Get implements ports.AsnRepository.
func (r *AsnRepo) Get(_ context.Context, number asn.Number) (*asn.Asn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.items[number]
	if !ok {
		return nil, repository.ErrAsnNotFound
	}
	return copyAsn(a)
}

// Save implements ports.AsnRepository.
func (r *AsnRepo) Save(_ context.Context, a *asn.Asn, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.items[a.Number()]
	if !versionGuardOK(loadedVersion, exists, versionOf(exists, cur)) {
		return repository.ErrConcurrentModification
	}
	stored, err := copyAsn(a)
	if err != nil {
		return err
	}
	r.items[a.Number()] = stored
	return nil
}

func versionOf(exists bool, a *asn.Asn) int64 {
	if !exists {
		return 0
	}
	return a.Version()
}

// versionGuardOK reports whether a save guarded by loaded may proceed given
// whether the row exists and the stored version: an insert (loaded 0) needs
// no row; an update needs a row at exactly the loaded version.
func versionGuardOK(loaded int64, exists bool, stored int64) bool {
	if loaded == 0 {
		return !exists
	}
	return exists && stored == loaded
}

// List implements ports.AsnRepository.
func (r *AsnRepo) List(_ context.Context, filter repository.AsnFilter, after asn.Number, limit int) ([]*asn.Asn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.items))
	for n := range r.items {
		keys = append(keys, string(n))
	}
	sort.Strings(keys)
	out := make([]*asn.Asn, 0, limit)
	for _, k := range keys {
		a := r.items[asn.Number(k)]
		if k <= string(after) || (filter.State != "" && a.State() != filter.State) {
			continue
		}
		c, err := copyAsn(a)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Snapshot implements Snapshotter.
func (r *AsnRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := make(map[asn.Number]*asn.Asn, len(r.items))
	for k, v := range r.items {
		saved[k] = v
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.items = saved
	}
}

func copyAsn(a *asn.Asn) (*asn.Asn, error) {
	lines := make([]asn.LineInput, 0, len(a.Lines()))
	for _, l := range a.Lines() {
		lines = append(lines, asn.LineInput{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	return asn.Rehydrate(string(a.Number()), a.SupplierRef(), a.ExpectedArrival(), lines, a.State(), a.Version())
}
