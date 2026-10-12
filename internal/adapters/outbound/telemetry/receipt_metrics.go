package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
)

// meterName scopes this service's own business instruments, keeping them
// distinct from the ones otelchi, otelpgx and the Go runtime collector
// register on the same MeterProvider.
const meterName = "github.com/claudioed/inbound-receiving"

// receiptLineUnitsCounterName is the Tier-2 business metric (ADR-0013): the
// Receipt aggregate's core write operation is ReceiveLine -- recording what
// actually arrived at the dock for one ASN line. outcome=damaged is the one
// to watch: ADR 0003 only hands Good units over to inventory-storage, so a
// rising damaged share is cargo that was announced but never becomes stock.
const receiptLineUnitsCounterName = "inbound_receiving.receipts.units_received"

// outcomeKey carries the receipt line's Condition (good or damaged) --
// WHAT happened to the units -- rather than splitting this into two
// separate counters for the same ReceiptLineReceived event.
var outcomeKey = attribute.Key("outcome")

// ReceiptMetrics implements ports.ReceiptMetrics against the global
// MeterProvider (a no-op until telemetry.Setup installs a real one), the
// same registry CircuitBreakerMetrics-style adapters in this fleet reuse
// rather than standing up a second, parallel registry.
type ReceiptMetrics struct {
	counter metric.Int64Counter
}

var _ ports.ReceiptMetrics = (*ReceiptMetrics)(nil)

// NewReceiptMetrics registers the counter. It only fails on an invalid
// instrument name -- a programming error -- so a caller that would rather
// run un-instrumented than not at all can ignore the error and pass a nil
// ports.ReceiptMetrics instead of propagating it fatally.
func NewReceiptMetrics() (*ReceiptMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		receiptLineUnitsCounterName,
		metric.WithDescription("Units accepted onto inbound-receiving receipt lines, by outcome (good or damaged); the damaged share never becomes stock (ADR 0003)."),
		metric.WithUnit("{unit}"),
	)
	if err != nil {
		return nil, err
	}
	return &ReceiptMetrics{counter: counter}, nil
}

// LineReceived implements ports.ReceiptMetrics.
func (m *ReceiptMetrics) LineReceived(ctx context.Context, condition string, quantity int64) {
	m.counter.Add(ctx, quantity, metric.WithAttributes(outcomeKey.String(condition)))
}
