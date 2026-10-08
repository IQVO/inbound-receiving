package asn

import "time"

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
	// ExpectedArrival is the zero time when the supplier gave none.
	ExpectedArrival time.Time
	Lines           []Line
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
