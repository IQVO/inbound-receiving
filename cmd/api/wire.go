package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
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

// appointmentCopy is the appointments local copy of dock-scheduling's
// appointments, read by the receiving use cases and the deprecated GETs and
// written by the dock-appointment consumer (the same adapter serves both).
type appointmentCopy interface {
	ports.AppointmentRepository
	ports.AppointmentStore
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
	appointments appointmentCopy
	receipts     ports.ReceiptRepository
	outbox       ports.OutboxRepository
	outboxStore  outboxrelay.Store
	processed    ports.ProcessedEvents
	stows        ports.StowStore
	advice       ports.AdviceStore
	// adviceSLA is RECEIVING_ADVICE_SLA (ADR 0023); zero = the default.
	adviceSLA time.Duration
	// adviceMetrics counts the advices issued and the overdue ones; nil = none.
	adviceMetrics ports.AdviceMetrics
	skus          skuCopy
	doors         doorCopy
	packaging     packagingView
	idempotency   ports.IdempotencyStore
	uow           ports.UnitOfWork
	// ssccSerials allocates the serials of receiver-assigned SSCCs; siteGLNs and
	// companyPrefix are RECEIVING_SITE_GLNS and RECEIVER_GS1_COMPANY_PREFIX (ADR 0024).
	ssccSerials   ssccSerialStore
	siteGLNs      receipt.SiteGLNs
	companyPrefix receipt.CompanyPrefix
	// sweeper is the housekeeping job (ADR 0010).
	sweeper *postgres.Sweeper
	// pool is the Postgres pool (the readiness check pings it).
	pool *pgxpool.Pool
}

// ssccSerialStore allocates SSCC serials and can create the sequence of a prefix
// ahead of the first request.
type ssccSerialStore interface {
	ports.SsccSerials
	EnsureSequence(ctx context.Context, prefix receipt.CompanyPrefix) error
}

// withLabelSettings applies the validated label settings of the environment.
func (a adapters) withLabelSettings(cfg config) adapters {
	a.siteGLNs, a.companyPrefix = cfg.siteGLNs, cfg.companyPrefix
	return a
}

func (a adapters) writer() usecases.Writer {
	sla := a.adviceSLA
	if sla <= 0 {
		sla = usecases.DefaultAdviceSLA
	}
	return usecases.Writer{
		Asns: a.asns, Appointments: a.appointments, Receipts: a.receipts, Stows: a.stows,
		Advice: a.advice, AdviceSLA: sla,
		Outbox: a.outbox, Encoder: outboundkafka.NewFanoutEncoder(), UoW: a.uow,
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
}

// adviceOverdueSweep is the overdue half of the housekeeping tick (ADR 0023): it
// raises ReceivingAdviceOverdue once for each Open receipt past the advice SLA.
func (a adapters) adviceOverdueSweep(logger *slog.Logger) func(context.Context) (int, error) {
	uc := (&usecases.SweepAdviceOverdue{Writer: a.writer(), Logger: logger}).WithMetrics(a.adviceMetrics)
	return uc.Handle
}

func buildServer(ad adapters, readiness *inboundhttp.Readiness, metrics http.Handler, receiptMetrics ports.ReceiptMetrics) *inboundhttp.Server {
	w := ad.writer()
	return &inboundhttp.Server{
		RegisterAsn:      &usecases.RegisterAsn{Writer: w, SkuMode: usecases.ModeKafka, Skus: ad.skus},
		CancelAsn:        &usecases.CancelAsn{Writer: w},
		GetAsn:           &usecases.GetAsn{Asns: ad.asns},
		GetAsnCartons:    &usecases.GetAsnCartons{Asns: ad.asns, Receipts: ad.receipts},
		ListAsns:         &usecases.ListAsns{Asns: ad.asns},
		GetAppointment:   &usecases.GetAppointment{Appointments: ad.appointments},
		ListAppointments: &usecases.ListAppointments{Appointments: ad.appointments},
		OpenReceipt:      &usecases.OpenReceipt{Writer: w},
		ReceiveLine:      (&usecases.ReceiveLine{Writer: w}).WithMetrics(receiptMetrics),
		ReceiveScan:      (&usecases.ReceiveScan{Writer: w, Packaging: ad.packaging, Limits: cartonLimits(os.Getenv, slog.Default()), SiteGLNs: ad.siteGLNs}).WithMetrics(receiptMetrics),
		AssignSscc:       &usecases.AssignSscc{Writer: w, Packaging: ad.packaging, Prefix: ad.companyPrefix, Serials: ad.ssccSerials},
		GetCartonLabel:   &usecases.GetCartonLabel{Receipts: ad.receipts},
		CloseReceipt:     (&usecases.CloseReceipt{Writer: w}).WithMetrics(ad.adviceMetrics),
		GetReceipt:       &usecases.GetReceipt{Receipts: ad.receipts},
		ListReceipts:     &usecases.ListReceipts{Receipts: ad.receipts},
		ListDocks:        &usecases.ListDocks{Doors: ad.doors, Mode: usecases.ModeKafka},
		Idempotency:      ad.idempotency,
		Readiness:        readiness,
		Metrics:          metrics,
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
	ad := adapters{
		asns: postgres.NewAsnRepo(pool), appointments: postgres.NewAppointmentRepo(pool), receipts: postgres.NewReceiptRepo(pool),
		outbox: ob, outboxStore: ob, processed: postgres.NewProcessedEventRepo(pool), stows: postgres.NewStowRepo(pool),
		advice: postgres.NewAdviceRepo(pool), adviceSLA: adviceSLA(os.Getenv, logger),
		skus: postgres.NewKnownSkus(pool), doors: postgres.NewDockDoors(pool), packaging: postgres.NewPackagingView(pool),
		idempotency: postgres.NewIdempotencyStore(pool), uow: postgres.NewUnitOfWork(pool), pool: pool,
		ssccSerials: postgres.NewSsccSerials(pool),
	}
	// Run un-instrumented rather than not at all if the instruments cannot be registered.
	if m, err := telemetry.NewAdviceMetrics(); err != nil {
		logger.Warn("advice metrics are not registered; the advice KPI counters are off", "error", err)
	} else {
		ad.adviceMetrics = m
	}
	ad.sweeper = newSweeper(pool, os.Getenv, logger, postgres.WithAdviceOverdue(ad.adviceOverdueSweep(logger)))
	logger.Info("receiving advice sla", "sla", ad.adviceSLA.String())
	return ad, pool.Close, nil
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}
