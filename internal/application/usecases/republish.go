package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// RepublishBatchSize is how many appointments one republish transaction
// enqueues.
const RepublishBatchSize = 500

// RepublishAppointments is the stage B backfill of the DockAppointment
// ownership transfer (ADR 0019): for every stored appointment it enqueues,
// through the transactional outbox, the legacy DockAppointment events that
// lead to its current state (Booked, then CheckedIn / Cancelled / Completed),
// so dock-scheduling's legacy importer can rebuild it. The running service's
// outbox relay publishes the rows.
//
// It is idempotent in effect: every run enqueues the same history again, and
// the importer imports an appointment once by id and moves only appointments
// it imported itself. Each batch commits in its own unit of work, so a crash
// leaves the committed batches enqueued and a re-run simply enqueues all
// again. Removed at stage E.
type RepublishAppointments struct {
	Records ports.AppointmentHistory
	Encoder ports.LegacyAppointmentEncoder
	Outbox  ports.OutboxRepository
	UoW     ports.UnitOfWork
}

// Execute enqueues every appointment in id order, in batches, and returns how
// many appointments it republished.
func (uc *RepublishAppointments) Execute(ctx context.Context) (int, error) {
	total := 0
	var after appointment.ID
	for {
		n := 0
		err := uc.UoW.Do(ctx, func(ctx context.Context) error {
			records, err := uc.Records.ListRecords(ctx, after, RepublishBatchSize)
			if err != nil {
				return fmt.Errorf("list appointments after %q: %w", after, err)
			}
			for _, rec := range records {
				msgs, err := uc.Encoder.EncodeAppointment(rec.Appointment.Replay(rec.Timeline)...)
				if err != nil {
					return fmt.Errorf("encode appointment %s: %w", rec.Appointment.ID(), err)
				}
				if err := uc.Outbox.Insert(ctx, msgs...); err != nil {
					return fmt.Errorf("enqueue appointment %s: %w", rec.Appointment.ID(), err)
				}
				after = rec.Appointment.ID()
			}
			n = len(records)
			return nil
		})
		total += n
		if err != nil {
			return total, err
		}
		if n < RepublishBatchSize {
			return total, nil
		}
	}
}
