package asn

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// Event is a domain event raised by the Asn aggregate. Every event carries
// the ASN number (the CloudEvents subject and Kafka key) and when it
// happened (from the caller's clock).
type Event interface {
	EventName() string
	AsnNumber() Number
	OccurredAt() time.Time
}

// Header is embedded in every event.
type Header struct {
	Number Number
	At     time.Time
}

// AsnNumber returns the ASN the event is about.
func (h Header) AsnNumber() Number { return h.Number }

// OccurredAt returns when the change happened (UTC).
func (h Header) OccurredAt() time.Time { return h.At }

// ASNRegistered is raised when an ASN is registered.
type ASNRegistered struct {
	Header
	SupplierRef string
	// OwnerRef is empty for fleet-owned stock.
	OwnerRef shared.OwnerRef
	// ExpectedArrival is the zero time when the supplier gave none.
	ExpectedArrival time.Time
	// RegisteredAt is when the ASN was registered here (ADR 0025); the zero
	// time never happens on a newly raised event.
	RegisteredAt time.Time
	Lines        []Line
	// Cartons is empty for a flat ASN.
	Cartons []Carton
}

// EventName returns "ASNRegistered".
func (ASNRegistered) EventName() string { return "ASNRegistered" }

// ASNCancelled is raised when a Registered ASN is cancelled.
type ASNCancelled struct {
	Header
	// Reason is empty when none was given.
	Reason string
}

// EventName returns "ASNCancelled".
func (ASNCancelled) EventName() string { return "ASNCancelled" }

// AsnLateForArrival is raised, once per ASN, when the ASN was registered
// after the visit it was received under had already arrived at the gate
// (ADR 0025). The trigger is OpenReceipt, the only place an ASN and an
// appointment meet; the arrival comes from the visit_arrivals local copy.
type AsnLateForArrival struct {
	Header
	SupplierRef string
	// AppointmentID is the appointment the receipt was opened from and
	// VisitID the yard visit whose arrival was the earliest seen for it.
	AppointmentID   string
	VisitID         string
	AsnRegisteredAt time.Time
	ArrivedAt       time.Time
	// LatenessSeconds is registered_at - arrived_at, whole seconds, >= 1 by
	// construction (an equal or earlier registration is on time and raises
	// nothing).
	LatenessSeconds int64
}

// EventName returns "AsnLateForArrival".
func (AsnLateForArrival) EventName() string { return "AsnLateForArrival" }
