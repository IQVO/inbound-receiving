package analyticsstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// Memory is the in-memory twin of Projection and Reader, for unit tests and
// the HTTP handler tests. It implements the same semantics as the SQL (the
// shared contract test runs against both): idempotent on the event id,
// earliest-candidate milestones, day windows from report.DayWindows, and a
// rejection of what the database's CHECK constraints would reject.
type Memory struct {
	mu           sync.Mutex
	processed    map[string]struct{}
	asns         map[string]*memAsn
	appointments map[string]*report.AppointmentTimes
	receipts     map[string]*memReceipt
	lines        []memLine
	discrepancy  map[discrepancyKey]time.Time
	lastAt       *time.Time
}

type memAsn struct {
	expectedArrival *time.Time
	cancelled       *time.Time
	receiptOpened   *time.Time
}

type memReceipt struct {
	opened, closed *time.Time
}

type memLine struct {
	at        time.Time
	quantity  int64
	condition report.Condition
}

type discrepancyKey struct {
	receiptID string
	lineNo    int
	kind      report.DiscrepancyKind
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{
		processed:    map[string]struct{}{},
		asns:         map[string]*memAsn{},
		appointments: map[string]*report.AppointmentTimes{},
		receipts:     map[string]*memReceipt{},
		discrepancy:  map[discrepancyKey]time.Time{},
	}
}

var (
	_ report.Projection = (*Memory)(nil)
	_ report.Reader     = (*Memory)(nil)
)

// Apply implements report.Projection.
func (m *Memory) Apply(_ context.Context, e report.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.processed[e.EventID]; dup {
		return false, nil
	}
	if err := checkLikeTheDatabase(e); err != nil {
		return false, err
	}
	m.processed[e.EventID] = struct{}{}
	at := e.At.UTC()
	if m.lastAt == nil || at.After(*m.lastAt) {
		m.lastAt = &at
	}
	switch {
	case e.Kind.IsAsn():
		m.foldAsn(e, at)
	case e.Kind.IsAppointment():
		m.foldAppointment(e)
	default:
		m.foldReceipt(e, at)
	}
	return true, nil
}

func (m *Memory) asn(number string) *memAsn {
	a, ok := m.asns[number]
	if !ok {
		a = &memAsn{}
		m.asns[number] = a
	}
	return a
}

func (m *Memory) foldAsn(e report.Event, at time.Time) {
	a := m.asn(e.AsnNumber)
	if e.Kind == report.KindAsnRegistered {
		if a.expectedArrival == nil && e.ExpectedArrival != nil {
			u := e.ExpectedArrival.UTC()
			a.expectedArrival = &u
		}
		return
	}
	a.cancelled = earliest(a.cancelled, &at)
}

func (m *Memory) foldAppointment(e report.Event) {
	t, ok := m.appointments[e.AppointmentID]
	if !ok {
		t = &report.AppointmentTimes{}
		m.appointments[e.AppointmentID] = t
	}
	c := report.AppointmentTimesOf(e)
	t.Booked = earliest(t.Booked, c.Booked)
	t.CheckedIn = earliest(t.CheckedIn, c.CheckedIn)
	t.Cancelled = earliest(t.Cancelled, c.Cancelled)
	t.Completed = earliest(t.Completed, c.Completed)
}

func (m *Memory) foldReceipt(e report.Event, at time.Time) {
	r, ok := m.receipts[e.ReceiptID]
	if !ok {
		r = &memReceipt{}
		m.receipts[e.ReceiptID] = r
	}
	c := report.ReceiptTimesOf(e)
	r.opened = earliest(r.opened, c.Opened)
	r.closed = earliest(r.closed, c.Closed)
	switch e.Kind {
	case report.KindReceiptOpened:
		a := m.asn(e.AsnNumber)
		a.receiptOpened = earliest(a.receiptOpened, c.Opened)
	case report.KindReceiptLineReceived:
		m.lines = append(m.lines, memLine{at: at, quantity: e.Line.Quantity, condition: e.Line.Condition})
	case report.KindReceiptClosed:
		for _, d := range e.Discrepancies {
			k := discrepancyKey{receiptID: e.ReceiptID, lineNo: d.LineNo, kind: d.Kind}
			if _, exists := m.discrepancy[k]; !exists {
				m.discrepancy[k] = at
			}
		}
	}
}

// checkLikeTheDatabase mirrors the analytical schema's CHECK constraints (a
// line number and quantity >= 1, a known condition and discrepancy kind):
// what Postgres rejects with an integrity violation, Memory rejects too.
func checkLikeTheDatabase(e report.Event) error {
	if l := e.Line; l != nil && (l.LineNo < 1 || l.Quantity < 1 || !l.Condition.Valid()) {
		return fmt.Errorf("%w: received line %d of %s violates a check constraint", report.ErrRejected, l.LineNo, e.ReceiptID)
	}
	for _, d := range e.Discrepancies {
		if d.LineNo < 1 || !d.Kind.Valid() {
			return fmt.Errorf("%w: discrepancy %d/%s of %s violates a check constraint", report.ErrRejected, d.LineNo, d.Kind, e.ReceiptID)
		}
	}
	return nil
}

// earliest is LEAST with SQL NULL semantics: nil candidates are ignored.
func earliest(stored, candidate *time.Time) *time.Time {
	if candidate == nil {
		return stored
	}
	if stored == nil || candidate.Before(*stored) {
		t := *candidate
		return &t
	}
	return stored
}

// inWindow is the half-open [From, To) test.
func inWindow(t *time.Time, w report.DayWindow) bool {
	return t != nil && t.Compare(w.From) >= 0 && t.Compare(w.To) < 0
}

func inWindowAt(t time.Time, w report.DayWindow) bool { return inWindow(&t, w) }

func count(b bool) int {
	if b {
		return 1
	}
	return 0
}

// seconds is the clamped (never negative) duration between two instants.
func seconds(from, to time.Time) float64 {
	return max(0, to.Sub(from).Seconds())
}

// PerformanceDays implements report.Reader.
func (m *Memory) PerformanceDays(_ context.Context, r report.Range) ([]report.PerformanceDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	windows := report.DayWindows(r)
	out := make([]report.PerformanceDay, 0, len(windows))
	for _, w := range windows {
		d := report.PerformanceDay{Day: w.Day}
		m.countReceipts(&d, w)
		m.countLines(&d, w)
		m.countDiscrepancies(&d, w)
		m.countAppointments(&d, w)
		out = append(out, d)
	}
	return out, nil
}

func (m *Memory) countLines(d *report.PerformanceDay, w report.DayWindow) {
	for _, l := range m.lines {
		if !inWindowAt(l.at, w) {
			continue
		}
		d.LinesReceived++
		if l.condition == report.ConditionGood {
			d.UnitsGood += l.quantity
		} else {
			d.UnitsDamaged += l.quantity
		}
	}
}

func (m *Memory) countDiscrepancies(d *report.PerformanceDay, w report.DayWindow) {
	for k, closedAt := range m.discrepancy {
		if !inWindowAt(closedAt, w) {
			continue
		}
		switch k.kind {
		case report.DiscrepancyShort:
			d.Discrepancies.Short++
		case report.DiscrepancyOver:
			d.Discrepancies.Over++
		case report.DiscrepancyDamaged:
			d.Discrepancies.Damaged++
		}
	}
}

func (m *Memory) countAppointments(d *report.PerformanceDay, w report.DayWindow) {
	var dwell []float64
	for _, a := range m.appointments {
		d.Appointments.Booked += count(inWindow(a.Booked, w))
		d.Appointments.CheckedIn += count(inWindow(a.CheckedIn, w))
		d.Appointments.Cancelled += count(inWindow(a.Cancelled, w))
		d.Appointments.Completed += count(inWindow(a.Completed, w))
		if inWindow(a.Completed, w) && a.CheckedIn != nil {
			dwell = append(dwell, seconds(*a.CheckedIn, *a.Completed))
		}
	}
	d.DockDwell = report.TimingOf(dwell)
}

func (m *Memory) countReceipts(d *report.PerformanceDay, w report.DayWindow) {
	var cycle []float64
	for _, r := range m.receipts {
		if !inWindow(r.closed, w) {
			continue
		}
		d.ReceiptsClosed++
		if r.opened != nil {
			cycle = append(cycle, seconds(*r.opened, *r.closed))
		}
	}
	d.ReceiptCycle = report.TimingOf(cycle)
}

// Current implements report.Reader.
func (m *Memory) Current(_ context.Context, today report.DayWindow) (report.CurrentCounts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c report.CurrentCounts
	for _, r := range m.receipts {
		c.OpenReceipts += count(r.opened != nil && r.closed == nil)
	}
	for _, a := range m.asns {
		if a.cancelled != nil || !inWindow(a.expectedArrival, today) {
			continue
		}
		c.ExpectedToday++
		c.ExpectedTodayStarted += count(a.receiptOpened != nil)
	}
	return c, nil
}

// LastEventAt implements report.Reader.
func (m *Memory) LastEventAt(_ context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastAt == nil {
		return nil, nil
	}
	at := *m.lastAt
	return &at, nil
}
