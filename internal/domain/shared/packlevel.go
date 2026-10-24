package shared

import "errors"

// ErrInvalidPackLevel is returned for a pack level other than EACH, INNER,
// CASE or PALLET (contracts G6).
var ErrInvalidPackLevel = errors.New("pack level must be EACH, INNER, CASE or PALLET")

// PackLevel is a level of a product's packaging hierarchy as product-master
// publishes it. INNER and PALLET exist in the model; receiving implements
// only EACH and CASE in v1 (contracts G6).
type PackLevel string

// The pack levels.
const (
	PackEach   PackLevel = "EACH"
	PackInner  PackLevel = "INNER"
	PackCase   PackLevel = "CASE"
	PackPallet PackLevel = "PALLET"
)

// ParsePackLevel validates a pack level text.
func ParsePackLevel(value string) (PackLevel, error) {
	switch PackLevel(value) {
	case PackEach, PackInner, PackCase, PackPallet:
		return PackLevel(value), nil
	default:
		return "", ErrInvalidPackLevel
	}
}

// String returns the level text.
func (l PackLevel) String() string { return string(l) }
