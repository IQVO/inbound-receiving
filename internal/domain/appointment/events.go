package appointment

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// Event is a domain event raised by the DockAppointment aggregate. Every
// event carries the appointment id (the CloudEvents subject and Kafka key)
// and when it happened (from the caller's clock).
type Event interface {
	EventName() string
	AppointmentID() ID
	OccurredAt() time.Time
}

// Header is embedded in every event.
type Header struct {
	ID ID
	At time.Time
}

// AppointmentID returns the appointment the event is about.
func (h Header) AppointmentID() ID { return h.ID }

// OccurredAt returns when the change happened (UTC).
func (h Header) OccurredAt() time.Time { return h.At }

// DockAppointmentBooked is raised when a window is booked at a door.
type DockAppointmentBooked struct {
	Header
	DoorCode   DoorCode
	Carrier    string
	Window     Window
	AsnNumbers []asn.Number
}

// EventName returns "DockAppointmentBooked".
func (DockAppointmentBooked) EventName() string { return "DockAppointmentBooked" }

// DockAppointmentCheckedIn is raised when the carrier arrives at the door.
type DockAppointmentCheckedIn struct {
	Header
	DoorCode    DoorCode
	CheckedInAt time.Time
}

// EventName returns "DockAppointmentCheckedIn".
func (DockAppointmentCheckedIn) EventName() string { return "DockAppointmentCheckedIn" }

// DockAppointmentCancelled is raised when a Booked appointment is cancelled.
type DockAppointmentCancelled struct {
	Header
	DoorCode DoorCode
	// Reason is empty when none was given.
	Reason string
}

// EventName returns "DockAppointmentCancelled".
func (DockAppointmentCancelled) EventName() string { return "DockAppointmentCancelled" }

// DockAppointmentCompleted is raised when the receipt opened from the
// appointment closes.
type DockAppointmentCompleted struct {
	Header
	DoorCode    DoorCode
	CompletedAt time.Time
}

// EventName returns "DockAppointmentCompleted".
func (DockAppointmentCompleted) EventName() string { return "DockAppointmentCompleted" }
