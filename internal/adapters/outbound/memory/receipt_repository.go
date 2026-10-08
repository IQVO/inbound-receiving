package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// ReceiptRepo is an in-memory, mutex-guarded ports.ReceiptRepository,
// including the "one open receipt per ASN" rule the Postgres adapter gets
// from a partial unique index.
type ReceiptRepo struct {
	mu    sync.Mutex
	items map[receipt.ID]*receipt.Receipt
}

// NewReceiptRepo constructs an empty ReceiptRepo.
func NewReceiptRepo() *ReceiptRepo {
	return &ReceiptRepo{items: make(map[receipt.ID]*receipt.Receipt)}
}

// Get implements ports.ReceiptRepository.
func (r *ReceiptRepo) Get(_ context.Context, id receipt.ID) (*receipt.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.items[id]
	if !ok {
		return nil, repository.ErrReceiptNotFound
	}
	return copyReceipt(x)
}

// Save implements ports.ReceiptRepository.
func (r *ReceiptRepo) Save(_ context.Context, x *receipt.Receipt, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.items[x.ID()]
	var stored int64
	if exists {
		stored = cur.Version()
	}
	if !versionGuardOK(loadedVersion, exists, stored) {
		return repository.ErrConcurrentModification
	}
	if loadedVersion == 0 && r.hasOpen(x.AsnNumber()) && x.State() == receipt.StateOpen {
		return repository.ErrReceiptAlreadyOpen
	}
	c, err := copyReceipt(x)
	if err != nil {
		return err
	}
	r.items[x.ID()] = c
	return nil
}

func (r *ReceiptRepo) hasOpen(number asn.Number) bool {
	for _, x := range r.items {
		if x.AsnNumber() == number && x.State() == receipt.StateOpen {
			return true
		}
	}
	return false
}

// List implements ports.ReceiptRepository: ascending id.
func (r *ReceiptRepo) List(_ context.Context, filter repository.ReceiptFilter, after receipt.ID, limit int) ([]*receipt.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out := make([]*receipt.Receipt, 0, limit)
	for _, id := range ids {
		x := r.items[receipt.ID(id)]
		if id <= string(after) || !matchesReceipt(x, filter) {
			continue
		}
		c, err := copyReceipt(x)
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

func matchesReceipt(x *receipt.Receipt, f repository.ReceiptFilter) bool {
	if f.AsnNumber != "" && x.AsnNumber() != f.AsnNumber {
		return false
	}
	return f.State == "" || x.State() == f.State
}

// OpenByAsn implements ports.ReceiptRepository.
func (r *ReceiptRepo) OpenByAsn(_ context.Context, number asn.Number) (*receipt.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.items {
		if x.AsnNumber() == number && x.State() == receipt.StateOpen {
			return copyReceipt(x)
		}
	}
	return nil, repository.ErrReceiptNotFound
}

// Snapshot implements Snapshotter.
func (r *ReceiptRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := make(map[receipt.ID]*receipt.Receipt, len(r.items))
	for k, v := range r.items {
		saved[k] = v
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.items = saved
	}
}

func copyReceipt(x *receipt.Receipt) (*receipt.Receipt, error) {
	lines := make([]receipt.LineState, 0, len(x.Lines()))
	for _, l := range x.Lines() {
		lines = append(lines, receipt.LineState{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
		})
	}
	return receipt.Rehydrate(receipt.Persisted{
		ID: string(x.ID()), AsnNumber: string(x.AsnNumber()), AppointmentID: string(x.AppointmentID()),
		DoorCode: string(x.DoorCode()), State: x.State(), Lines: lines,
		OpenedAt: x.OpenedAt(), ClosedAt: x.ClosedAt(), Version: x.Version(),
	})
}
