// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary
// (errors, filters, messages) lives in internal/application/repository and
// internal/application/outbox.
package ports

import (
	"context"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// AsnRepository persists Asn aggregates by ASN number. Inside a UnitOfWork it
// joins the transaction carried in ctx.
type AsnRepository interface {
	// Get returns the ASN, or repository.ErrAsnNotFound.
	Get(ctx context.Context, number asn.Number) (*asn.Asn, error)
	// Save persists a guarded by the version the caller loaded:
	// loadedVersion 0 inserts (an existing number is
	// repository.ErrConcurrentModification); any other value updates only
	// when the stored version still equals loadedVersion (else
	// repository.ErrConcurrentModification).
	Save(ctx context.Context, a *asn.Asn, loadedVersion int64) error
	// List returns up to limit ASNs with number > after (byte order),
	// ascending by number, narrowed by filter.
	List(ctx context.Context, filter repository.AsnFilter, after asn.Number, limit int) ([]*asn.Asn, error)
}

// AppointmentRepository persists DockAppointment aggregates by id.
type AppointmentRepository interface {
	// Get returns the appointment, or repository.ErrAppointmentNotFound.
	Get(ctx context.Context, id appointment.ID) (*appointment.DockAppointment, error)
	// Save persists d with the same version guard as AsnRepository.Save.
	Save(ctx context.Context, d *appointment.DockAppointment, loadedVersion int64) error
	// List returns up to limit appointments after the cursor, ascending by
	// (window start, id), narrowed by filter.
	List(ctx context.Context, filter repository.AppointmentFilter, after repository.AppointmentCursor, limit int) ([]*appointment.DockAppointment, error)
	// ActiveOnDoor returns the Booked and CheckedIn appointments of a door.
	ActiveOnDoor(ctx context.Context, door appointment.DoorCode) ([]*appointment.DockAppointment, error)
	// LockDoor serialises concurrent bookings of one door until the
	// surrounding unit of work ends, so Schedule.CheckNoOverlap and the
	// following Save are atomic per door.
	LockDoor(ctx context.Context, door appointment.DoorCode) error
}

// ReceiptRepository persists Receipt aggregates by id.
type ReceiptRepository interface {
	// Get returns the receipt, or repository.ErrReceiptNotFound.
	Get(ctx context.Context, id receipt.ID) (*receipt.Receipt, error)
	// Save persists r with the same version guard as AsnRepository.Save;
	// an insert for an ASN that already has an open receipt is
	// repository.ErrReceiptAlreadyOpen.
	Save(ctx context.Context, r *receipt.Receipt, loadedVersion int64) error
	// List returns up to limit receipts with id > after, ascending by id,
	// narrowed by filter.
	List(ctx context.Context, filter repository.ReceiptFilter, after receipt.ID, limit int) ([]*receipt.Receipt, error)
	// OpenByAsn returns the ASN's open receipt, or
	// repository.ErrReceiptNotFound when it has none.
	OpenByAsn(ctx context.Context, number asn.Number) (*receipt.Receipt, error)
}
