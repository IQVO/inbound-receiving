// Command mcp is the composition root for the inbound-receiving MCP server
// (docs/adr/0005-mcp-server-adoption.md): it wires env config to the outbound
// repositories, the repositories to the SAME read use cases the REST adapter
// uses (GetAsn, ListAsns, GetAppointment, ListAppointments, GetReceipt,
// ListReceipts, ListDocks), and those to the inbound MCP adapter, then serves
// MCP over Streamable HTTP (and nothing else: no stdio, no SSE). It is a
// second, independent deployable alongside cmd/api.
//
// The surface is READ-ONLY: no write use case is wired here at all, so this
// binary can never insert into the outbox. It never starts the outbox relay,
// never runs the local-copy consumers and never dials Kafka (no
// EVENT_PUBLISHER, no KAFKA_BROKERS, no consumer groups): those run in
// cmd/api only.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	MCP_ADDR                     listen address (default :8090)
//	DATABASE_URL                 Postgres DSN; unset = in-memory repositories
//	MIGRATIONS_DATABASE_URL      direct DSN for the boot migrations (default DATABASE_URL)
//	DOCK_DOOR_MODE               permissive | kafka (default permissive): ONLY the mode
//	                             list_docks reports, so it matches cmd/api; no consumer runs here
//	OTEL_EXPORTER_OTLP_ENDPOINT  OTLP/gRPC collector (default localhost:4317)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT
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

	inboundmcp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/mcp"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
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
		slog.Error("mcp server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened.
	doorMode, err := usecases.ParseMode(os.Getenv("DOCK_DOOR_MODE"))
	if err != nil {
		return fmt.Errorf("DOCK_DOOR_MODE: %w", err)
	}

	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	otelShutdown, err := telemetry.Setup(otelCtx, mcpServiceName, getenv("SERVICE_VERSION", "dev"),
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

	databaseURL := os.Getenv("DATABASE_URL")
	deps, closeAdapters, err := buildDeps(context.Background(), logger, databaseURL, getenv("MIGRATIONS_DATABASE_URL", databaseURL), doorMode)
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("MCP_ADDR", ":8090"),
		Handler:           newRouter(inboundmcp.Handler(inboundmcp.NewServer(deps))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveMCP(logger, srv)
}

// readRepositories is the set of read ports cmd/mcp wires, whichever adapter
// family backs them.
type readRepositories struct {
	asns         ports.AsnRepository
	appointments ports.AppointmentRepository
	receipts     ports.ReceiptRepository
	doors        ports.DockDoorDirectory
}

// buildDeps selects the repositories the way cmd/api does: no DATABASE_URL
// means the in-memory repositories (runnable locally, empty); a URL means
// migrate then connect a pgx pool. The returned close func releases the pool.
//
// Like every sibling's cmd/mcp this binary runs the idempotent embedded
// migrations on start, so it can boot against a fresh database before cmd/api
// has; golang-migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN where
// DATABASE_URL points at PgBouncer, which cannot honour the session-scoped
// advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string, doorMode usecases.Mode) (inboundmcp.Deps, func(), error) {
	noop := func() {}
	var (
		repos   readRepositories
		closeFn = noop
	)
	if databaseURL == "" {
		logger.Info("DATABASE_URL not configured; using the in-memory repositories")
		repos = readRepositories{
			asns: memory.NewAsnRepo(), appointments: memory.NewAppointmentRepo(),
			receipts: memory.NewReceiptRepo(), doors: memory.NewDockDoors(),
		}
	} else {
		// The first outbound dial of an injected pod can be reset (Istio
		// native sidecars), so migrations and the first ping retry with
		// backoff; on exhaustion the process refuses to boot.
		if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
			return postgres.RunMigrations(migrationsDatabaseURL)
		}); err != nil {
			return inboundmcp.Deps{}, noop, err
		}
		pool, err := postgres.NewPool(ctx, databaseURL)
		if err != nil {
			return inboundmcp.Deps{}, noop, err
		}
		if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
			pool.Close()
			return inboundmcp.Deps{}, noop, err
		}
		logger.Info("postgres repositories configured")
		repos = readRepositories{
			asns: postgres.NewAsnRepo(pool), appointments: postgres.NewAppointmentRepo(pool),
			receipts: postgres.NewReceiptRepo(pool), doors: postgres.NewDockDoors(pool),
		}
		closeFn = pool.Close
	}
	return inboundmcp.Deps{
		GetAsn:           &usecases.GetAsn{Asns: repos.asns},
		ListAsns:         &usecases.ListAsns{Asns: repos.asns},
		GetAppointment:   &usecases.GetAppointment{Appointments: repos.appointments},
		ListAppointments: &usecases.ListAppointments{Appointments: repos.appointments},
		GetReceipt:       &usecases.GetReceipt{Receipts: repos.receipts},
		ListReceipts:     &usecases.ListReceipts{Receipts: repos.receipts},
		ListDocks:        &usecases.ListDocks{Doors: repos.doors, Mode: doorMode},
	}, closeFn, nil
}

// serveMCP runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveMCP(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening (Streamable HTTP)", "addr", srv.Addr)
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
