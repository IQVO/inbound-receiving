package receipt

import "time"

// IsGood reports whether the carton was accepted and every content of it was
// received Good. Only a Good carton is expected to be stowed by inventory
// (ADR 0021): a Damaged carton is not handed over and a carton still Expected
// was never received.
func (c Carton) IsGood() bool {
	if c.status != CartonAccepted || len(c.contents) == 0 {
		return false
	}
	for _, x := range c.contents {
		if x.Condition != ConditionGood {
			return false
		}
	}
	return true
}

// GoodCartonLPNs returns the license plates of the Good cartons, in ledger
// order.
func (r *Receipt) GoodCartonLPNs() []string {
	var out []string
	for _, c := range r.cartons {
		if c.IsGood() {
			out = append(out, c.lpn)
		}
	}
	return out
}

// StowCompletion is the dock-to-stock fact of one receipt (ADR 0021): from
// the carrier's arrival at the gate to the moment the last Good carton was
// stowed.
type StowCompletion struct {
	ArrivedAt time.Time
	ClosedAt  time.Time
	StowedAt  time.Time
	// DockToStockSeconds is StowedAt - ArrivedAt in whole seconds, never
	// below 0 (two contexts' clocks may disagree).
	DockToStockSeconds int64
	// CartonCount is the number of Good cartons that were stowed.
	CartonCount int
}

// CompleteStow computes the dock-to-stock fact from the arrival and the stow
// observations (license plate to the time the carton was first stowed). It is
// pure: it reads no clock and changes nothing. It reports false until the
// fact is complete: the receipt is Closed and was opened from an appointment,
// the arrival is known, it has at least one Good carton, and EVERY Good carton
// has a stow observation. Stow observations of other cartons are ignored.
func (r *Receipt) CompleteStow(arrivedAt time.Time, stows map[string]time.Time) (StowCompletion, bool) {
	if r.state != StateClosed || r.appointmentID == "" || arrivedAt.IsZero() {
		return StowCompletion{}, false
	}
	lpns := r.GoodCartonLPNs()
	if len(lpns) == 0 {
		return StowCompletion{}, false
	}
	var last time.Time
	for _, lpn := range lpns {
		at, ok := stows[lpn]
		if !ok || at.IsZero() {
			return StowCompletion{}, false
		}
		if at.After(last) {
			last = at
		}
	}
	// A stow before the arrival (clock skew between contexts) is clamped to 0.
	seconds := max(int64(last.Sub(arrivedAt)/time.Second), 0)
	return StowCompletion{
		ArrivedAt: arrivedAt.UTC(), ClosedAt: r.closedAt, StowedAt: last.UTC(),
		DockToStockSeconds: seconds, CartonCount: len(lpns),
	}, true
}

// StowCompleted returns the ReceiptStowCompleted event for the receipt when
// CompleteStow reports the fact complete. at is when the fact was observed
// (the caller's clock); the aggregate is not changed, so raising it twice is
// prevented by the caller's marker (ADR 0021), not here.
func (r *Receipt) StowCompleted(arrivedAt time.Time, stows map[string]time.Time, at time.Time) (ReceiptStowCompleted, bool) {
	c, ok := r.CompleteStow(arrivedAt, stows)
	if !ok {
		return ReceiptStowCompleted{}, false
	}
	return ReceiptStowCompleted{
		Header:        r.header(at),
		AppointmentID: r.appointmentID, ArrivedAt: c.ArrivedAt, ClosedAt: c.ClosedAt, StowedAt: c.StowedAt,
		DockToStockSeconds: c.DockToStockSeconds, CartonCount: c.CartonCount,
	}, true
}
