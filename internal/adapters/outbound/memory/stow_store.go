package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// StowStore is the in-memory ports.StowStore (tests and the BDD suite only,
// ADR 0011): the visit_arrivals and carton_stows copies and the completion
// marker, reading receipts from the ReceiptRepo it was built over. The
// UnitOfWork serialises writers, so the receipt lookups need no lock.
type StowStore struct {
	mu       sync.Mutex
	receipts *ReceiptRepo
	arrivals map[appointment.ID]repository.Arrival
	stows    map[string]time.Time
	done     map[receipt.ID]struct{}
}

// NewStowStore constructs an empty StowStore over the receipts it evaluates.
func NewStowStore(receipts *ReceiptRepo) *StowStore {
	return &StowStore{
		receipts: receipts, arrivals: map[appointment.ID]repository.Arrival{},
		stows: map[string]time.Time{}, done: map[receipt.ID]struct{}{},
	}
}

var _ ports.StowStore = (*StowStore)(nil)

// ApplyArrival implements ports.StowStore: the earliest arrival wins.
func (s *StowStore) ApplyArrival(_ context.Context, id appointment.ID, visitID string, arrivedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.arrivals[id]; ok && !arrivedAt.Before(cur.ArrivedAt) {
		return false, nil
	}
	s.arrivals[id] = repository.Arrival{VisitID: visitID, ArrivedAt: arrivedAt.UTC()}
	return true, nil
}

// Arrival implements ports.StowStore (ADR 0025).
func (s *StowStore) Arrival(_ context.Context, id appointment.ID) (repository.Arrival, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.arrivals[id]
	return a, ok, nil
}

// ApplyCartonStow implements ports.StowStore: the earliest stow wins.
func (s *StowStore) ApplyCartonStow(_ context.Context, lpn string, stowedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.stows[lpn]; ok && !stowedAt.Before(cur) {
		return false, nil
	}
	s.stows[lpn] = stowedAt.UTC()
	return true, nil
}

// ReceiptsOfAppointment implements ports.StowStore.
func (s *StowStore) ReceiptsOfAppointment(_ context.Context, id appointment.ID) ([]*receipt.Receipt, error) {
	return s.receipts.matching(func(x *receipt.Receipt) bool { return x.AppointmentID() == id })
}

// ReceiptsOfCarton implements ports.StowStore.
func (s *StowStore) ReceiptsOfCarton(_ context.Context, lpn string) ([]*receipt.Receipt, error) {
	return s.receipts.matching(func(x *receipt.Receipt) bool {
		_, ok := x.CartonByLPN(lpn)
		return ok
	})
}

// Evidence implements ports.StowStore.
func (s *StowStore) Evidence(_ context.Context, x *receipt.Receipt) (repository.StowEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := repository.StowEvidence{ArrivedAt: s.arrivals[x.AppointmentID()].ArrivedAt, Stows: map[string]time.Time{}}
	for _, lpn := range x.GoodCartonLPNs() {
		if at, ok := s.stows[lpn]; ok {
			ev.Stows[lpn] = at
		}
	}
	return ev, nil
}

// Complete implements ports.StowStore.
func (s *StowStore) Complete(_ context.Context, id receipt.ID, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.done[id]; ok {
		return false, nil
	}
	s.done[id] = struct{}{}
	return true, nil
}

// Snapshot implements Snapshotter.
func (s *StowStore) Snapshot() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	arrivals, stows, done := map[appointment.ID]repository.Arrival{}, map[string]time.Time{}, map[receipt.ID]struct{}{}
	for k, v := range s.arrivals {
		arrivals[k] = v
	}
	for k, v := range s.stows {
		stows[k] = v
	}
	for k, v := range s.done {
		done[k] = v
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.arrivals, s.stows, s.done = arrivals, stows, done
	}
}

// matching returns copies of the receipts the predicate keeps, in id order.
func (r *ReceiptRepo) matching(keep func(*receipt.Receipt) bool) ([]*receipt.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	var out []*receipt.Receipt
	for _, id := range ids {
		x := r.items[receipt.ID(id)]
		if !keep(x) {
			continue
		}
		c, err := copyReceipt(x)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
