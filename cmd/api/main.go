// Command api is inbound-receiving's composition root: it wires env config
// into adapters, adapters into use cases, and use cases into the HTTP router,
// the two local-copy consumers (ADR 0003) and the outbox relay, all on real
// Postgres and real Kafka (ADR 0011).
//
// Environment:
//
//	HTTP_ADDR                     listen address (default :8080)
//	DATABASE_URL                  Postgres DSN (REQUIRED: there is no in-memory mode, ADR 0011)
//	MIGRATIONS_DATABASE_URL       direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS                 comma-separated brokers (REQUIRED: relay, consumers, /readyz)
//	EVENT_PUBLISHER               kafka (default kafka); log is a boot error
//	OUTBOX_RELAY_INTERVAL         relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS           failures before the relay excludes a row (default 20, 0 = unlimited)
//	CARTON_MAX_GROSS_WEIGHT_G     CartonOverLimit weight limit in grams (default 0 = unchecked, ADR 0014)
//	CARTON_MAX_LONGEST_SIDE_MM    CartonOverLimit longest-side limit in mm (default 0 = unchecked, ADR 0014)
//	HOUSEKEEPING_INTERVAL         sweeper interval (default 1h, 0 = sweeper off)
//	IDEMPOTENCY_KEY_TTL           idempotency_keys row lifetime (default 24h, 0 = keep forever)
//	OUTBOX_RETENTION              published outbox_events retention (default 168h, 0 = keep forever)
//	PROCESSED_EVENTS_RETENTION    processed_events dedupe-mark retention (default 720h, 0 = keep forever)
//	PRODUCT_MODE                  kafka (default kafka): known SKUs; permissive/off are boot errors
//	PRODUCT_CONSUMER_GROUP        stable group of the product consumer (REQUIRED)
//	DOCK_DOOR_MODE                kafka (default kafka): known inbound doors; permissive/off are boot errors
//	DOCK_DOOR_CONSUMER_GROUP      stable group of the dock-door consumer (REQUIRED)
//	PACKAGING_CONSUMER_GROUP      stable group of the packaging consumer feeding the PackagingView (REQUIRED)
//	OTEL_EXPORTER_OTLP_ENDPOINT   OTLP/gRPC collector (default localhost:4317)
//	SHUTDOWN_DRAIN_DELAY          wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT, CORS_ALLOWED_ORIGINS
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a metadata request (verdict cached 3s, bounded 2s); /healthz is
// liveness only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/telemetry"
	"github.com/claudioed/inbound-receiving/internal/housekeeping"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for the consumers and the
// relay to return after their context is cancelled.
const workerDrainTimeout = 10 * time.Second

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the integration test stops the real entry point that way).
func run(ctx context.Context) error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened.
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	otelShutdown, err := setupTelemetry(logger)
	if err != nil {
		return err
	}
	defer otelShutdown()

	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain, the consumers and the relay have all stopped.
	ad, closeAdapters, err := buildAdapters(ctx, os.Getenv("DATABASE_URL"), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	metrics, err := telemetry.NewMetricsHandler()
	if err != nil {
		return fmt.Errorf("metrics handler: %w", err)
	}
	receiptMetrics, err := telemetry.NewReceiptMetrics()
	if err != nil {
		return fmt.Errorf("receipt metrics: %w", err)
	}
	readiness := newReadiness(ad.pool.Ping, cfg.kafkaBrokers)
	httpServer := &http.Server{
		Addr:              getenv("HTTP_ADDR", ":8080"),
		Handler:           inboundhttp.NewRouter(buildServer(ad, readiness, metrics, receiptMetrics)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumers := startConsumers(ad, cfg, logger)
	if sw := startSweeper(ad.sweeper, logger); sw != nil {
		consumers = append(consumers, sw)
	}
	relay, closeRelay, err := startOutboxRelay(ad.outboxStore, cfg.kafkaBrokers, logger)
	if err != nil {
		for _, c := range consumers {
			c.stop()
		}
		return err
	}
	return serveUntilSignal(ctx, logger, httpServer, readiness, consumers, relay, closeRelay)
}

// newSweeper builds the housekeeping sweeper (gap fix D24, ADR 0010) from
// HOUSEKEEPING_INTERVAL, IDEMPOTENCY_KEY_TTL, OUTBOX_RETENTION and
// PROCESSED_EVENTS_RETENTION.
func newSweeper(pool *pgxpool.Pool, env func(string) string, logger *slog.Logger) *postgres.Sweeper {
	return postgres.NewSweeper(pool,
		postgres.WithSweepInterval(housekeeping.Duration(env, housekeeping.EnvInterval, postgres.DefaultSweepInterval, logger)),
		postgres.WithIdempotencyKeyTTL(housekeeping.Duration(env, housekeeping.EnvIdempotencyKeyTTL, postgres.DefaultIdempotencyKeyTTL, logger)),
		postgres.WithOutboxRetention(housekeeping.Duration(env, housekeeping.EnvOutboxRetention, postgres.DefaultOutboxRetention, logger)),
		postgres.WithProcessedEventsRetention(housekeeping.Duration(env, housekeeping.EnvProcessedEventsRetention, postgres.DefaultProcessedEventsRetention, logger)),
		postgres.WithSweeperLogger(logger),
	)
}

// startSweeper runs the sweeper as a background worker (nil only when no
// sweeper was built, as in unit tests). It is stopped with the consumers,
// before the relay.
func startSweeper(s *postgres.Sweeper, logger *slog.Logger) *worker {
	if s == nil {
		return nil
	}
	logger.Info("housekeeping sweeper running")
	return startWorker(logger, "housekeeping-sweeper", func(ctx context.Context) error {
		_ = s.Run(ctx) // logs its own failed passes; returns nil on cancel
		return ctx.Err()
	}, func() {})
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers (outbox
// writers), (5) stop and await the relay LAST so nothing committed above is
// stranded; the caller's deferred pool.Close runs after all of it.
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	consumers []*worker, relay *worker, closeRelay func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	died := watchWorkers(append([]*worker{relay}, consumers...)...)
	select {
	case err := <-errCh:
		for _, c := range consumers {
			c.stop()
		}
		closeRelay()
		return err
	case name := <-died:
		// A consumer or the relay returned on its own (a lost broker, a
		// failed reader): the service cannot run without it and must not sit
		// "alive" with a dead worker, so exit non-zero and let the
		// orchestrator restart it, like the analytics projector does.
		for _, c := range consumers {
			c.stop()
		}
		closeRelay()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", name)
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	for _, c := range consumers {
		c.stop()
		await(logger, c)
	}
	closeRelay()
	await(logger, relay)
	return err
}

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset,
// negative or unparsable = the default).
func shutdownDrainDelay(logger *slog.Logger) time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", DefaultShutdownDrainDelay.String())
		return DefaultShutdownDrainDelay
	}
	return d
}

// setupTelemetry installs the OTLP trace and metric providers (never blocks
// on a missing Collector) and returns a bounded flush-on-exit closer.
func setupTelemetry(logger *slog.Logger) (func(), error) {
	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	otelShutdown, err := telemetry.Setup(otelCtx, inboundhttp.DefaultServiceName, getenv("SERVICE_VERSION", "dev"),
		getenv("OTEL_EXPORTER_OTLP_ENDPOINT", telemetry.DefaultOTLPEndpoint))
	otelCancel()
	if err != nil {
		return nil, fmt.Errorf("telemetry setup: %w", err)
	}
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}, nil
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide).
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
