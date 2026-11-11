package ports

import (
	"context"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// StowStore is the two local copies of the dock-to-stock KPI and the marker
// that makes its event exactly-once (ADR 0021): visit_arrivals (yard-
// management's VisitCheckedIn) and carton_stows (inventory-storage's
// ContainerStowed). Only the two consumers and the evaluation behind
// CloseReceipt use it, and applying a fact raises no event by itself.
type StowStore interface {
	// ApplyArrival stores the gate arrival of the appointment, keeping the
	// EARLIEST arrived_at seen. It reports false (and changes nothing) when
	// the stored arrival is already at or before arrivedAt.
	ApplyArrival(ctx context.Context, appointmentID appointment.ID, visitID string, arrivedAt time.Time) (applied bool, err error)
	// ApplyCartonStow stores the stow of the container with the license
	// plate, keeping the EARLIEST stowedAt seen (a later move to another bin
	// is not a new stow). It reports false when the stored stow is already at
	// or before stowedAt.
	ApplyCartonStow(ctx context.Context, lpn string, stowedAt time.Time) (applied bool, err error)
	// ReceiptsOfAppointment returns the receipts opened from the appointment,
	// in id order, and LOCKS them (SELECT ... FOR UPDATE) until the unit of
	// work ends, so two evaluations of one receipt serialise and the later
	// one sees the earlier one's committed fact.
	ReceiptsOfAppointment(ctx context.Context, id appointment.ID) ([]*receipt.Receipt, error)
	// ReceiptsOfCarton returns the receipts whose carton ledger holds the
	// license plate, in id order, locked like ReceiptsOfAppointment.
	ReceiptsOfCarton(ctx context.Context, lpn string) ([]*receipt.Receipt, error)
	// Evidence reads the arrival of the receipt's appointment and the stow
	// of each of its Good cartons.
	Evidence(ctx context.Context, r *receipt.Receipt) (repository.StowEvidence, error)
	// Arrival reads the stored gate arrival of the appointment from the
	// visit_arrivals copy: the visit id and the EARLIEST arrived_at seen. It
	// reports false when no arrival was seen for the appointment yet (ADR
	// 0025; the ASN-timeliness fact then stays unraised).
	Arrival(ctx context.Context, appointmentID appointment.ID) (repository.Arrival, bool, error)
	// Complete records that the receipt's ReceiptStowCompleted was raised. It
	// reports true only for the call that inserted the marker, so the event
	// is raised exactly once.
	Complete(ctx context.Context, id receipt.ID, stowedAt time.Time) (first bool, err error)
}
