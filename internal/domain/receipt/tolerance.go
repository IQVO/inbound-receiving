// Tolerance: the receiving policy's effect on the receipt (ADR 0026). The
// band and the reason codes live here; the close and the scan judge the
// variance of a line as |received - expected| / expected against the band:
// at or below the tolerance the variance is ACCEPTED (no finding), between
// the tolerance and the hard stop an override reason code is required (the
// finding is OverTolerance/UnderTolerance carrying it), at or beyond the hard
// stop the close or scan is refused outright.
package receipt

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

var (
	// ErrInvalidOverrideReason is returned for a reason code outside the
	// closed list of ADR 0026.
	ErrInvalidOverrideReason = errors.New("override reason must be one of the declared receiving reason codes")
	// ErrToleranceOverrideRequired marks a variance that sits between the
	// band and the hard stop and was closed or scanned without a reason code.
	ErrToleranceOverrideRequired = errors.New("variance is beyond the tolerance band and needs an override reason code")
	// ErrToleranceBlocked marks a variance at or beyond the hard stop: no
	// override can pass and the receipt stays open.
	ErrToleranceBlocked = errors.New("variance is at or beyond the tolerance hard stop")
)

// OverrideReason is a close/scan override reason code (ADR 0026). The list is
// ours: no source in the research names a code, so the domain declares a
// small closed set and refuses everything else.
type OverrideReason string

// The reason codes of ADR 0026.
const (
	// ReasonSupplierAgreed: the supplier confirmed the quantity in advance.
	ReasonSupplierAgreed OverrideReason = "supplier-agreed"
	// ReasonDamagedInTransitRecounted: the variance was traced to transit
	// damage and the count re-verified with the carrier note.
	ReasonDamagedInTransitRecounted OverrideReason = "damaged-in-transit-recounted"
	// ReasonMislabelledRecounted: mislabelled cartons; a physical recount
	// confirmed the booked quantity.
	ReasonMislabelledRecounted OverrideReason = "mislabelled-recounted"
	// ReasonRecountConfirmed: a full recount, for an audit or a claim,
	// confirmed the booked quantity against the paperwork.
	ReasonRecountConfirmed OverrideReason = "recount-confirmed"
)

var overrideReasons = map[OverrideReason]bool{
	ReasonSupplierAgreed: true, ReasonDamagedInTransitRecounted: true,
	ReasonMislabelledRecounted: true, ReasonRecountConfirmed: true,
}

// ParseOverrideReason validates a reason text against the closed list.
func ParseOverrideReason(value string) (OverrideReason, error) {
	r := OverrideReason(value)
	if !overrideReasons[r] {
		return "", fmt.Errorf("%w: %q", ErrInvalidOverrideReason, value)
	}
	return r, nil
}

// Band is the effective over/under band of a receiving policy (ADR 0026):
// the tolerance ratios and the hard stops, per side. A side is DECLARED when
// its hard stop is positive (a validated policy always has tolerance <
// hard stop, so a positive stop implies a band); an undeclared side keeps
// today's behaviour for that side. The zero value declares nothing.
type Band struct {
	Over      float64
	OverStop  float64
	Under     float64
	UnderStop float64
}

// Declared reports whether any side of the band is declared.
func (b Band) Declared() bool { return b.OverDeclared() || b.UnderDeclared() }

// OverDeclared reports whether the over side is declared.
func (b Band) OverDeclared() bool { return b.OverStop > 0 }

// UnderDeclared reports whether the under side is declared.
func (b Band) UnderDeclared() bool { return b.UnderStop > 0 }

// AppliedPolicy is what a close was evaluated under (ADR 0026): the band and
// the override reason the operator confirmed. The zero value is "no policy":
// exactly today's close.
type AppliedPolicy struct {
	Band   Band
	Reason OverrideReason
}

// Declared reports whether a policy was applied.
func (p AppliedPolicy) Declared() bool { return p.Band.Declared() }

// bandState is where a variance ratio sits.
type bandState int

const (
	insideBand bandState = iota
	overrideZone
	blockedBand
)

// classifySide pins the boundaries of ADR 0026: equal to the tolerance is
// INSIDE, equal to the hard stop is BLOCKED.
func classifySide(variance, tolerance, stop float64) bandState {
	if variance <= tolerance {
		return insideBand
	}
	if variance < stop {
		return overrideZone
	}
	return blockedBand
}

// varianceOf is the ratio |received - expected| / expected; expected is >= 1
// by the receipt's invariants, so the ratio is always defined.
func varianceOf(received, expected int64) float64 {
	diff := received - expected
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) / float64(expected)
}

// ToleranceError is the refusal of a close or a scan under a policy: the
// line, its quantities, the offending side, the variance ratio, the band it
// was judged against and whether the hard stop (no override possible) or the
// missing reason code (override required) refused it. The HTTP adapter maps
// it to the 409 problems of ADR 0026 with this detail.
type ToleranceError struct {
	LineNo      int
	SKU         shared.SKU
	Expected    int64
	Received    int64
	Over        bool
	Variance    float64
	Tolerance   float64
	HardStop    float64
	NeedsReason bool
	Blocked     bool
}

// Error implements error.
func (e *ToleranceError) Error() string {
	side, act := "under", "short of"
	if e.Over {
		side, act = "over", "above"
	}
	if e.Blocked {
		return fmt.Sprintf("line %d (%s) received %d, %d %s %d expected: variance %.4f is at or beyond the %s hard stop %.4f, no override can pass",
			e.LineNo, e.SKU, e.Received, e.Received-e.Expected, act, e.Expected, e.Variance, side, e.HardStop)
	}
	return fmt.Sprintf("line %d (%s) received %d, %d %s %d expected: variance %.4f is beyond the %s tolerance %.4f and below the hard stop %.4f, an override reason code is required",
		e.LineNo, e.SKU, e.Received, e.Received-e.Expected, act, e.Expected, e.Variance, side, e.Tolerance, e.HardStop)
}

// Unwrap maps the refusal to its sentinel.
func (e *ToleranceError) Unwrap() error {
	if e.Blocked {
		return ErrToleranceBlocked
	}
	return ErrToleranceOverrideRequired
}

// CloseUnderPolicy ends receiving under a receiving policy (ADR 0026): lines
// inside the band raise no over/under finding, override-zone lines raise
// OverTolerance/UnderTolerance carrying the reason, and a hard-stop line
// refuses the close (the receipt stays open). AppliedPolicy's zero value is
// exactly Close.
func (r *Receipt) CloseUnderPolicy(at time.Time, p AppliedPolicy) ([]Event, error) {
	if r.state != StateOpen {
		return nil, ErrReceiptClosed
	}
	ds, err := r.policyDiscrepancies(p)
	if err != nil {
		return nil, err
	}
	var applied *AppliedPolicy
	if p.Declared() {
		applied = &p
	}
	return r.finishClose(at, ds, applied), nil
}

// policyDiscrepancies computes the close's discrepancies under p: today's
// kinds for undeclared sides and without a policy, the tolerance kinds
// between band and hard stop, nothing inside the band. Damaged is untouched:
// condition is a different dimension. The order keeps Discrepancies': under,
// over, damaged, per line.
func (r *Receipt) policyDiscrepancies(p AppliedPolicy) ([]Discrepancy, error) {
	out := make([]Discrepancy, 0)
	for _, l := range r.lines {
		if d, ok, err := sideFinding(l, p, false); err != nil {
			return nil, err
		} else if ok {
			out = append(out, d)
		} else if !p.Band.UnderDeclared() && l.Received() < l.expectedQty {
			out = append(out, l.discrepancy(KindShort))
		}
		if d, ok, err := sideFinding(l, p, true); err != nil {
			return nil, err
		} else if ok {
			out = append(out, d)
		} else if !p.Band.OverDeclared() && l.Received() > l.expectedQty {
			out = append(out, l.discrepancy(KindOver))
		}
		if l.receivedDamaged > 0 {
			out = append(out, l.discrepancy(KindDamaged))
		}
	}
	return out, nil
}

// sideFinding judges one side of one line under p. judged says the side is
// declared; the finding is reported (ok) only in the override zone.
func sideFinding(l Line, p AppliedPolicy, over bool) (d Discrepancy, ok bool, err error) {
	declared, diff := p.Band.OverDeclared(), l.Received()-l.expectedQty
	kind, tolerance, stop := KindOverTolerance, p.Band.Over, p.Band.OverStop
	if !over {
		declared, diff = p.Band.UnderDeclared(), l.expectedQty-l.Received()
		kind, tolerance, stop = KindUnderTolerance, p.Band.Under, p.Band.UnderStop
	}
	if !declared || diff <= 0 {
		return Discrepancy{}, false, nil
	}
	variance := varianceOf(l.Received(), l.expectedQty)
	switch classifySide(variance, tolerance, stop) {
	case insideBand:
		return Discrepancy{}, false, nil
	case overrideZone:
		if p.Reason == "" {
			return Discrepancy{}, false, refusal(l, over, variance, tolerance, stop, false)
		}
		d := l.discrepancy(kind)
		d.Reason = p.Reason
		return d, true, nil
	default:
		return Discrepancy{}, false, refusal(l, over, variance, tolerance, stop, true)
	}
}

func refusal(l Line, over bool, variance, tolerance, stop float64, blocked bool) error {
	return &ToleranceError{
		LineNo: l.lineNo, SKU: l.sku, Expected: l.expectedQty, Received: l.Received(),
		Over: over, Variance: variance, Tolerance: tolerance, HardStop: stop,
		NeedsReason: !blocked, Blocked: blocked,
	}
}

// toleranceFinding is the advice-side twin of sideFinding: the entry of an
// override-zone variance, with its quantity and its reason. A variance that
// cannot happen on a closed receipt (the close would have refused it) is
// still reported, never dropped.
func toleranceFinding(l Line, p AppliedPolicy, over bool) (AdviceDiscrepancy, bool) {
	d, ok, err := sideFinding(l, p, over)
	if err != nil || !ok {
		return AdviceDiscrepancy{}, false
	}
	q := l.Received() - l.expectedQty
	if !over {
		q = l.expectedQty - l.Received()
	}
	coding, _ := CodingOf(d.Kind)
	return AdviceDiscrepancy{Kind: d.Kind, Quantity: q, Coding: coding, Reason: d.Reason}, true
}

// CheckScanTolerance judges the OVER side of the given lines (the ones a scan
// just booked) against the band, on the receipt's state as it stands after
// the booking (ADR 0026). An open receipt is legitimately short mid-flight,
// so the under side is a close-time judgement; a line the scan did not book
// is not judged. It changes nothing: the caller refuses the unit of work on
// error, so a blocked or unconfirmed booking leaves nothing behind.
func (r *Receipt) CheckScanTolerance(band Band, lineNos []int, reason OverrideReason) error {
	for _, lineNo := range lineNos {
		if lineNo < 1 || lineNo > len(r.lines) {
			return ErrLineNotOnAsn
		}
		if _, _, err := sideFinding(r.lines[lineNo-1], AppliedPolicy{Band: band, Reason: reason}, true); err != nil {
			return err
		}
	}
	return nil
}
