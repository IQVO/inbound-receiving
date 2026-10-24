package shared

import "errors"

// MaxOwnerRefLength is the longest owner reference (contracts G4).
const MaxOwnerRefLength = 64

// ErrInvalidOwnerRef is returned when an owner reference is longer than
// MaxOwnerRefLength or has a character outside [A-Za-z0-9._-].
var ErrInvalidOwnerRef = errors.New("owner reference must be at most 64 characters from [A-Za-z0-9._-]")

// OwnerRef is the opaque tenant tag of stock (contracts G4). The fleet has no
// seller concept: it arrives from the Vendor API, is validated by shape only,
// copied to the events and never interpreted here. The empty OwnerRef means
// fleet-owned stock, which is all pre-carton data.
type OwnerRef string

// NewOwnerRef validates an owner reference; "" is valid (absent).
func NewOwnerRef(value string) (OwnerRef, error) {
	if len(value) > MaxOwnerRefLength {
		return "", ErrInvalidOwnerRef
	}
	for i := 0; i < len(value); i++ {
		if !isRefByte(value[i]) {
			return "", ErrInvalidOwnerRef
		}
	}
	return OwnerRef(value), nil
}

func isRefByte(c byte) bool {
	if c >= 'a' && c <= 'z' {
		return true
	}
	if c >= 'A' && c <= 'Z' {
		return true
	}
	if c >= '0' && c <= '9' {
		return true
	}
	return c == '.' || c == '_' || c == '-'
}

// String returns the owner reference text ("" when absent).
func (o OwnerRef) String() string { return string(o) }
