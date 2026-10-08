package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// world is a complete in-memory set of fakes for every port plus the use
// cases wired over them.
type world struct {
	asns         *fakeAsns
	appointments *fakeAppointments
	receipts     *fakeReceipts
	skus         *fakeSkus
	doors        *fakeDoors
	processed    *fakeProcessed
	outbox       *fakeOutbox
	clock        *fakeClock
	ids          *fakeIDs
	uow          *fakeUoW
	writer       usecases.Writer
}

func newWorld() *world {
	w := &world{
		asns:      &fakeAsns{items: map[asn.Number]*asn.Asn{}},
		skus:      &fakeSkus{known: map[string]bool{}},
		doors:     &fakeDoors{doors: map[appointment.DoorCode]repository.DockDoor{}},
		processed: &fakeProcessed{seen: map[string]bool{}},
		outbox:    &fakeOutbox{},
		clock:     &fakeClock{now: time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)},
		ids:       &fakeIDs{},
		uow:       &fakeUoW{},
	}
	w.appointments = &fakeAppointments{items: map[appointment.ID]*appointment.DockAppointment{}}
	w.receipts = &fakeReceipts{items: map[receipt.ID]*receipt.Receipt{}}
	w.writer = usecases.Writer{
		Asns: w.asns, Appointments: w.appointments, Receipts: w.receipts,
		Outbox: w.outbox, Encoder: fakeEncoder{}, UoW: w.uow, Clock: w.clock, IDs: w.ids,
	}
	return w
}

func (w *world) registerAsn(number string, skus ...string) *asn.Asn {
	lines := make([]usecases.AsnLineInput, 0, len(skus))
	for i, s := range skus {
		lines = append(lines, usecases.AsnLineInput{LineNo: i + 1, SKU: s, ExpectedQty: 10})
	}
	uc := &usecases.RegisterAsn{Writer: w.writer}
	a, err := uc.Handle(context.Background(), usecases.RegisterAsnCommand{AsnNumber: number, SupplierRef: "ACME", Lines: lines})
	if err != nil {
		panic(err)
	}
	return a
}

func (w *world) book(door string, start time.Time, asns ...string) *appointment.DockAppointment {
	uc := &usecases.BookAppointment{Writer: w.writer}
	d, err := uc.Handle(context.Background(), usecases.BookAppointmentCommand{
		DoorCode: door, Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(2 * time.Hour), AsnNumbers: asns,
	})
	if err != nil {
		panic(err)
	}
	return d
}

func (w *world) eventTypes() []string {
	out := make([]string, 0, len(w.outbox.msgs))
	for _, m := range w.outbox.msgs {
		out = append(out, m.EventType)
	}
	return out
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func wantNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func windowAt(w *world, hours int) time.Time {
	return w.clock.now.Add(time.Duration(hours) * time.Hour)
}

// ---- fakeUoW ----------------------------------------------------------

type fakeUoW struct {
	calls int
	err   error
}

func (u *fakeUoW) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	u.calls++
	if u.err != nil {
		return u.err
	}
	return fn(ctx)
}

// ---- fakeClock / fakeIDs ------------------------------------------------

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct{ n int }

func (g *fakeIDs) uuid() string {
	g.n++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", g.n)
}
func (g *fakeIDs) NewAppointmentID() string { return "appt-" + g.uuid() }
func (g *fakeIDs) NewReceiptID() string     { return "rcpt-" + g.uuid() }

// ---- fakeEncoder / fakeOutbox ---------------------------------------------

type fakeEncoder struct{}

func (fakeEncoder) EncodeAsn(events ...asn.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Message{EventType: "asn." + e.EventName(), Subject: string(e.AsnNumber())})
	}
	return out, nil
}

func (fakeEncoder) EncodeAppointment(events ...appointment.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Message{EventType: "dockappointment." + e.EventName(), Subject: string(e.AppointmentID())})
	}
	return out, nil
}

func (fakeEncoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Message{EventType: "receipt." + e.EventName(), Subject: string(e.ReceiptID())})
	}
	return out, nil
}

type fakeOutbox struct {
	msgs []outbox.Message
	err  error
}

func (o *fakeOutbox) Insert(_ context.Context, msgs ...outbox.Message) error {
	if o.err != nil {
		return o.err
	}
	o.msgs = append(o.msgs, msgs...)
	return nil
}

// ---- fakeAsns ---------------------------------------------------------------

type fakeAsns struct {
	items   map[asn.Number]*asn.Asn
	getErr  error
	saveErr error
}

func (r *fakeAsns) Get(_ context.Context, n asn.Number) (*asn.Asn, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	a, ok := r.items[n]
	if !ok {
		return nil, repository.ErrAsnNotFound
	}
	return clone(a), nil
}

func (r *fakeAsns) Save(_ context.Context, a *asn.Asn, loaded int64) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	cur, ok := r.items[a.Number()]
	switch {
	case loaded == 0 && ok:
		return repository.ErrConcurrentModification
	case loaded != 0 && (!ok || cur.Version() != loaded):
		return repository.ErrConcurrentModification
	}
	r.items[a.Number()] = clone(a)
	return nil
}

func (r *fakeAsns) List(_ context.Context, f repository.AsnFilter, after asn.Number, limit int) ([]*asn.Asn, error) {
	keys := make([]string, 0, len(r.items))
	for k := range r.items {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	var out []*asn.Asn
	for _, k := range keys {
		a := r.items[asn.Number(k)]
		if k <= string(after) || (f.State != "" && a.State() != f.State) {
			continue
		}
		out = append(out, clone(a))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func clone(a *asn.Asn) *asn.Asn {
	lines := make([]asn.LineInput, 0, len(a.Lines()))
	for _, l := range a.Lines() {
		lines = append(lines, asn.LineInput{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	c, err := asn.Rehydrate(string(a.Number()), a.SupplierRef(), a.ExpectedArrival(), lines, a.State(), a.Version())
	if err != nil {
		panic(err)
	}
	return c
}

// ---- fakeAppointments -----------------------------------------------------------

type fakeAppointments struct {
	items map[appointment.ID]*appointment.DockAppointment
	locks []appointment.DoorCode
}

func cloneAppt(d *appointment.DockAppointment) *appointment.DockAppointment {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	c, err := appointment.Rehydrate(string(d.ID()), string(d.DoorCode()), d.Carrier(), d.Window().Start(), d.Window().End(), numbers, d.State(), d.Version())
	if err != nil {
		panic(err)
	}
	return c
}

func (r *fakeAppointments) Get(_ context.Context, id appointment.ID) (*appointment.DockAppointment, error) {
	d, ok := r.items[id]
	if !ok {
		return nil, repository.ErrAppointmentNotFound
	}
	return cloneAppt(d), nil
}

func (r *fakeAppointments) Save(_ context.Context, d *appointment.DockAppointment, loaded int64) error {
	cur, ok := r.items[d.ID()]
	if (loaded == 0 && ok) || (loaded != 0 && (!ok || cur.Version() != loaded)) {
		return repository.ErrConcurrentModification
	}
	r.items[d.ID()] = cloneAppt(d)
	return nil
}

func (r *fakeAppointments) List(_ context.Context, f repository.AppointmentFilter, after repository.AppointmentCursor, limit int) ([]*appointment.DockAppointment, error) {
	all := make([]*appointment.DockAppointment, 0, len(r.items))
	for _, d := range r.items {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if !a.Window().Start().Equal(b.Window().Start()) {
			return a.Window().Start().Before(b.Window().Start())
		}
		return a.ID() < b.ID()
	})
	var out []*appointment.DockAppointment
	for _, d := range all {
		if !afterCursor(d, after) || !matchesAppointment(d, f) {
			continue
		}
		out = append(out, cloneAppt(d))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func afterCursor(d *appointment.DockAppointment, after repository.AppointmentCursor) bool {
	if after.WindowStart.IsZero() {
		return true
	}
	s := d.Window().Start()
	return s.After(after.WindowStart) || (s.Equal(after.WindowStart) && d.ID() > after.ID)
}

func matchesAppointment(d *appointment.DockAppointment, f repository.AppointmentFilter) bool {
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

func (r *fakeAppointments) ActiveOnDoor(_ context.Context, door appointment.DoorCode) ([]*appointment.DockAppointment, error) {
	var out []*appointment.DockAppointment
	for _, d := range r.items {
		if d.DoorCode() == door && d.State().Active() {
			out = append(out, cloneAppt(d))
		}
	}
	return out, nil
}

func (r *fakeAppointments) LockDoor(_ context.Context, door appointment.DoorCode) error {
	r.locks = append(r.locks, door)
	return nil
}

// ---- fakeReceipts ---------------------------------------------------------------

type fakeReceipts struct {
	items map[receipt.ID]*receipt.Receipt
}

func cloneReceipt(r *receipt.Receipt) *receipt.Receipt {
	lines := make([]receipt.LineState, 0, len(r.Lines()))
	for _, l := range r.Lines() {
		lines = append(lines, receipt.LineState{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
		})
	}
	c, err := receipt.Rehydrate(receipt.Persisted{
		ID: string(r.ID()), AsnNumber: string(r.AsnNumber()), AppointmentID: string(r.AppointmentID()),
		DoorCode: string(r.DoorCode()), State: r.State(), Lines: lines, OpenedAt: r.OpenedAt(),
		ClosedAt: r.ClosedAt(), Version: r.Version(),
	})
	if err != nil {
		panic(err)
	}
	return c
}

func (r *fakeReceipts) Get(_ context.Context, id receipt.ID) (*receipt.Receipt, error) {
	x, ok := r.items[id]
	if !ok {
		return nil, repository.ErrReceiptNotFound
	}
	return cloneReceipt(x), nil
}

func (r *fakeReceipts) Save(_ context.Context, x *receipt.Receipt, loaded int64) error {
	cur, ok := r.items[x.ID()]
	if (loaded == 0 && ok) || (loaded != 0 && (!ok || cur.Version() != loaded)) {
		return repository.ErrConcurrentModification
	}
	if loaded == 0 {
		for _, o := range r.items {
			if o.AsnNumber() == x.AsnNumber() && o.State() == receipt.StateOpen && x.State() == receipt.StateOpen {
				return repository.ErrReceiptAlreadyOpen
			}
		}
	}
	r.items[x.ID()] = cloneReceipt(x)
	return nil
}

func (r *fakeReceipts) List(_ context.Context, f repository.ReceiptFilter, after receipt.ID, limit int) ([]*receipt.Receipt, error) {
	ids := make([]string, 0, len(r.items))
	for k := range r.items {
		ids = append(ids, string(k))
	}
	sort.Strings(ids)
	var out []*receipt.Receipt
	for _, k := range ids {
		x := r.items[receipt.ID(k)]
		if k <= string(after) || (f.AsnNumber != "" && x.AsnNumber() != f.AsnNumber) || (f.State != "" && x.State() != f.State) {
			continue
		}
		out = append(out, cloneReceipt(x))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *fakeReceipts) OpenByAsn(_ context.Context, n asn.Number) (*receipt.Receipt, error) {
	for _, x := range r.items {
		if x.AsnNumber() == n && x.State() == receipt.StateOpen {
			return cloneReceipt(x), nil
		}
	}
	return nil, repository.ErrReceiptNotFound
}

// ---- directories / processed events ---------------------------------------------

type fakeSkus struct {
	known map[string]bool
	err   error
}

func (s *fakeSkus) Exists(_ context.Context, sku string) (bool, error) { return s.known[sku], s.err }
func (s *fakeSkus) Upsert(_ context.Context, sku string) error {
	if s.err != nil {
		return s.err
	}
	s.known[sku] = true
	return nil
}

type fakeDoors struct {
	doors map[appointment.DoorCode]repository.DockDoor
	err   error
}

func (d *fakeDoors) Exists(_ context.Context, code appointment.DoorCode) (bool, error) {
	_, ok := d.doors[code]
	return ok, d.err
}

func (d *fakeDoors) List(_ context.Context) ([]repository.DockDoor, error) {
	if d.err != nil {
		return nil, d.err
	}
	var out []repository.DockDoor
	for _, v := range d.doors {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(string(out[i].Code), string(out[j].Code)) < 0 })
	return out, nil
}

func (d *fakeDoors) Upsert(_ context.Context, door repository.DockDoor) error {
	if d.err != nil {
		return d.err
	}
	d.doors[door.Code] = door
	return nil
}

func (d *fakeDoors) Remove(_ context.Context, code appointment.DoorCode) error {
	if d.err != nil {
		return d.err
	}
	delete(d.doors, code)
	return nil
}

type fakeProcessed struct {
	seen map[string]bool
	err  error
}

func (p *fakeProcessed) Claim(_ context.Context, consumer, eventID string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	key := consumer + "/" + eventID
	if p.seen[key] {
		return false, nil
	}
	p.seen[key] = true
	return true, nil
}
