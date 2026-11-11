package receipt

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// EarliestDates are the derived earliest best-before and expiry of a set of
// cartons (ADR 0024). A field is "" when no carton had a usable date.
type EarliestDates struct {
	BestBefore string
	Expiry     string
}

// Pallet is the accepted cartons that share a parent SSCC and the earliest
// dates among them.
type Pallet struct {
	ParentSSCC  gs1.SSCC
	CartonCount int
	Earliest    EarliestDates
}

// EarliestDate returns the earliest of the given ISO dates as it was written,
// or "" when none is a date. A literal "-00" day is compared as the last day of
// its month; of two equal days the first wins; a string that is not a date (a
// bad month or day, the wrong shape) is skipped.
func EarliestDate(dates ...string) string {
	best, bestDay := "", time.Time{}
	for _, d := range dates {
		day, ok := parseDay(d)
		if !ok {
			continue
		}
		if best == "" || day.Before(bestDay) {
			best, bestDay = d, day
		}
	}
	return best
}

// parseDay reads YYYY-MM-DD; day 00 is the last day of the month.
func parseDay(s string) (time.Time, bool) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return time.Time{}, false
	}
	year, ok1 := digits(s[0:4])
	month, ok2 := digits(s[5:7])
	day, ok3 := digits(s[8:10])
	if !ok1 || !ok2 || !ok3 || month < 1 || month > 12 {
		return time.Time{}, false
	}
	last := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day == 0 {
		day = last
	}
	if day > last {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

func digits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// Label returns what the carton's label said; the zero value when it said nothing.
func (c Carton) Label() LabelData { return c.label }

// SSCCSource returns who gave the carton its SSCC: the receiver when it was
// assigned, the supplier for any other SSCC (so a row written before the field
// existed reads right), "" for a carton with no SSCC.
func (c Carton) SSCCSource() SsccSource {
	if c.sscc.IsZero() {
		return ""
	}
	if c.ssccSource == SsccReceiver {
		return SsccReceiver
	}
	return SsccSupplier
}

// expiry is the carton's label expiry when it is a date, else the earliest
// expiry among its contents.
func (c Carton) expiry() string {
	if e := EarliestDate(c.label.Expiry); e != "" {
		return e
	}
	all := make([]string, 0, len(c.contents))
	for _, x := range c.contents {
		all = append(all, x.Expiry)
	}
	return EarliestDate(all...)
}

// Pallets returns, in ledger order, one entry per parent SSCC that has accepted
// cartons, with the count of accepted cartons and their earliest dates. Cartons
// not yet accepted and cartons with no pallet are not part of it.
func (r *Receipt) Pallets() []Pallet {
	var order []gs1.SSCC
	groups := map[gs1.SSCC][]Carton{}
	for _, c := range r.cartons {
		if c.status != CartonAccepted || c.parentSscc.IsZero() {
			continue
		}
		if _, seen := groups[c.parentSscc]; !seen {
			order = append(order, c.parentSscc)
		}
		groups[c.parentSscc] = append(groups[c.parentSscc], c)
	}
	out := make([]Pallet, 0, len(order))
	for _, parent := range order {
		cartons := groups[parent]
		out = append(out, Pallet{ParentSSCC: parent, CartonCount: len(cartons), Earliest: earliestOf(cartons)})
	}
	return out
}

// LineDates returns the earliest best-before and expiry over the accepted
// cartons whose contents include the line.
func (r *Receipt) LineDates(lineNo int) EarliestDates {
	var cartons []Carton
	for _, c := range r.cartons {
		if c.status != CartonAccepted {
			continue
		}
		for _, x := range c.contents {
			if x.LineNo == lineNo {
				cartons = append(cartons, c)
				break
			}
		}
	}
	return earliestOf(cartons)
}

func earliestOf(cartons []Carton) EarliestDates {
	bestBefore := make([]string, 0, len(cartons))
	expiry := make([]string, 0, len(cartons))
	for _, c := range cartons {
		bestBefore = append(bestBefore, c.label.BestBefore)
		expiry = append(expiry, c.expiry())
	}
	return EarliestDates{BestBefore: EarliestDate(bestBefore...), Expiry: EarliestDate(expiry...)}
}
