package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// AdviceStore is the in-memory ports.AdviceStore (tests and the BDD suite only,
// ADR 0011): the issued and overdue markers of the receiving advice, reading
// receipts from the ReceiptRepo it was built over. The UnitOfWork serialises
// writers, so the candidate lookup needs no row lock.
type AdviceStore struct {
	mu       sync.Mutex
	receipts *ReceiptRepo
	issued   map[receipt.ID]bool // within_sla by receipt
	overdue  map[receipt.ID]time.Time
}

// NewAdviceStore constructs an empty AdviceStore over the receipts it scans.
func NewAdviceStore(receipts *ReceiptRepo) *AdviceStore {
	return &AdviceStore{receipts: receipts, issued: map[receipt.ID]bool{}, overdue: map[receipt.ID]time.Time{}}
}

var _ ports.AdviceStore = (*AdviceStore)(nil)

// MarkIssued implements ports.AdviceStore.
func (s *AdviceStore) MarkIssued(_ context.Context, id receipt.ID, _ time.Time, withinSLA bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.issued[id]; ok {
		return false, nil
	}
	s.issued[id] = withinSLA
	return true, nil
}

// MarkOverdue implements ports.AdviceStore.
func (s *AdviceStore) MarkOverdue(_ context.Context, id receipt.ID, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.overdue[id]; ok {
		return false, nil
	}
	s.overdue[id] = at.UTC()
	return true, nil
}

// OverdueCandidates implements ports.AdviceStore.
func (s *AdviceStore) OverdueCandidates(_ context.Context, cutoff time.Time, limit int) ([]*receipt.Receipt, error) {
	s.mu.Lock()
	marked := make(map[receipt.ID]bool, len(s.overdue))
	for id := range s.overdue {
		marked[id] = true
	}
	s.mu.Unlock()
	all, err := s.receipts.matching(func(x *receipt.Receipt) bool {
		return x.State() == receipt.StateOpen && x.AdviceClock().Before(cutoff) && !marked[x.ID()]
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID() < all[j].ID() })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Snapshot implements Snapshotter.
func (s *AdviceStore) Snapshot() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	issued, overdue := make(map[receipt.ID]bool, len(s.issued)), make(map[receipt.ID]time.Time, len(s.overdue))
	for k, v := range s.issued {
		issued[k] = v
	}
	for k, v := range s.overdue {
		overdue[k] = v
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.issued, s.overdue = issued, overdue
	}
}
