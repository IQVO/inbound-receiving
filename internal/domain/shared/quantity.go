package shared

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxQuantity is the largest quantity a line may expect or accumulate. It
// is the int32 bound so every downstream consumer can store it.
const MaxQuantity int64 = math.MaxInt32

// MaxReasonLength is the longest free-text reason, in characters (runes).
const MaxReasonLength = 200

var (
	// ErrInvalidQuantity is returned for a quantity below 1 or above
	// MaxQuantity.
	ErrInvalidQuantity = errors.New("quantity must be between 1 and 2147483647")
	// ErrInvalidReason is returned for a reason longer than MaxReasonLength
	// characters or containing a control character.
	ErrInvalidReason = errors.New("reason must be at most 200 characters without control characters")
)

// ValidateQuantity checks that q is a usable unit count.
func ValidateQuantity(q int64) error {
	if q < 1 || q > MaxQuantity {
		return ErrInvalidQuantity
	}
	return nil
}

// ValidateReason checks an optional free-text reason (empty is fine).
func ValidateReason(reason string) error {
	if utf8.RuneCountInString(reason) > MaxReasonLength {
		return ErrInvalidReason
	}
	if strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return ErrInvalidReason
	}
	return nil
}
