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
	stows        map[string]report.StowCompleted // by receipt id
	advices      map[string]memAdvice            // by receipt id
	discrepancy  map[discrepancyKey]time.Time
	// lates is the asn_late_facts twin (ADR 0025): one row per ASN, the
	// first fact wins.
	lates  map[string]struct{}
	lastAt *time.Time
}

type memAsn struct {
	expectedArrival *time.Time
	cancelled       *time.Time
	receiptOpened   *time.Time
	// supplier and registeredAt fold from ASNRegistered (and from the late
	// fact's payload) for the asn_timeliness block (ADR 0025).
	supplier   string
	registered *time.Time
}

type memReceipt struct {
	opened, closed *time.Time
}

type memLine struct {
	at        time.Time
	quantity  int64
	condition report.Condition
}

type memAdvice struct {
	issuedAt  time.Time
	withinSLA bool
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
		stows:        map[string]report.StowCompleted{},
		advices:      map[string]memAdvice{},
		lates:        map[string]struct{}{},
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
	switch e.Kind {
	case report.KindAsnRegistered:
		if a.expectedArrival == nil && e.ExpectedArrival != nil {
			u := e.ExpectedArrival.UTC()
			a.expectedArrival = &u
		}
		m.foldAsnRegistered(a, e)
	case report.KindAsnLateForArrival:
		// The late fact also carries the supplier and the registration day
		// (the SQL upsert does the same), so the join never misses.
		m.foldAsnRegistered(a, e)
		if _, exists := m.lates[e.AsnNumber]; !exists { // one row per ASN: the first wins
			m.lates[e.AsnNumber] = struct{}{}
		}
	default:
		a.cancelled = earliest(a.cancelled, &at)
	}
}

// foldAsnRegistered keeps the supplier (first one wins, the SQL COALESCE) and
// the earliest registration time.
func (m *Memory) foldAsnRegistered(a *memAsn, e report.Event) {
	if a.supplier == "" {
		a.supplier = e.SupplierRef
	}
	registered := e.AsnRegisteredAt
	if e.Kind == report.KindAsnRegistered {
		registered = e.At
	}
	a.registered = earliest(a.registered, &registered)
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
	if e.Kind == report.KindReceiptStowCompleted {
		if _, exists := m.stows[e.ReceiptID]; !exists { // one row per receipt: the first wins
			s := *e.Stow
			s.ArrivedAt, s.StowedAt = s.ArrivedAt.UTC(), s.StowedAt.UTC()
			m.stows[e.ReceiptID] = s
		}
		return
	}
	if e.Kind == report.KindReceivingAdviceIssued {
		if _, exists := m.advices[e.ReceiptID]; !exists { // one row per receipt: the first wins
			m.advices[e.ReceiptID] = memAdvice{issuedAt: at, withinSLA: e.Advice.WithinSLA}
		}
		return
	}
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
	if s := e.Stow; s != nil && (s.DockToStockSeconds < 0 || s.CartonCount < 1) {
		return fmt.Errorf("%w: stow of %s violates a check constraint", report.ErrRejected, e.ReceiptID)
	}
	if e.Kind == report.KindReceivingAdviceIssued && e.Advice == nil {
		return fmt.Errorf("%w: advice of %s has no within_sla (a NOT NULL column)", report.ErrRejected, e.ReceiptID)
	}
	if e.Kind == report.KindAsnLateForArrival && e.LatenessSeconds < 1 {
		return fmt.Errorf("%w: late fact of %s violates a check constraint", report.ErrRejected, e.AsnNumber)
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
