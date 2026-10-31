package receipt

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// Event is a domain event raised by the Receipt aggregate. Every event
// carries the receipt id, the ASN number (the Kafka key of the receipt
// events, so one ASN's receiving stays ordered on one partition) and when it
// happened (from the caller's clock).
type Event interface {
	EventName() string
	ReceiptID() ID
	AsnNumber() asn.Number
	OccurredAt() time.Time
}

// Header is embedded in every event.
type Header struct {
	ID  ID
	Asn asn.Number
	At  time.Time
}

// ReceiptID returns the receipt the event is about.
func (h Header) ReceiptID() ID { return h.ID }

// AsnNumber returns the ASN being received.
func (h Header) AsnNumber() asn.Number { return h.Asn }

// OccurredAt returns when the change happened (UTC).
func (h Header) OccurredAt() time.Time { return h.At }

// ReceiptOpened is raised when receiving starts. AppointmentID and DoorCode
// are empty when absent.
type ReceiptOpened struct {
	Header
	AppointmentID appointment.ID
	DoorCode      appointment.DoorCode
}

// EventName returns "ReceiptOpened".
func (ReceiptOpened) EventName() string { return "ReceiptOpened" }

// ReceiptLineReceived is raised for every quantity received against a line.
// It is the handover event inventory-storage consumes (Good condition only,
// ADR 0003).
//
// Quantity is always in eaches. OwnerRef is the receipt's owner reference
// ("" for fleet-owned stock); PackGTIN and Cases are set for cases received
// by case GTIN and for the SSCC of a homogeneous case with an active CASE
// level; SSCC is set when the quantity came from accepting a carton.
type ReceiptLineReceived struct {
	Header
	LineNo    int
	SKU       shared.SKU
	Quantity  int64
	Condition Condition
	OwnerRef  shared.OwnerRef
	PackGTIN  gs1.GTIN
	Cases     int64
	SSCC      gs1.SSCC
}

// EventName returns "ReceiptLineReceived".
func (ReceiptLineReceived) EventName() string { return "ReceiptLineReceived" }

// ReceiptClosed is raised when receiving ends. Discrepancies is empty (not
// nil) when everything matched.
type ReceiptClosed struct {
	Header
	Discrepancies []Discrepancy
}

// EventName returns "ReceiptClosed".
func (ReceiptClosed) EventName() string { return "ReceiptClosed" }

// ReceiptCartonReceived is raised once per carton accepted. It is IDENTITY
// ONLY and never books quantity: ReceiptLineReceived stays the sole quantity
// authority (the double-count rule, ADR 0012). LPN is the SSCC, or an
// internal `LPN-<ulid>` when SSCC is absent. There is no site: the site is
// derived downstream at stow (contracts R6).
type ReceiptCartonReceived struct {
	Header
	OwnerRef   shared.OwnerRef
	SSCC       gs1.SSCC
	ParentSSCC gs1.SSCC
	Sealed     bool
	LPN        string
	Contents   []CartonContent
}

// EventName returns "ReceiptCartonReceived".
func (ReceiptCartonReceived) EventName() string { return "ReceiptCartonReceived" }

// CartonDiscrepancyRaised is raised when a scan or the close finds a carton
// problem; ReceiptClosed keeps its three line-level kinds (ADR 0012).
type CartonDiscrepancyRaised struct {
	Header
	CartonDiscrepancy
}

// EventName returns "CartonDiscrepancyRaised".
func (CartonDiscrepancyRaised) EventName() string { return "CartonDiscrepancyRaised" }
