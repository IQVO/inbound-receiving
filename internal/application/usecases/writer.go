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
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// Writer bundles the ports every write use case needs.
type Writer struct {
	Asns         ports.AsnRepository
	Appointments ports.AppointmentRepository
	Receipts     ports.ReceiptRepository
	Outbox       ports.OutboxRepository
	Encoder      ports.EventEncoder
	UoW          ports.UnitOfWork
	Clock        ports.Clock
	IDs          ports.IDGenerator
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

// saveAppointment saves d guarded by loadedVersion and enqueues its events.
func (w Writer) saveAppointment(ctx context.Context, d *appointment.DockAppointment, loadedVersion int64, events []appointment.Event) error {
	if err := w.Appointments.Save(ctx, d, loadedVersion); err != nil {
		return err
	}
	msgs, err := w.Encoder.EncodeAppointment(events...)
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
