package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/telemetry"
)

// receivedUnitsMetric finds the one instrument under test in a collection
// and returns it, failing the test if it is missing or the wrong type.
func receivedUnitsMetric(t *testing.T, collected metricdata.ResourceMetrics) metricdata.Metrics {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "inbound_receiving.receipts.units_received" {
				return m
			}
		}
	}
	t.Fatal("inbound_receiving.receipts.units_received not found")
	return metricdata.Metrics{}
}

// countsByOutcome sums a Sum[int64] metric's data points by their outcome
// attribute.
func countsByOutcome(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s is a %T, want an int64 Sum", m.Name, m.Data)
	}
	if !sum.IsMonotonic {
		t.Fatal("counter must be monotonic")
	}
	counts := map[string]int64{}
	for _, point := range sum.DataPoints {
		outcome, ok := point.Attributes.Value("outcome")
		if !ok {
			t.Fatal("data point has no outcome attribute")
		}
		counts[outcome.AsString()] = point.Value
	}
	return counts
}

func TestReceiptMetrics_CountsUnitsByOutcome(t *testing.T) {
	reader := metric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	metrics, err := telemetry.NewReceiptMetrics()
	if err != nil {
		t.Fatalf("NewReceiptMetrics: %v", err)
	}

	ctx := context.Background()
	metrics.LineReceived(ctx, "good", 6)
	metrics.LineReceived(ctx, "good", 15)
	metrics.LineReceived(ctx, "damaged", 2)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	m := receivedUnitsMetric(t, collected)
	counts := countsByOutcome(t, m)
	if counts["good"] != 21 {
		t.Errorf("outcome=good count = %d, want 21", counts["good"])
	}
	if counts["damaged"] != 2 {
		t.Errorf("outcome=damaged count = %d, want 2", counts["damaged"])
	}
	if m.Unit != "{unit}" {
		t.Errorf("unit = %q, want {unit}", m.Unit)
	}
	if m.Description == "" {
		t.Error("description must not be empty")
	}
}

func TestReceiptMetrics_RecordingNeverPanics(t *testing.T) {
	// Whatever the global provider happens to be -- the no-op one before
	// Setup runs, a real one after -- recording is fire-and-forget and
	// must never take the use case down with it.
	metrics, err := telemetry.NewReceiptMetrics()
	if err != nil {
		t.Fatalf("NewReceiptMetrics: %v", err)
	}
	metrics.LineReceived(context.Background(), "good", 1)
	metrics.LineReceived(context.Background(), "damaged", 1)
}
