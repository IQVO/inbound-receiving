// Package usecases holds the application's use cases. Every write follows
// the same shape inside ONE ports.UnitOfWork: load the aggregate(s), apply
// the aggregate command, save each guarded by the version that was loaded,
// and enqueue the raised events in the transactional outbox.
package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// Writer bundles the ports every write use case needs. Appointments is the
// read side of the local copy (OpenReceipt validates against it); this
// context never writes an appointment from a use case (ADR 0019). Stows is
// the dock-to-stock store (ADR 0021): when it is nil no stow completion is
// evaluated, which only a test may do; cmd/api always sets it.
type Writer struct {
	Asns         ports.AsnRepository
	Appointments ports.AppointmentRepository
	Receipts     ports.ReceiptRepository
	Stows        ports.StowStore
	// Advice holds the receiving advice markers (ADR 0023): when it is nil no
	// advice is issued and no overdue is raised, which only a test may do;
	// cmd/api always sets it. AdviceSLA is the committed window (a zero or
	// negative value is DefaultAdviceSLA).
	Advice    ports.AdviceStore
	AdviceSLA time.Duration
	Outbox    ports.OutboxRepository
	Encoder   ports.EventEncoder
	UoW       ports.UnitOfWork
	Clock     ports.Clock
	IDs       ports.IDGenerator
}

// now returns the clock's current time in UTC.
func (w Writer) now() time.Time { return w.Clock.Now().UTC() }

// enqueue inserts already-encoded messages in the outbox.
func (w Writer) enqueue(ctx context.Context, msgs []outbox.Message, err error) error {
	if err != nil {
		return fmt.Errorf("encode events: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}
	if err := w.Outbox.Insert(ctx, msgs...); err != nil {
		return fmt.Errorf("enqueue events: %w", err)
	}
	return nil
}

// saveAsn saves a guarded by loadedVersion and enqueues its events.
func (w Writer) saveAsn(ctx context.Context, a *asn.Asn, loadedVersion int64, events []asn.Event) error {
	if err := w.Asns.Save(ctx, a, loadedVersion); err != nil {
		return err
	}
	msgs, err := w.Encoder.EncodeAsn(events...)
	return w.enqueue(ctx, msgs, err)
}

// saveReceipt saves r guarded by loadedVersion and enqueues its events.
func (w Writer) saveReceipt(ctx context.Context, r *receipt.Receipt, loadedVersion int64, events []receipt.Event) error {
	if err := w.Receipts.Save(ctx, r, loadedVersion); err != nil {
		return err
	}
	msgs, err := w.Encoder.EncodeReceipt(events...)
	return w.enqueue(ctx, msgs, err)
}

// checkVersion enforces an optional If-Match: expected 0 means "not sent".
func checkVersion(expected, actual int64) error {
	if expected != 0 && expected != actual {
		return ErrVersionMismatch
	}
	return nil
}
