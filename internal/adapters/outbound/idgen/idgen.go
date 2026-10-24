// Package idgen is the UUID adapter of ports.IDGenerator.
package idgen

import (
	"crypto/rand"
	"time"

	"github.com/google/uuid"
)

// UUID mints `appt-<uuid>` and `rcpt-<uuid>` identities from random
// (version 4) UUIDs; the lowercase hex form matches the domain's id
// patterns. Its license plates are `LPN-<ulid>`.
type UUID struct{}

// NewAppointmentID implements ports.IDGenerator.
func (UUID) NewAppointmentID() string { return "appt-" + uuid.NewString() }

// NewReceiptID implements ports.IDGenerator.
func (UUID) NewReceiptID() string { return "rcpt-" + uuid.NewString() }

// NewLPN implements ports.IDGenerator: `LPN-` followed by a ULID (48-bit
// millisecond timestamp and 80 random bits in 26 Crockford base32 characters),
// so plates sort by time and never collide in practice.
func (UUID) NewLPN() string { return "LPN-" + newULID(time.Now()) }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID encodes the timestamp and 10 fresh random bytes.
func newULID(now time.Time) string {
	var raw [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		panic("idgen: no randomness: " + err.Error()) // crypto/rand never fails on supported platforms
	}
	// 128 bits -> 26 groups of 5 bits (the first group holds 3 bits).
	var out [26]byte
	var acc uint16
	bits := 0
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint16(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&31]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}
