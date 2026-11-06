package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/bootretry"
)

// republishCommand is the one-shot stage B backfill of the DockAppointment
// ownership transfer to dock-scheduling (ADR 0019, plan YD-0002):
//
//	kubectl exec deploy/inbound-receiving -- ./api republish-appointments
//
// It enqueues the legacy DockAppointment events of every stored appointment
// through the transactional outbox; the running pod's relay publishes them and
// dock-scheduling's legacy importer rebuilds its appointments from them.
// Removed at stage E.
const republishCommand = "republish-appointments"

// runCommand dispatches a subcommand of the main binary and returns the
// process exit code: 0 on success, 1 on failure, 2 on an unknown command.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case republishCommand:
		// Logs go to stderr so stdout carries only the result line.
		logger := slog.New(slog.NewJSONHandler(stderr, nil))
		if err := runRepublish(ctx, os.Getenv("DATABASE_URL"), stdout, logger); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", republishCommand, err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q (supported: %s)\n", args[0], republishCommand)
		return 2
	}
}

// runRepublish opens a pool on databaseURL (no migrations: the running pod
// owns the schema), runs the backfill and prints the appointment count. It
// needs only DATABASE_URL: no broker, no HTTP server, no consumer.
func runRepublish(ctx context.Context, databaseURL string, out io.Writer, logger *slog.Logger) error {
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		return err
	}
	n, err := newRepublishUseCase(pool).Execute(ctx)
	if err != nil {
		return fmt.Errorf("after %d appointments: %w", n, err)
	}
	_, err = fmt.Fprintf(out, "republished %d dock appointments\n", n)
	return err
}

// newRepublishUseCase wires the backfill: the Postgres appointments and
// outbox, and the plain integration-topic Encoder (the legacy
// DockAppointment mapping kept for this command only). The analytics topic is
// NOT written: the backfill re-announces history to dock-scheduling, it is no
// new occurrence for this context's own analytics.
func newRepublishUseCase(pool *pgxpool.Pool) *usecases.RepublishAppointments {
	return &usecases.RepublishAppointments{
		Records: postgres.NewAppointmentRepo(pool),
		Encoder: outboundkafka.NewEncoder(),
		Outbox:  postgres.NewOutboxRepo(pool),
		UoW:     postgres.NewUnitOfWork(pool),
	}
}
