package usecases

import (
	"errors"
	"fmt"
)

var (
	// ErrAsnAlreadyExists is returned when registering an ASN number that is
	// already registered.
	ErrAsnAlreadyExists = errors.New("an asn with this number is already registered")
	// ErrUnknownSKU is returned (PRODUCT_MODE=kafka only) for a SKU
	// product-master never registered.
	ErrUnknownSKU = errors.New("sku is not known to product-master")
	// ErrUnknownDockDoor is returned (DOCK_DOOR_MODE=kafka only) for a door
	// that is not a known inbound dock door.
	ErrUnknownDockDoor = errors.New("door is not an inbound dock door")
	// ErrUnknownAsn is returned when a booking or receipt names an ASN that
	// does not exist.
	ErrUnknownAsn = errors.New("asn does not exist")
	// ErrUnknownAppointment is returned when a receipt names an appointment
	// that does not exist.
	ErrUnknownAppointment = errors.New("appointment does not exist")
	// ErrAsnNotOnAppointment is returned when a receipt names an appointment
	// that does not cover its ASN.
	ErrAsnNotOnAppointment = errors.New("asn is not covered by the appointment")
	// ErrAppointmentNotCheckedIn is returned when a receipt is opened from
	// an appointment that has not checked in.
	ErrAppointmentNotCheckedIn = errors.New("appointment is not checked in")
	// ErrVersionMismatch is returned when the caller's expected version
	// (If-Match) is not the current one.
	ErrVersionMismatch = errors.New("expected version does not match the current version")
	// ErrInvalidListQuery marks a malformed list query (limit, cursor or
	// filter); the HTTP adapter maps it to 400 invalid-query.
	ErrInvalidListQuery = errors.New("invalid list query")
	// ErrInvalidEvent marks a consumed event that can never be applied (a
	// payload that breaks a domain rule). The consumer logs it and commits
	// past the message.
	ErrInvalidEvent = errors.New("invalid event")
	// ErrInvalidMode is returned by ParseMode for an unknown mode value.
	ErrInvalidMode = errors.New("mode must be kafka or permissive")
)

// detailed is an error with a caller-facing message that still matches its
// sentinel with errors.Is.
type detailed struct {
	sentinel error
	msg      string
}

func (e *detailed) Error() string { return e.msg }
func (e *detailed) Unwrap() error { return e.sentinel }

func unknownSKU(sku string) error {
	return &detailed{sentinel: ErrUnknownSKU, msg: fmt.Sprintf("sku %s is not known to product-master", sku)}
}

func unknownDoor(door string) error {
	return &detailed{sentinel: ErrUnknownDockDoor, msg: fmt.Sprintf("door %s is not an inbound dock door", door)}
}

func unknownAsn(number string) error {
	return &detailed{sentinel: ErrUnknownAsn, msg: fmt.Sprintf("asn %s does not exist", number)}
}

func unknownAppointment(id string) error {
	return &detailed{sentinel: ErrUnknownAppointment, msg: fmt.Sprintf("appointment %s does not exist", id)}
}

// Mode selects whether a local copy is enforced (ADR 0003).
type Mode string

// The two modes of PRODUCT_MODE and DOCK_DOOR_MODE.
const (
	// ModePermissive accepts any value and does not run the consumer.
	ModePermissive Mode = "permissive"
	// ModeKafka enforces the local copy fed by the consumer.
	ModeKafka Mode = "kafka"
)

// ParseMode validates a mode value; "" is permissive.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case "", ModePermissive:
		return ModePermissive, nil
	case ModeKafka:
		return ModeKafka, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidMode, value)
	}
}
