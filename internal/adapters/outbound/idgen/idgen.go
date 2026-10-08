// Package idgen is the UUID adapter of ports.IDGenerator.
package idgen

import "github.com/google/uuid"

// UUID mints `appt-<uuid>` and `rcpt-<uuid>` identities from random
// (version 4) UUIDs; the lowercase hex form matches the domain's id
// patterns.
type UUID struct{}

// NewAppointmentID implements ports.IDGenerator.
func (UUID) NewAppointmentID() string { return "appt-" + uuid.NewString() }

// NewReceiptID implements ports.IDGenerator.
func (UUID) NewReceiptID() string { return "rcpt-" + uuid.NewString() }
