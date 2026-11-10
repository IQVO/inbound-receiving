package usecases_test

import (
	"context"
	"sort"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// fakeStow is the in-memory ports.StowStore of the use case tests: it keeps
// the earliest fact per key like the Postgres adapter and reads receipts from
// the fakeReceipts of the world. Each *Err field fails the one call.
type fakeStow struct {
	arrivals map[appointment.ID]time.Time
	stows    map[string]time.Time
	done     map[receipt.ID]time.Time
	receipts *fakeReceipts

	applyErr, lookupErr, evidenceErr, completeErr error
}

func newFakeStow(r *fakeReceipts) *fakeStow {
	return &fakeStow{
		arrivals: map[appointment.ID]time.Time{}, stows: map[string]time.Time{}, done: map[receipt.ID]time.Time{}, receipts: r,
	}
}

func (s *fakeStow) ApplyArrival(_ context.Context, id appointment.ID, _ string, at time.Time) (bool, error) {
	if s.applyErr != nil {
		return false, s.applyErr
	}
	if cur, ok := s.arrivals[id]; ok && !at.Before(cur) {
		return false, nil
	}
	s.arrivals[id] = at
	return true, nil
}

func (s *fakeStow) ApplyCartonStow(_ context.Context, lpn string, at time.Time) (bool, error) {
	if s.applyErr != nil {
		return false, s.applyErr
	}
	if cur, ok := s.stows[lpn]; ok && !at.Before(cur) {
		return false, nil
	}
	s.stows[lpn] = at
	return true, nil
}

func (s *fakeStow) sorted() []*receipt.Receipt {
	ids := make([]string, 0, len(s.receipts.items))
	for id := range s.receipts.items {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out := make([]*receipt.Receipt, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneReceipt(s.receipts.items[receipt.ID(id)]))
	}
	return out
}

func (s *fakeStow) ReceiptsOfAppointment(_ context.Context, id appointment.ID) ([]*receipt.Receipt, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	var out []*receipt.Receipt
	for _, r := range s.sorted() {
		if r.AppointmentID() == id {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStow) ReceiptsOfCarton(_ context.Context, lpn string) ([]*receipt.Receipt, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	var out []*receipt.Receipt
	for _, r := range s.sorted() {
		if _, ok := r.CartonByLPN(lpn); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStow) Evidence(_ context.Context, r *receipt.Receipt) (repository.StowEvidence, error) {
	if s.evidenceErr != nil {
		return repository.StowEvidence{}, s.evidenceErr
	}
	ev := repository.StowEvidence{ArrivedAt: s.arrivals[r.AppointmentID()], Stows: map[string]time.Time{}}
	for _, lpn := range r.GoodCartonLPNs() {
		if at, ok := s.stows[lpn]; ok {
			ev.Stows[lpn] = at
		}
	}
	return ev, nil
}

func (s *fakeStow) Complete(_ context.Context, id receipt.ID, at time.Time) (bool, error) {
	if s.completeErr != nil {
		return false, s.completeErr
	}
	if _, ok := s.done[id]; ok {
		return false, nil
	}
	s.done[id] = at
	return true, nil
}

// failingEncoder fails every receipt encoding.
type failingEncoder struct {
	fakeEncoder
	err error
}

func (e failingEncoder) EncodeReceipt(...receipt.Event) ([]outbox.Message, error) { return nil, e.err }
