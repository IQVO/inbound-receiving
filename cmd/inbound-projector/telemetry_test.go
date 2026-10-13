package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestSetupTelemetry_InstallsTheSDKProviders pins gap fix D21: the projector
// calls telemetry.Setup like cmd/api, cmd/mcp and cmd/inbound-reports, so the
// OTEL_EXPORTER_OTLP_ENDPOINT / SERVICE_VERSION / ENVIRONMENT values the
// chart injects are used (providers, the W3C propagator, Go runtime
// metrics). An unreachable Collector must not fail boot.
func TestSetupTelemetry_InstallsTheSDKProviders(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")
	t.Setenv("SERVICE_VERSION", "test")

	shutdown, err := setupTelemetry(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("setupTelemetry: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("global tracer provider = %T, want the SDK provider telemetry.Setup installs", otel.GetTracerProvider())
	}
	if projectorServiceName != "inbound-receiving-projector" {
		t.Fatalf("service name = %q", projectorServiceName)
	}
}
