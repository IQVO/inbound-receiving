// Package shared holds the small value types every inbound-receiving
// aggregate needs: the SKU reference, quantities and free-text reasons.
//
// The SKU rules are restated from product-master's `internal/domain/product`
// (ADR 0003 of this repository): bounded contexts never share Go code, so the
// same concept is spelled out here instead of imported.
package shared

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

// MaxSKULength is the longest SKU this context accepts.
const MaxSKULength = 64

// ErrInvalidSKU is returned when a SKU is empty, longer than MaxSKULength,
// or contains whitespace, a control character or '/'.
var ErrInvalidSKU = errors.New("sku must be 1..64 characters without whitespace, control characters or '/'")

// SKU identifies a product. It is a reference to product-master's aggregate;
// this context never owns the product.
type SKU string

// NewSKU validates a SKU.
func NewSKU(value string) (SKU, error) {
	if value == "" || utf8.RuneCountInString(value) > MaxSKULength {
		return "", ErrInvalidSKU
	}
	for _, r := range value {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrInvalidSKU
		}
	}
	return SKU(value), nil
}

// String returns the SKU text.
func (s SKU) String() string { return string(s) }
