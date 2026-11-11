package receipt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

var (
	// ErrInvalidCompanyPrefix is returned for a GS1 company prefix that is not 7
	// to 10 ASCII digits.
	ErrInvalidCompanyPrefix = errors.New("GS1 company prefix must be 7 to 10 digits")
	// ErrCompanyPrefixNotConfigured is returned when an SSCC is asked for and the
	// business has not supplied its (licensed) company prefix.
	ErrCompanyPrefixNotConfigured = errors.New("no GS1 company prefix is configured")
	// ErrInvalidSerial is returned for a serial that is below 1 or does not fit
	// after the prefix.
	ErrInvalidSerial = errors.New("serial reference does not fit the company prefix")
)

// receiverExtensionDigit is the SSCC extension digit of every SSCC this context
// assigns (ADR 0024).
const receiverExtensionDigit = 0

// CompanyPrefix is the GS1 company prefix the receiver allocates SSCCs under. The
// zero value is "not configured". There is no default: the prefix is a licensed
// asset the business supplies.
type CompanyPrefix struct{ v string }

// ParseCompanyPrefix accepts "" (not configured) or 7 to 10 ASCII digits.
func ParseCompanyPrefix(raw string) (CompanyPrefix, error) {
	if raw == "" {
		return CompanyPrefix{}, nil
	}
	if len(raw) < 7 || len(raw) > 10 || strings.Trim(raw, "0123456789") != "" {
		return CompanyPrefix{}, fmt.Errorf("%w: got %d characters", ErrInvalidCompanyPrefix, len(raw))
	}
	return CompanyPrefix{v: raw}, nil
}

// String is the prefix, or "" when not configured.
func (p CompanyPrefix) String() string { return p.v }

// IsZero reports whether no prefix is configured.
func (p CompanyPrefix) IsZero() bool { return p.v == "" }

// MaxSerial is the largest serial reference that fits after the prefix (the SSCC
// has 16 digits for prefix and serial together); 0 when not configured.
func (p CompanyPrefix) MaxSerial() int64 {
	if p.IsZero() {
		return 0
	}
	limit := int64(1)
	for i := 0; i < 16-len(p.v); i++ {
		limit *= 10
	}
	return limit - 1
}

// Build is the SSCC of a serial under the prefix: extension digit 0, the prefix,
// the serial zero-padded, the check digit (gs1.BuildSSCC).
func (p CompanyPrefix) Build(serial int64) (gs1.SSCC, error) {
	if p.IsZero() {
		return gs1.SSCC{}, ErrCompanyPrefixNotConfigured
	}
	if serial < 1 || serial > p.MaxSerial() {
		return gs1.SSCC{}, ErrInvalidSerial
	}
	return gs1.BuildSSCC(receiverExtensionDigit, p.v, strconv.FormatInt(serial, 10))
}
