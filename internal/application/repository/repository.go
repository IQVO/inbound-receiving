// Package repository holds the non-interface vocabulary of the repository
// ports: their typed errors, list filters and the small read models of the
// two local copies (ADR 0003). It is separate from package ports because
// ports may contain interfaces only (internal/architecture's "ports package
// only contains interfaces" rule).
package repository

import (
	"errors"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

var (
	// ErrAsnNotFound is returned by AsnRepository.Get for an unknown number.
	ErrAsnNotFound = errors.New("asn not found")
	// ErrAppointmentNotFound is returned by AppointmentRepository.Get for an
	// unknown id.
	ErrAppointmentNotFound = errors.New("appointment not found")
	// ErrReceiptNotFound is returned by ReceiptRepository.Get (unknown id)
	// and OpenByAsn (no open receipt for the ASN).
	ErrReceiptNotFound = errors.New("receipt not found")
	// ErrConcurrentModification is returned by Save when the stored version
	// is not the version the caller loaded (or, for an insert, when the
	// identity already exists): another writer won the race.
	ErrConcurrentModification = errors.New("concurrent modification")
	// ErrReceiptAlreadyOpen is returned by ReceiptRepository.Save when
	// inserting a receipt for an ASN that already has an open one (the
	// partial unique index of ADR 0002).
	ErrReceiptAlreadyOpen = errors.New("asn already has an open receipt")
	// ErrPackLevelNotFound is returned by PackagingDirectory when no pack level
	// matches the GTIN (or the SKU and level).
	ErrPackLevelNotFound = errors.New("pack level not found")
)

// AsnFilter narrows AsnRepository.List. The zero value means no filter.
type AsnFilter struct {
	// State keeps only ASNs in this state when non-empty.
	State asn.State
}

// AppointmentFilter narrows AppointmentRepository.List. Zero values mean
// "no filter".
type AppointmentFilter struct {
	// Door keeps only appointments at this door when non-empty.
	Door appointment.DoorCode
	// State keeps only appointments in this state when non-empty.
	State appointment.State
	// From keeps only appointments whose window ends after it (when set).
	From time.Time
	// To keeps only appointments whose window starts before it (when set).
	To time.Time
}

// AppointmentCursor is the position after which an appointment page
// continues: appointments order by (window start, id). The zero value is
// the first page.
type AppointmentCursor struct {
	WindowStart time.Time
	ID          appointment.ID
}

// ReceiptFilter narrows ReceiptRepository.List. The zero value means no
// filter.
type ReceiptFilter struct {
	// AsnNumber keeps only receipts of this ASN when non-empty.
	AsnNumber asn.Number
	// State keeps only receipts in this state when non-empty.
	State receipt.State
}

// DockFlow says which traffic a dock door serves. Only the two inbound
// flavours are ever stored.
type DockFlow string

// The stored dock flows.
const (
	DockFlowInbound DockFlow = "Inbound"
	DockFlowBoth    DockFlow = "Both"
)

// DockDoor is one row of the dock_doors local copy.
type DockDoor struct {
	Code appointment.DoorCode
	Flow DockFlow
}

// PackLevel is one row of the PackagingView, the local projection of
// product-master's ProductPackLevelDeclared / ProductPackLevelRetired
// (contracts R8). A GTIN identifies exactly one (SKU, level); its quantity
// never changes (GS1 immutability, contracts G7), so a retired level stays
// valid for the physical cases already printed with it.
type PackLevel struct {
	GTIN      gs1.GTIN
	SKU       shared.SKU
	Level     shared.PackLevel
	ChildGTIN gs1.GTIN
	// QtyOfChild is the count of the next lower level, 0 for EACH.
	QtyOfChild int64
	// UnitsPerPack is the direct factor to eaches (1 for EACH).
	UnitsPerPack  int64
	EffectiveFrom time.Time
	// EffectiveTo is the zero time while the level is open-ended.
	EffectiveTo time.Time
	// Version is the product version of the last event applied (a stale
	// event is ignored).
	Version int64
}

// ActiveAt reports whether the level is in effect at t: effective from its
// start (inclusive) until its end (exclusive), when it has one.
func (p PackLevel) ActiveAt(t time.Time) bool {
	return !t.Before(p.EffectiveFrom) && (p.EffectiveTo.IsZero() || t.Before(p.EffectiveTo))
}
