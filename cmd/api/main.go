// Command api is inbound-receiving's composition root: it wires env config
// into adapters, adapters into use cases, and use cases into the HTTP router,
// the two local-copy consumers (ADR 0003) and the outbox relay.
//
// Environment:
//
//	HTTP_ADDR                     listen address (default :8080)
//	DATABASE_URL                  Postgres DSN; unset = in-memory adapters
//	MIGRATIONS_DATABASE_URL       direct DSN for the boot migrations (default DATABASE_URL)
//	EVENT_PUBLISHER               kafka | log (default log)
//	KAFKA_BROKERS                 comma-separated brokers (EVENT_PUBLISHER=kafka, consumers)
//	OUTBOX_RELAY_INTERVAL         relay poll interval (Go duration or seconds, default 1s)
//	PRODUCT_MODE                  permissive | kafka (default permissive): enforce known SKUs
//	PRODUCT_CONSUMER_GROUP        stable group of the product consumer; required when PRODUCT_MODE=kafka
//	DOCK_DOOR_MODE                permissive | kafka (default permissive): enforce known inbound doors
//	DOCK_DOOR_CONSUMER_GROUP      stable group of the dock-door consumer; required when DOCK_DOOR_MODE=kafka
//	OTEL_EXPORTER_OTLP_ENDPOINT   OTLP/gRPC collector (default localhost:4317)
//	SHUTDOWN_DRAIN_DELAY          wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT, CORS_ALLOWED_ORIGINS
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/telemetry"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/bootretry"
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
	if err := run(); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
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
	ad, closeAdapters, err := buildAdapters(context.Background(), os.Getenv("DATABASE_URL"), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	metrics, err := telemetry.NewMetricsHandler()
	if err != nil {
		return fmt.Errorf("metrics handler: %w", err)
	}
	readiness := &inboundhttp.Readiness{}
	httpServer := &http.Server{
		Addr:              getenv("HTTP_ADDR", ":8080"),
		Handler:           inboundhttp.NewRouter(buildServer(ad, cfg, readiness, metrics)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumers := startConsumers(ad, cfg, logger)
	relay, closeRelay, err := startOutboxRelay(ad.outboxStore, logger)
	if err != nil {
		for _, c := range consumers {
			c.stop()
		}
		return err
	}
	return serveUntilSignal(logger, httpServer, readiness, consumers, relay, closeRelay)
}

// config is the validated, env-derived part of the wiring.
type config struct {
	productMode   usecases.Mode
	productGroup  string
	doorMode      usecases.Mode
	doorGroup     string
	kafkaBrokers  []string
	consumersWant bool
}

// loadConfig validates the mode/group/broker combinations (ADR 0003): a
// `kafka` mode with an unset group, or without brokers, is a boot error.
func loadConfig(env func(string) string) (config, error) {
	var (
		cfg config
		err error
	)
	if cfg.productMode, err = usecases.ParseMode(env("PRODUCT_MODE")); err != nil {
		return config{}, fmt.Errorf("PRODUCT_MODE: %w", err)
	}
	if cfg.doorMode, err = usecases.ParseMode(env("DOCK_DOOR_MODE")); err != nil {
		return config{}, fmt.Errorf("DOCK_DOOR_MODE: %w", err)
	}
	cfg.productGroup, cfg.doorGroup = env("PRODUCT_CONSUMER_GROUP"), env("DOCK_DOOR_CONSUMER_GROUP")
	if cfg.productMode == usecases.ModeKafka && cfg.productGroup == "" {
		return config{}, errors.New("PRODUCT_MODE=kafka requires PRODUCT_CONSUMER_GROUP")
	}
	if cfg.doorMode == usecases.ModeKafka && cfg.doorGroup == "" {
		return config{}, errors.New("DOCK_DOOR_MODE=kafka requires DOCK_DOOR_CONSUMER_GROUP")
	}
	cfg.consumersWant = cfg.productMode == usecases.ModeKafka || cfg.doorMode == usecases.ModeKafka
	if raw := env("KAFKA_BROKERS"); raw != "" {
		cfg.kafkaBrokers = strings.Split(raw, ",")
	}
	if cfg.consumersWant && len(cfg.kafkaBrokers) == 0 {
		return config{}, errors.New("PRODUCT_MODE/DOCK_DOOR_MODE=kafka requires KAFKA_BROKERS")
	}
	return cfg, nil
}

// skuCopy and doorCopy are the local copies read by the HTTP use cases and
// written by the consumers (the same adapter serves both).
type skuCopy interface {
	ports.SkuDirectory
	ports.SkuStore
}

type doorCopy interface {
	ports.DockDoorDirectory
	ports.DockDoorStore
}

// adapters is the set of outbound adapters the composition root wires.
type adapters struct {
	asns         ports.AsnRepository
	appointments ports.AppointmentRepository
	receipts     ports.ReceiptRepository
	outbox       ports.OutboxRepository
	outboxStore  outboxrelay.Store
	processed    ports.ProcessedEvents
	skus         skuCopy
	doors        doorCopy
	idempotency  ports.IdempotencyStore
	uow          ports.UnitOfWork
}

func (a adapters) writer() usecases.Writer {
	return usecases.Writer{
		Asns: a.asns, Appointments: a.appointments, Receipts: a.receipts,
		Outbox: a.outbox, Encoder: outboundkafka.NewFanoutEncoder(), UoW: a.uow,
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
}

func buildServer(ad adapters, cfg config, readiness *inboundhttp.Readiness, metrics http.Handler) *inboundhttp.Server {
	w := ad.writer()
	return &inboundhttp.Server{
		RegisterAsn:        &usecases.RegisterAsn{Writer: w, SkuMode: cfg.productMode, Skus: ad.skus},
		CancelAsn:          &usecases.CancelAsn{Writer: w},
		GetAsn:             &usecases.GetAsn{Asns: ad.asns},
		ListAsns:           &usecases.ListAsns{Asns: ad.asns},
		BookAppointment:    &usecases.BookAppointment{Writer: w, DoorMode: cfg.doorMode, Doors: ad.doors},
		CheckInAppointment: &usecases.CheckInAppointment{Writer: w},
		CancelAppointment:  &usecases.CancelAppointment{Writer: w},
		GetAppointment:     &usecases.GetAppointment{Appointments: ad.appointments},
		ListAppointments:   &usecases.ListAppointments{Appointments: ad.appointments},
		OpenReceipt:        &usecases.OpenReceipt{Writer: w},
		ReceiveLine:        &usecases.ReceiveLine{Writer: w},
		CloseReceipt:       &usecases.CloseReceipt{Writer: w},
		GetReceipt:         &usecases.GetReceipt{Receipts: ad.receipts},
		ListReceipts:       &usecases.ListReceipts{Receipts: ad.receipts},
		ListDocks:          &usecases.ListDocks{Doors: ad.doors, Mode: cfg.doorMode},
		Idempotency:        ad.idempotency,
		Readiness:          readiness,
		Metrics:            metrics,
	}
}

// buildAdapters wires Postgres when databaseURL is set (running the embedded
// migrations first) or in-memory adapters otherwise.
func buildAdapters(ctx context.Context, databaseURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		logger.Info("DATABASE_URL not configured; using in-memory adapters")
		asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
		skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
		ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
		return adapters{
			asns: asns, appointments: appts, receipts: receipts, outbox: ob, outboxStore: ob, processed: processed,
			skus: skus, doors: doors, idempotency: memory.NewIdempotencyStore(),
			uow: memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed),
		}, noop, nil
	}

	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the LAST error is returned and the process refuses to boot.
	migrationsURL := migrationsDatabaseURL(databaseURL)
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsURL)
	}); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")
	ob := postgres.NewOutboxRepo(pool)
	return adapters{
		asns: postgres.NewAsnRepo(pool), appointments: postgres.NewAppointmentRepo(pool), receipts: postgres.NewReceiptRepo(pool),
		outbox: ob, outboxStore: ob, processed: postgres.NewProcessedEventRepo(pool),
		skus: postgres.NewKnownSkus(pool), doors: postgres.NewDockDoors(pool),
		idempotency: postgres.NewIdempotencyStore(pool), uow: postgres.NewUnitOfWork(pool),
	}, pool.Close, nil
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}

// worker is a started background loop, the channel closed once it returned
// and the function that asks it to stop.
type worker struct {
	name string
	done chan struct{}
	stop func()
}

// startWorker runs run in a goroutine with a cancellable context. closeFn
// (reader/sink close) runs after the context is cancelled.
func startWorker(logger *slog.Logger, name string, run func(ctx context.Context) error, closeFn func()) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{name: name, done: make(chan struct{})}
	w.stop = func() {
		cancel()
		closeFn()
	}
	go func() {
		defer close(w.done)
		if err := run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error(name+" stopped", "error", err)
		}
	}()
	return w
}

// startConsumers starts the consumer of every local copy whose mode is
// kafka. A reader dials lazily inside Run, so a broker outage never blocks
// boot. Permissive modes start nothing.
func startConsumers(ad adapters, cfg config, logger *slog.Logger) []*worker {
	var workers []*worker
	if cfg.productMode == usecases.ModeKafka {
		apply := &usecases.ApplyProductRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Skus: ad.skus}
		c := inboundkafka.NewProductConsumer(cfg.kafkaBrokers, cfg.productGroup, apply, logger)
		logger.Info("product consumer running", "topic", inboundkafka.ProductTopic, "group_id", cfg.productGroup, "brokers", cfg.kafkaBrokers)
		workers = append(workers, startWorker(logger, "product-consumer", c.Run, func() { _ = c.Close() }))
	} else {
		logger.Info("PRODUCT_MODE is permissive; product consumer not started")
	}
	if cfg.doorMode == usecases.ModeKafka {
		reg := &usecases.ApplyLocationSlotRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
		dec := &usecases.ApplyLocationSlotDecommissioned{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
		c := inboundkafka.NewDockDoorConsumer(cfg.kafkaBrokers, cfg.doorGroup, reg, dec, logger)
		logger.Info("dock door consumer running", "topic", inboundkafka.FacilityTopic, "group_id", cfg.doorGroup, "brokers", cfg.kafkaBrokers)
		workers = append(workers, startWorker(logger, "dock-door-consumer", c.Run, func() { _ = c.Close() }))
	} else {
		logger.Info("DOCK_DOOR_MODE is permissive; dock door consumer not started")
	}
	return workers
}

// Event publisher modes (EVENT_PUBLISHER).
const (
	publisherLog   = "log"
	publisherKafka = "kafka"
)

// parsePublisherMode validates EVENT_PUBLISHER ("" and "log" = log sink).
func parsePublisherMode(raw string) (string, error) {
	switch raw {
	case "", publisherLog:
		return publisherLog, nil
	case publisherKafka:
		return publisherKafka, nil
	default:
		return "", fmt.Errorf("unknown EVENT_PUBLISHER %q (want kafka or log)", raw)
	}
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// startOutboxRelay starts the relay goroutine with the EVENT_PUBLISHER sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, logger *slog.Logger) (*worker, func(), error) {
	mode, err := parsePublisherMode(os.Getenv("EVENT_PUBLISHER"))
	if err != nil {
		return nil, nil, err
	}
	var (
		sink      outboxrelay.Sink = outboxrelay.LogSink{Logger: logger}
		closeSink                  = func() {}
	)
	if mode == publisherKafka {
		raw := os.Getenv("KAFKA_BROKERS")
		if raw == "" {
			return nil, nil, errors.New("EVENT_PUBLISHER=kafka requires KAFKA_BROKERS")
		}
		kafkaSink := outboundkafka.NewRelaySink(strings.Split(raw, ","))
		sink = kafkaSink
		closeSink = func() {
			if err := kafkaSink.Close(); err != nil {
				logger.Error("kafka relay sink close failed", "error", err)
			}
		}
	}
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	relay := outboxrelay.NewRelay(store, sink, logger, outboxrelay.WithInterval(interval))

	logger.Info("outbox relay running", "publisher", mode, "interval", interval, "topics", []string{outboundkafka.Topic, outboundkafka.AnalyticsTopic})
	w := startWorker(logger, "outbox-relay", relay.Run, func() {})
	go func() {
		<-w.done
		closeSink()
	}()
	return w, w.stop, nil
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers (outbox
// writers), (5) stop and await the relay LAST so nothing committed above is
// stranded; the caller's deferred pool.Close runs after all of it.
func serveUntilSignal(logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	consumers []*worker, relay *worker, closeRelay func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		for _, c := range consumers {
			c.stop()
		}
		closeRelay()
		return err
	case <-ctx.Done():
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

// await waits (bounded) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
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

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|error.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
