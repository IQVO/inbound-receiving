package analyticsstore

import (
	"context"
	"time"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// The report-side (Reader) methods of Memory: PerformanceDays, Current and
// LastEventAt, with the count* helpers that build each PerformanceDay block.
// Split from memory_store.go to stay under the 300-line file budget.

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
		m.countStows(&d, w)
		m.countAdvices(&d, w)
		m.countTimeliness(&d, w)
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

func (m *Memory) countStows(d *report.PerformanceDay, w report.DayWindow) {
	var secs []float64
	for _, s := range m.stows {
		if inWindowAt(s.StowedAt, w) {
			secs = append(secs, float64(s.DockToStockSeconds))
		}
	}
	d.DockToStock = report.DockToStockOf(secs)
}

func (m *Memory) countAdvices(d *report.PerformanceDay, w report.DayWindow) {
	var within []bool
	for _, a := range m.advices {
		if inWindowAt(a.issuedAt, w) {
			within = append(within, a.withinSLA)
		}
	}
	d.Advice = report.AdviceOf(within)
}

// countTimeliness folds the day's ASNs per supplier into the asn_timeliness
// block (ADR 0025): both sides of the ratio in the UTC day of the ASN's
// registration, the late side one row per ASN.
func (m *Memory) countTimeliness(d *report.PerformanceDay, w report.DayWindow) {
	registered, late := map[string]int{}, map[string]int{}
	for number, a := range m.asns {
		if !inWindow(a.registered, w) {
			continue
		}
		registered[a.supplier]++
		if _, isLate := m.lates[number]; isLate {
			late[a.supplier]++
		}
	}
	d.AsnTimeliness = report.AsnTimelinessOf(registered, late)
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
