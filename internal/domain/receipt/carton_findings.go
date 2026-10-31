package receipt

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidCartonDiscrepancy is returned when a persisted carton discrepancy
// is malformed (unknown kind, invalid SSCC, no raise time).
var ErrInvalidCartonDiscrepancy = errors.New("receipt carton discrepancy is malformed")

// CartonFinding is a carton discrepancy as it is kept on the receipt: the
// finding plus the instant it was raised (ADR 0015). A scan raises it, the
// close raises MissingCarton; the receipt keeps them in raise order.
type CartonFinding struct {
	CartonDiscrepancy
	At time.Time
}

// CartonFindingState is the stored shape of a CartonFinding. SSCC, Expected
// and Actual are empty when they do not apply.
type CartonFindingState struct {
	Kind     string
	SSCC     string
	Expected string
	Actual   string
	At       time.Time
}

var cartonKinds = map[Kind]bool{
	KindUnexpectedCarton: true, KindMissingCarton: true, KindDuplicateSscc: true, KindUnknownGtin: true,
	KindCaseQtyMismatch: true, KindMixedCaseDeclaredAsCasePack: true, KindUnlabelledCarton: true,
	KindCartonOverLimit: true, KindMixedCartonContentMismatch: true,
}

// CartonDiscrepancies returns every carton discrepancy raised on the receipt,
// in raise order, scans first and MissingCarton findings (raised at close)
// last. It is never nil; the caller may keep the slice.
func (r *Receipt) CartonDiscrepancies() []CartonFinding {
	out := make([]CartonFinding, len(r.findings))
	copy(out, r.findings)
	return out
}

// raise records the findings on the receipt and returns one
// CartonDiscrepancyRaised per finding, so the read model and the event stream
// can never disagree.
func (r *Receipt) raise(findings []CartonDiscrepancy, at time.Time) []Event {
	out := make([]Event, 0, len(findings))
	for _, d := range findings {
		r.findings = append(r.findings, CartonFinding{CartonDiscrepancy: d, At: at.UTC()})
		out = append(out, CartonDiscrepancyRaised{Header: r.header(at), CartonDiscrepancy: d})
	}
	return out
}

func buildFindings(states []CartonFindingState) ([]CartonFinding, error) {
	out := make([]CartonFinding, 0, len(states))
	for _, s := range states {
		sscc, err := optionalSSCC(s.SSCC)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidCartonDiscrepancy, err)
		}
		if !cartonKinds[Kind(s.Kind)] || s.At.IsZero() {
			return nil, ErrInvalidCartonDiscrepancy
		}
		out = append(out, CartonFinding{
			CartonDiscrepancy: CartonDiscrepancy{Kind: Kind(s.Kind), SSCC: sscc, Expected: s.Expected, Actual: s.Actual},
			At:                s.At.UTC(),
		})
	}
	return out, nil
}

func findingStates(in []CartonFinding) []CartonFindingState {
	out := make([]CartonFindingState, 0, len(in))
	for _, f := range in {
		out = append(out, CartonFindingState{
			Kind: string(f.Kind), SSCC: f.SSCC.String(), Expected: f.Expected, Actual: f.Actual, At: f.At,
		})
	}
	return out
}
