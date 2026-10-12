// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary
// (errors, filters, messages) lives in internal/application/repository and
// internal/application/outbox.
package ports

import "context"

// ReceiptMetrics is the outbound telemetry port for the Tier-2 business
// signal on the Receipt aggregate's core write operation, ReceiveLine
// (ADR-0013): how many units actually land at the dock against an ASN, and
// how many of them arrive in a condition that can never become stock. A nil
// ReceiptMetrics on the use case means "record nothing" -- the use case
// does not require telemetry to function.
type ReceiptMetrics interface {
	// LineReceived records quantity units accepted onto a receipt line in
	// the given condition ("good" or "damaged").
	LineReceived(ctx context.Context, condition string, quantity int64)
}
