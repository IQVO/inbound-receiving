package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/bootretry"
)

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

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}
