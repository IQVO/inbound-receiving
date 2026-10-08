package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// AppointmentRepo is an in-memory, mutex-guarded ports.AppointmentRepository.
// LockDoor is a no-op: the in-memory UnitOfWork already serialises every
// write, which gives the per-door atomicity the Postgres adapter gets from
// an advisory lock.
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
	return copyAppointment(d)
}

// Save implements ports.AppointmentRepository.
func (r *AppointmentRepo) Save(_ context.Context, d *appointment.DockAppointment, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.items[d.ID()]
	var stored int64
	if exists {
		stored = cur.Version()
	}
	if !versionGuardOK(loadedVersion, exists, stored) {
		return repository.ErrConcurrentModification
	}
	c, err := copyAppointment(d)
	if err != nil {
		return err
	}
	r.items[d.ID()] = c
	return nil
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
		c, err := copyAppointment(d)
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

// ActiveOnDoor implements ports.AppointmentRepository.
func (r *AppointmentRepo) ActiveOnDoor(_ context.Context, door appointment.DoorCode) ([]*appointment.DockAppointment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*appointment.DockAppointment
	for _, d := range r.items {
		if d.DoorCode() != door || !d.State().Active() {
			continue
		}
		c, err := copyAppointment(d)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// LockDoor implements ports.AppointmentRepository (see the type comment).
func (r *AppointmentRepo) LockDoor(context.Context, appointment.DoorCode) error { return nil }

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

func copyAppointment(d *appointment.DockAppointment) (*appointment.DockAppointment, error) {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	return appointment.Rehydrate(string(d.ID()), string(d.DoorCode()), d.Carrier(), d.Window().Start(), d.Window().End(), numbers, d.State(), d.Version())
}
