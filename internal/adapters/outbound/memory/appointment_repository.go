package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// AppointmentRepo is an in-memory, mutex-guarded appointments local copy
// (ports.AppointmentRepository and ports.AppointmentStore) with the same
// version rules as the Postgres adapter. Unit-test double only: the service
// itself has no in-memory runtime (ADR 0011).
type AppointmentRepo struct {
	mu    sync.Mutex
	items map[appointment.ID]*appointment.DockAppointment
}

// NewAppointmentRepo constructs an empty AppointmentRepo.
func NewAppointmentRepo() *AppointmentRepo {
	return &AppointmentRepo{items: make(map[appointment.ID]*appointment.DockAppointment)}
}

// Get implements ports.AppointmentRepository.
func (r *AppointmentRepo) Get(_ context.Context, id appointment.ID) (*appointment.DockAppointment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.items[id]
	if !ok {
		return nil, repository.ErrAppointmentNotFound
	}
	return copyAppointment(d, d.State())
}

// ApplyBooked implements ports.AppointmentStore.
func (r *AppointmentRepo) ApplyBooked(_ context.Context, d *appointment.DockAppointment) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.items[d.ID()]
	if !exists {
		c, err := copyAppointment(d, d.State())
		if err != nil {
			return false, err
		}
		r.items[d.ID()] = c
		return true, nil
	}
	if cur.Version() >= d.Version() {
		return false, nil
	}
	// The stored state is kept (a Booked event never revives a Cancelled row).
	c, err := copyAppointment(d, cur.State())
	if err != nil {
		return false, err
	}
	r.items[d.ID()] = c
	return true, nil
}

// ApplyCancelled implements ports.AppointmentStore.
func (r *AppointmentRepo) ApplyCancelled(_ context.Context, id appointment.ID, version int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.items[id]
	if !exists || cur.Version() >= version {
		return false, nil
	}
	numbers := make([]string, 0, len(cur.AsnNumbers()))
	for _, n := range cur.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	c, err := appointment.Rehydrate(string(cur.ID()), string(cur.DoorCode()), cur.Carrier(), cur.Window().Start(), cur.Window().End(), numbers, appointment.Cancelled, version)
	if err != nil {
		return false, err
	}
	r.items[id] = c
	return true, nil
}

// ApplyRescheduled implements ports.AppointmentStore (ADR 0020).
func (r *AppointmentRepo) ApplyRescheduled(_ context.Context, id appointment.ID, door appointment.DoorCode, window appointment.Window, version int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.move(id, door, &window, version)
}

// ApplyDoorReassigned implements ports.AppointmentStore (ADR 0020).
func (r *AppointmentRepo) ApplyDoorReassigned(_ context.Context, id appointment.ID, door appointment.DoorCode, version int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.move(id, door, nil, version)
}

// move sets the door (and the window, when non-nil) of a stored row at a
// newer version, keeping its state, carrier and ASNs. The caller holds mu.
func (r *AppointmentRepo) move(id appointment.ID, door appointment.DoorCode, window *appointment.Window, version int64) (bool, error) {
	cur, exists := r.items[id]
	if !exists || cur.Version() >= version {
		return false, nil
	}
	w := cur.Window()
	if window != nil {
		w = *window
	}
	numbers := make([]string, 0, len(cur.AsnNumbers()))
	for _, n := range cur.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	c, err := appointment.Rehydrate(string(cur.ID()), string(door), cur.Carrier(), w.Start(), w.End(), numbers, cur.State(), version)
	if err != nil {
		return false, err
	}
	r.items[id] = c
	return true, nil
}

// List implements ports.AppointmentRepository: ascending (window start, id).
func (r *AppointmentRepo) List(_ context.Context, filter repository.AppointmentFilter, after repository.AppointmentCursor, limit int) ([]*appointment.DockAppointment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]*appointment.DockAppointment, 0, len(r.items))
	for _, d := range r.items {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return appointmentBefore(all[i], all[j]) })
	out := make([]*appointment.DockAppointment, 0, limit)
	for _, d := range all {
		if !pastCursor(d, after) || !matchesFilter(d, filter) {
			continue
		}
		c, err := copyAppointment(d, d.State())
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

func appointmentBefore(a, b *appointment.DockAppointment) bool {
	if !a.Window().Start().Equal(b.Window().Start()) {
		return a.Window().Start().Before(b.Window().Start())
	}
	return a.ID() < b.ID()
}

func pastCursor(d *appointment.DockAppointment, after repository.AppointmentCursor) bool {
	if after.WindowStart.IsZero() {
		return true
	}
	start := d.Window().Start()
	return start.After(after.WindowStart) || (start.Equal(after.WindowStart) && d.ID() > after.ID)
}

func matchesFilter(d *appointment.DockAppointment, f repository.AppointmentFilter) bool {
	if f.Door != "" && d.DoorCode() != f.Door {
		return false
	}
	if f.State != "" && d.State() != f.State {
		return false
	}
	if !f.From.IsZero() && !d.Window().End().After(f.From) {
		return false
	}
	return f.To.IsZero() || d.Window().Start().Before(f.To)
}

// Snapshot implements Snapshotter.
func (r *AppointmentRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := make(map[appointment.ID]*appointment.DockAppointment, len(r.items))
	for k, v := range r.items {
		saved[k] = v
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.items = saved
	}
}

func copyAppointment(d *appointment.DockAppointment, state appointment.State) (*appointment.DockAppointment, error) {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	return appointment.Rehydrate(string(d.ID()), string(d.DoorCode()), d.Carrier(), d.Window().Start(), d.Window().End(), numbers, state, d.Version())
}
