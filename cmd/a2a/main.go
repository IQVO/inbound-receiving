// Command a2a is the composition root for the inbound-receiving A2A agent
// (docs/adr/0016-a2a-inbound-adapter.md): it wires env config to the same
// outbound repositories cmd/mcp uses, the repositories to the SAME read use
// cases the REST and MCP adapters use (GetAsn, ListReceipts, ListAppointments),
// and those to the inbound A2A adapter, then serves Agent2Agent JSON-RPC over
// HTTP with the Agent Card at the well-known path. It is a third, independent
// deployable alongside cmd/api and cmd/mcp.
//
// The surface is READ-ONLY, exactly like cmd/mcp: no write use case is wired
// here at all, so this binary can never insert into the outbox. It never
// starts the outbox relay, never runs the local-copy consumers and never dials
// Kafka (no EVENT_PUBLISHER, no KAFKA_BROKERS, no consumer groups): those run
// in cmd/api only. It calls no other bounded context and contains no outbound
// A2A client (CLAUDE.md rule 5).
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// A missing or malformed mandatory variable is a boot error naming it, raised
// before telemetry, any connection and the listener.
//
// Environment:
//
//	DATABASE_URL                 REQUIRED. Postgres DSN (there is no in-memory mode, ADR 0011)
//	A2A_AGENT_URL                REQUIRED. Absolute http(s) URL of this agent's JSON-RPC
//	                             endpoint, advertised on the Agent Card
//	A2A_ADDR                     listen address (default :8093)
//	MIGRATIONS_DATABASE_URL      direct DSN for the boot migrations (default DATABASE_URL)
//	OTEL_EXPORTER_OTLP_ENDPOINT  OTLP/gRPC collector (default localhost:4317)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inbounda2a "github.com/claudioed/inbound-receiving/internal/adapters/inbound/a2a"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/telemetry"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("a2a agent exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened.
	cfg, err := loadBootConfig(os.Getenv)
	if err != nil {
		return err
	}

	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	otelShutdown, err := telemetry.Setup(otelCtx, a2aServiceName, getenv("SERVICE_VERSION", "dev"),
		getenv("OTEL_EXPORTER_OTLP_ENDPOINT", telemetry.DefaultOTLPEndpoint))
	otelCancel()
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()

	deps, closeAdapters, err := buildDeps(context.Background(), logger, cfg.databaseURL, getenv("MIGRATIONS_DATABASE_URL", cfg.databaseURL))
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("A2A_ADDR", ":8093"),
		Handler:           newAgent(deps, cfg.agentURL, getenv("SERVICE_VERSION", "dev")),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveA2A(logger, srv)
}

// errMissingDatabaseURL is the boot error of an unset DATABASE_URL: the A2A
// binary reads the same Postgres as cmd/api and has no in-memory mode.
var errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")

// errMissingAgentURL is the boot error for a missing A2A_AGENT_URL: the Agent
// Card must advertise the absolute URL clients dial (docs/adr/0016).
var errMissingAgentURL = errors.New("A2A_AGENT_URL is required: the Agent Card must advertise the absolute URL of this agent's JSON-RPC endpoint (docs/adr/0016), e.g. http://<release>-a2a.<ns>.svc.cluster.local:8093/")

// bootConfig is the mandatory configuration of cmd/a2a: real Postgres and the
// URL the Agent Card advertises, with no default and no degraded mode behind
// either.
type bootConfig struct {
	databaseURL string
	agentURL    string
}

// checkAgentURL validates A2A_AGENT_URL: an absolute http(s) URL. The card is
// the discovery contract; a relative or schemeless URL is a misconfiguration
// every client would hit, so it is refused at boot, never silently served.
func checkAgentURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errMissingAgentURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("A2A_AGENT_URL=%q is not an absolute http(s) URL: the Agent Card must advertise the endpoint clients dial (docs/adr/0016)", raw)
	}
	return nil
}

// loadBootConfig validates the environment before anything is opened. Every
// problem is reported at once, each naming its variable, so the process fails
// fast at boot, before telemetry, any connection and the listener.
func loadBootConfig(env func(string) string) (bootConfig, error) {
	cfg := bootConfig{databaseURL: env("DATABASE_URL"), agentURL: strings.TrimSpace(env("A2A_AGENT_URL"))}
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, errMissingDatabaseURL)
	}
	if err := checkAgentURL(cfg.agentURL); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return bootConfig{}, errors.Join(errs...)
	}
	return cfg, nil
}

// buildDeps migrates then connects a pgx pool and wires the three read use
// cases over the Postgres repositories. An empty databaseURL is a boot error. The
// returned close func releases the pool.
//
// Like cmd/mcp this binary runs the idempotent embedded
// migrations on start, so it can boot against a fresh database before cmd/api
// has; golang-migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN where
// DATABASE_URL points at PgBouncer, which cannot honour the session-scoped
// advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string) (inbounda2a.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inbounda2a.Deps{}, noop, errMissingDatabaseURL
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inbounda2a.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	return depsOver(postgres.NewAsnRepo(pool), postgres.NewAppointmentRepo(pool), postgres.NewReceiptRepo(pool)), pool.Close, nil
}

// depsOver wires the three read-only use cases over the given read ports. No
// write use case, outbox, encoder or unit of work is constructed here.
func depsOver(asns ports.AsnRepository, appts ports.AppointmentRepository, receipts ports.ReceiptRepository) inbounda2a.Deps {
	return inbounda2a.Deps{
		GetAsn:           &usecases.GetAsn{Asns: asns},
		ListReceipts:     &usecases.ListReceipts{Receipts: receipts},
		ListAppointments: &usecases.ListAppointments{Appointments: appts},
	}
}

// serveA2A runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveA2A(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("a2a agent listening (JSON-RPC over HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|error (case-insensitive), defaulting to Info.
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
