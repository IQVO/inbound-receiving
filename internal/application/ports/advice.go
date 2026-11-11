package ports

import (
	"context"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// AdviceStore holds the markers that make the receiving advice and its overdue
// alarm exactly-once, and finds the receipts the overdue sweep must look at
// (ADR 0023). Every method runs inside the caller's unit of work.
type AdviceStore interface {
	// MarkIssued records that the receipt's ReceivingAdviceIssued was raised
	// and whether it was within the SLA. It reports true only for the call
	// that recorded it, so the event is raised exactly once.
	MarkIssued(ctx context.Context, id receipt.ID, issuedAt time.Time, withinSLA bool) (first bool, err error)
	// OverdueCandidates returns at most limit OPEN receipts, in id order, whose
	// advice clock (last received, else opened) is strictly before cutoff and
	// that have no overdue marker, and LOCKS them (FOR UPDATE SKIP LOCKED) until
	// the unit of work ends so two sweepers never take the same receipt.
	OverdueCandidates(ctx context.Context, cutoff time.Time, limit int) ([]*receipt.Receipt, error)
	// MarkOverdue records that the receipt's ReceivingAdviceOverdue was raised.
	// It reports true only for the call that recorded it.
	MarkOverdue(ctx context.Context, id receipt.ID, at time.Time) (first bool, err error)
}

// AdviceMetrics is the telemetry port of the advice KPI (ADR 0023). A nil
// AdviceMetrics on a use case means "record nothing".
type AdviceMetrics interface {
	// AdviceIssued counts one issued advice, inside the SLA or late.
	AdviceIssued(ctx context.Context, withinSLA bool)
	// AdviceOverdue counts one receipt that passed the advice SLA while Open.
	AdviceOverdue(ctx context.Context)
}
