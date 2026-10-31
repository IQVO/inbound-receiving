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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/telemetry"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/bootretry"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
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

// config is the validated, env-derived part of the wiring.
type config struct {
	productGroup   string
	doorGroup      string
	packagingGroup string
	kafkaBrokers   []string
}

// Boot errors for the variables the context cannot run without. Each message
// names the variable (ADR 0011).
var (
	errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	errMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// loadConfig validates the environment before anything is opened or bound:
// DATABASE_URL, KAFKA_BROKERS and both consumer groups are mandatory, the
// consumer modes may only be `kafka` (the default), and EVENT_PUBLISHER may
// only be `kafka`. Every error names the variable (ADR 0011).
func loadConfig(env func(string) string) (config, error) {
	var cfg config
	if env("DATABASE_URL") == "" {
		return config{}, errMissingDatabaseURL
	}
	cfg.kafkaBrokers = splitBrokers(env("KAFKA_BROKERS"))
	if len(cfg.kafkaBrokers) == 0 {
		return config{}, errMissingBrokers
	}
	if err := parseConsumerMode("PRODUCT_MODE", env("PRODUCT_MODE")); err != nil {
		return config{}, err
	}
	if err := parseConsumerMode("DOCK_DOOR_MODE", env("DOCK_DOOR_MODE")); err != nil {
		return config{}, err
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return config{}, err
	}
	if cfg.productGroup = env("PRODUCT_CONSUMER_GROUP"); cfg.productGroup == "" {
		return config{}, errors.New("PRODUCT_CONSUMER_GROUP is required: the product consumer always runs on real Kafka")
	}
	if cfg.doorGroup = env("DOCK_DOOR_CONSUMER_GROUP"); cfg.doorGroup == "" {
		return config{}, errors.New("DOCK_DOOR_CONSUMER_GROUP is required: the dock-door consumer always runs on real Kafka")
	}
	if cfg.packagingGroup = env("PACKAGING_CONSUMER_GROUP"); cfg.packagingGroup == "" {
		return config{}, errors.New("PACKAGING_CONSUMER_GROUP is required: the packaging consumer (PackagingView) always runs on real Kafka")
	}
	return cfg, nil
}

// splitBrokers splits KAFKA_BROKERS on commas, trimming blanks.
func splitBrokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// parseConsumerMode accepts the only runtime mode of a local-copy consumer:
// "" (the default) or "kafka". The names stay (docs and infra set them) but
// `permissive` and `off` are refused: the consumers always run.
func parseConsumerMode(name, raw string) error {
	switch raw {
	case "", string(usecases.ModeKafka):
		return nil
	case string(usecases.ModePermissive), "off":
		return fmt.Errorf("%s=%s is not supported: this context runs on real Kafka", name, raw)
	default:
		return fmt.Errorf("%s: unknown mode %q (want kafka)", name, raw)
	}
}

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping
// and a Kafka broker answering a topic-less metadata request (never topics:
// the broker auto-creates them on first write), cached 3s, bounded 2s. The
// ping is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
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

// packagingView is the PackagingView read by the receiving use cases and
// written by the packaging consumer (the same adapter serves both).
type packagingView interface {
	ports.PackagingDirectory
	ports.PackagingStore
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
	packaging    packagingView
	idempotency  ports.IdempotencyStore
	uow          ports.UnitOfWork
	// sweeper is the housekeeping job (ADR 0010).
	sweeper *postgres.Sweeper
	// pool is the Postgres pool (the readiness check pings it).
	pool *pgxpool.Pool
}

func (a adapters) writer() usecases.Writer {
	return usecases.Writer{
		Asns: a.asns, Appointments: a.appointments, Receipts: a.receipts,
		Outbox: a.outbox, Encoder: outboundkafka.NewFanoutEncoder(), UoW: a.uow,
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
}

func buildServer(ad adapters, readiness *inboundhttp.Readiness, metrics http.Handler, receiptMetrics ports.ReceiptMetrics) *inboundhttp.Server {
	w := ad.writer()
	return &inboundhttp.Server{
		RegisterAsn:        &usecases.RegisterAsn{Writer: w, SkuMode: usecases.ModeKafka, Skus: ad.skus},
		CancelAsn:          &usecases.CancelAsn{Writer: w},
		GetAsn:             &usecases.GetAsn{Asns: ad.asns},
		GetAsnCartons:      &usecases.GetAsnCartons{Asns: ad.asns, Receipts: ad.receipts},
		ListAsns:           &usecases.ListAsns{Asns: ad.asns},
		BookAppointment:    &usecases.BookAppointment{Writer: w, DoorMode: usecases.ModeKafka, Doors: ad.doors},
		CheckInAppointment: &usecases.CheckInAppointment{Writer: w},
		CancelAppointment:  &usecases.CancelAppointment{Writer: w},
		GetAppointment:     &usecases.GetAppointment{Appointments: ad.appointments},
		ListAppointments:   &usecases.ListAppointments{Appointments: ad.appointments},
		OpenReceipt:        &usecases.OpenReceipt{Writer: w},
		ReceiveLine:        (&usecases.ReceiveLine{Writer: w}).WithMetrics(receiptMetrics),
		ReceiveScan:        (&usecases.ReceiveScan{Writer: w, Packaging: ad.packaging, Limits: cartonLimits(os.Getenv, slog.Default())}).WithMetrics(receiptMetrics),
		GetCartonLabel:     &usecases.GetCartonLabel{Receipts: ad.receipts},
		CloseReceipt:       &usecases.CloseReceipt{Writer: w},
		GetReceipt:         &usecases.GetReceipt{Receipts: ad.receipts},
		ListReceipts:       &usecases.ListReceipts{Receipts: ad.receipts},
		ListDocks:          &usecases.ListDocks{Doors: ad.doors, Mode: usecases.ModeKafka},
		Idempotency:        ad.idempotency,
		Readiness:          readiness,
		Metrics:            metrics,
	}
}

// buildAdapters wires the Postgres adapters, running the embedded migrations
// first. An empty databaseURL is a boot error: there is no in-memory runtime.
func buildAdapters(ctx context.Context, databaseURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return adapters{}, noop, errMissingDatabaseURL
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
		skus: postgres.NewKnownSkus(pool), doors: postgres.NewDockDoors(pool), packaging: postgres.NewPackagingView(pool),
		idempotency: postgres.NewIdempotencyStore(pool), uow: postgres.NewUnitOfWork(pool),
		sweeper: newSweeper(pool, os.Getenv, logger), pool: pool,
	}, pool.Close, nil
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

// startConsumers starts the three local-copy consumers; they are always on. A
// reader dials lazily inside Run, so a broker outage never blocks boot.
func startConsumers(ad adapters, cfg config, logger *slog.Logger) []*worker {
	apply := &usecases.ApplyProductRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Skus: ad.skus}
	pc := inboundkafka.NewProductConsumer(cfg.kafkaBrokers, cfg.productGroup, apply, logger)
	logger.Info("product consumer running", "topic", inboundkafka.ProductTopic, "group_id", cfg.productGroup, "brokers", cfg.kafkaBrokers)

	reg := &usecases.ApplyLocationSlotRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
	dec := &usecases.ApplyLocationSlotDecommissioned{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
	dc := inboundkafka.NewDockDoorConsumer(cfg.kafkaBrokers, cfg.doorGroup, reg, dec, logger)
	logger.Info("dock door consumer running", "topic", inboundkafka.FacilityTopic, "group_id", cfg.doorGroup, "brokers", cfg.kafkaBrokers)

	declared := &usecases.ApplyPackLevelDeclared{UoW: ad.uow, ProcessedEvents: ad.processed, Packaging: ad.packaging}
	retired := &usecases.ApplyPackLevelRetired{UoW: ad.uow, ProcessedEvents: ad.processed, Packaging: ad.packaging}
	pk := inboundkafka.NewPackagingConsumer(cfg.kafkaBrokers, cfg.packagingGroup, declared, retired, logger)
	logger.Info("packaging consumer running", "topic", inboundkafka.ProductTopic, "group_id", cfg.packagingGroup, "brokers", cfg.kafkaBrokers)

	return []*worker{
		startWorker(logger, "product-consumer", pc.Run, func() { _ = pc.Close() }),
		startWorker(logger, "dock-door-consumer", dc.Run, func() { _ = dc.Close() }),
		startWorker(logger, "packaging-consumer", pk.Run, func() { _ = pk.Close() }),
	}
}

// parsePublisherMode validates EVENT_PUBLISHER: unset or "kafka". The log
// sink is gone (ADR 0011); the variable stays because the chart and
// warehouse-infra set it.
func parsePublisherMode(raw string) error {
	switch raw {
	case "", "kafka":
		return nil
	case "log":
		return errors.New("EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka")
	default:
		return fmt.Errorf("EVENT_PUBLISHER: unknown value %q (want kafka)", raw)
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

// Env names of the CartonOverLimit limits (contracts R9, ADR 0014).
const (
	envCartonMaxGrossWeightG  = "CARTON_MAX_GROSS_WEIGHT_G"
	envCartonMaxLongestSideMm = "CARTON_MAX_LONGEST_SIDE_MM"
)

// cartonLimits reads the two carton handling limits. Unset or 0 = unchecked;
// a negative or unparsable value logs a warning and is unchecked, so a typo
// never rejects or flags goods by accident.
func cartonLimits(env func(string) string, logger *slog.Logger) receipt.CartonLimits {
	return receipt.CartonLimits{
		MaxGrossWeightG:  limitFromEnv(env, envCartonMaxGrossWeightG, logger),
		MaxLongestSideMm: limitFromEnv(env, envCartonMaxLongestSideMm, logger),
	}
}

func limitFromEnv(env func(string) string, name string, logger *slog.Logger) int64 {
	raw := env(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		logger.Warn("invalid "+name+"; the limit is unchecked", "value", raw)
		return 0
	}
	return n
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS (gap fix D12): how many times
// an outbox row may fail before the relay excludes it from claims. Unset =
// the fleet default (20); 0 = unlimited; negative or unparsable values log
// a warning and use the default, so a typo never silently removes the cap.
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

// startOutboxRelay starts the always-on relay goroutine over the Kafka sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, func(), error) {
	if len(brokers) == 0 {
		return nil, nil, errMissingBrokers
	}
	sink := outboundkafka.NewRelaySink(brokers)
	closeSink := func() {
		if err := sink.Close(); err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	}
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewRelay(store, sink, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	logger.Info("outbox relay running", "brokers", brokers, "interval", interval, "max_attempts", maxAttempts,
		"topics", []string{outboundkafka.Topic, outboundkafka.AnalyticsTopic})
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

// watchWorkers returns a channel that receives the name of every worker that
// returns. Workers only return when stopped or on an unrecoverable failure, so
// a value read while the service is serving means a worker died.
func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
