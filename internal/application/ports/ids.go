package ports

// IDGenerator mints the identities of new aggregates.
type IDGenerator interface {
	// NewAppointmentID returns a fresh `appt-<uuid>`.
	NewAppointmentID() string
	// NewReceiptID returns a fresh `rcpt-<uuid>`.
	NewReceiptID() string
	// NewLPN returns a fresh internal license plate `LPN-<ulid>` for a carton
	// received without an SSCC (contracts section 2; there is no site, R6).
	NewLPN() string
}
