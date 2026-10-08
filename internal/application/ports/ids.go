package ports

// IDGenerator mints the identities of new aggregates.
type IDGenerator interface {
	// NewAppointmentID returns a fresh `appt-<uuid>`.
	NewAppointmentID() string
	// NewReceiptID returns a fresh `rcpt-<uuid>`.
	NewReceiptID() string
}
