package report

import (
	"math"
	"sort"
	"time"
)

// Timing summarises a set of durations: how many there were and their median
// and 95th percentile in seconds. P50 and P95 are nil while Count is 0.
type Timing struct {
	Count int
	P50   *float64
	P95   *float64
}

// DockToStockTargetSeconds is the WERC best-in-class dock-to-stock, 3.1 h
// (ADR 0021): the target the report's share is measured against.
const DockToStockTargetSeconds = 11160

// DockToStock summarises the dock-to-stock facts of a day: how many there were,
// their median and 90th percentile in seconds, how many were at or under the
// target and that count's share. The percentiles and the share are nil while
// Count is 0.
type DockToStock struct {
	Count            int
	P50              *float64
	P90              *float64
	UnderTarget      int
	ShareUnderTarget *float64
}

// NewDockToStock builds the summary from what a store counted: the number of
// facts, their percentiles and how many were at or under the target. It is
// where the share is defined, so every store agrees.
func NewDockToStock(count int, p50, p90 *float64, underTarget int) DockToStock {
	d := DockToStock{Count: count, P50: p50, P90: p90, UnderTarget: underTarget}
	if count > 0 {
		share := float64(underTarget) / float64(count)
		d.ShareUnderTarget = &share
	}
	return d
}

// DockToStockOf summarises durations in seconds (any order).
func DockToStockOf(seconds []float64) DockToStock {
	if len(seconds) == 0 {
		return DockToStock{}
	}
	s := append([]float64(nil), seconds...)
	sort.Float64s(s)
	p50, p90 := Percentile(s, 0.50), Percentile(s, 0.90)
	under := 0
	for _, v := range s {
		if v <= DockToStockTargetSeconds {
			under++
		}
	}
	return NewDockToStock(len(s), &p50, &p90, under)
}

// Percentile is the p-quantile (0..1) of an ascending-sorted sample by linear
// interpolation between closest ranks: the same definition as PostgreSQL's
// percentile_cont, so the in-memory and SQL stores agree. It is 0 for an
// empty sample.
func Percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := p * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

// TimingOf summarises durations in seconds (any order).
func TimingOf(seconds []float64) Timing {
	if len(seconds) == 0 {
		return Timing{}
	}
	s := append([]float64(nil), seconds...)
	sort.Float64s(s)
	p50, p95 := Percentile(s, 0.50), Percentile(s, 0.95)
	return Timing{Count: len(s), P50: &p50, P95: &p95}
}

// Seconds is d in seconds as a float.
func Seconds(d time.Duration) float64 { return d.Seconds() }

// AdviceSummary counts the receiving advices (ADR 0023) issued in a window and
// how many were inside the SLA: the KPI "% receipts with advice inside the SLA".
// ShareWithinSLA is nil while Count is 0.
type AdviceSummary struct {
	Count          int
	WithinSLA      int
	ShareWithinSLA *float64
}

// NewAdviceSummary builds the summary from what a store counted. It is where
// the share is defined, so every store agrees.
func NewAdviceSummary(count, withinSLA int) AdviceSummary {
	a := AdviceSummary{Count: count, WithinSLA: withinSLA}
	if count > 0 {
		share := float64(withinSLA) / float64(count)
		a.ShareWithinSLA = &share
	}
	return a
}

// AdviceOf summarises advices given as "was it inside the SLA" flags.
func AdviceOf(withinSLA []bool) AdviceSummary {
	within := 0
	for _, w := range withinSLA {
		if w {
			within++
		}
	}
	return NewAdviceSummary(len(withinSLA), within)
}
