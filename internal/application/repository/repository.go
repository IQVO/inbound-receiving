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
