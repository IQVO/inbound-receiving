package report

// The receiving-tolerance types of the projection (ADR 0026), split out of
// report.go to keep it under the file-length budget.

// ToleranceBands is the receiving policy a close was judged under (ADR 0026):
// presence on a ReceiptClosed means the close was policy-ruled.
type ToleranceBands struct {
	Over      float64
	Under     float64
	OverStop  float64
	UnderStop float64
}

// OverrideReasonCount is one row of the tolerance block's reason Pareto.
type OverrideReasonCount struct {
	Reason string
	Count  int
}

// MaxOverrideReasons bounds the reason Pareto of one day (ADR 0026): a
// bounded block is a score, an unbounded one is a table dump.
const MaxOverrideReasons = 5

// ToleranceSummary is the `tolerance` block of one day of the report (ADR
// 0026): how the declared bands changed the close answers of the day.
type ToleranceSummary struct {
	// Closed is the day's receipts closed (all of them, policy or not);
	// Overrides those closed with at least one tolerance finding.
	Closed    int
	Overrides int
	// ShareWithoutOverride is 1 - overrides / closed, nil when none closed.
	ShareWithoutOverride *float64
	// WithPolicy counts the closes judged under a declared policy;
	// AutoAccepted those whose every variance was inside the band.
	WithPolicy   int
	AutoAccepted int
	// Reasons is the day's override-reason Pareto, biggest count first,
	// ties by reason ascending, at most MaxOverrideReasons rows.
	Reasons []OverrideReasonCount
}

// NewToleranceSummary computes the share over the day's closes.
func NewToleranceSummary(closed, overrides, withPolicy, autoAccepted int, reasons []OverrideReasonCount) ToleranceSummary {
	out := ToleranceSummary{Closed: closed, Overrides: overrides, WithPolicy: withPolicy, AutoAccepted: autoAccepted, Reasons: reasons}
	if closed > 0 {
		share := 1 - float64(overrides)/float64(closed)
		out.ShareWithoutOverride = &share
	}
	return out
}
