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

// The advice KPI instruments (ADR 0023). The Prometheus exporter appends the
// counter suffix, so they are read as inbound_receiving_advice_issued_total and
// inbound_receiving_advice_overdue_total.
const (
	adviceIssuedCounterName  = "inbound_receiving.advice.issued"
	adviceOverdueCounterName = "inbound_receiving.advice.overdue"
)

var withinSLAKey = attribute.Key("within_sla")

// AdviceMetrics implements ports.AdviceMetrics: every receiving advice issued
// (within_sla=true|false, the KPI "% receipts with advice inside the SLA" is
// true / (true + false)) and every Open receipt that passed the advice SLA.
type AdviceMetrics struct {
	issued  metric.Int64Counter
	overdue metric.Int64Counter
}

var _ ports.AdviceMetrics = (*AdviceMetrics)(nil)

// NewAdviceMetrics registers the two counters on the global MeterProvider.
func NewAdviceMetrics() (*AdviceMetrics, error) {
	meter := otel.Meter(meterName)
	issued, err := meter.Int64Counter(adviceIssuedCounterName,
		metric.WithDescription("Receiving advices issued at close, by whether they were inside the SLA (within_sla=true|false)."),
		metric.WithUnit("{advice}"))
	if err != nil {
		return nil, err
	}
	overdue, err := meter.Int64Counter(adviceOverdueCounterName,
		metric.WithDescription("Open receipts that passed the receiving advice SLA since their last unit was received (ReceivingAdviceOverdue)."),
		metric.WithUnit("{receipt}"))
	if err != nil {
		return nil, err
	}
	return &AdviceMetrics{issued: issued, overdue: overdue}, nil
}

// AdviceIssued implements ports.AdviceMetrics.
func (m *AdviceMetrics) AdviceIssued(ctx context.Context, withinSLA bool) {
	m.issued.Add(ctx, 1, metric.WithAttributes(withinSLAKey.Bool(withinSLA)))
}

// AdviceOverdue implements ports.AdviceMetrics.
func (m *AdviceMetrics) AdviceOverdue(ctx context.Context) {
	m.overdue.Add(ctx, 1)
}
