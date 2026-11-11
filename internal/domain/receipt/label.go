package receipt

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// The label kinds of ADR 0024. Both are carton findings: they are raised at the
// scan, refuse it, and book nothing.
const (
	// KindLabelNonCompliant: a GS1-128 element string that violates the standard
	// was scanned (Expected is the reason code, Actual the scanned string).
	KindLabelNonCompliant Kind = "LabelNonCompliant"
	// KindWrongDestination: the label's ship-to GLN (AI 410) is not one of this
	// site's receiving GLNs (Actual is the GLN on the label).
	KindWrongDestination Kind = "WrongDestination"
)

// LabelReason is the stable reason code of a LabelNonCompliant finding (ADR
// 0024): the set is closed, a new code is additive.
type LabelReason string

// The reason codes, taken from the error value the gs1 package returns.
const (
	ReasonBadCheckDigit     LabelReason = "bad_check_digit"
	ReasonInvalidIdentifier LabelReason = "invalid_identifier"
	ReasonConflictingAIs    LabelReason = "conflicting_ais"
	ReasonMissingAI         LabelReason = "missing_ai"
	ReasonDuplicateAI       LabelReason = "duplicate_ai"
	ReasonUnknownAI         LabelReason = "unknown_ai"
	ReasonBadDate           LabelReason = "bad_date"
	ReasonBadCharacter      LabelReason = "bad_character"
	ReasonMalformedElement  LabelReason = "malformed_element"
)

// AllLabelReasons lists every reason code in the order of ADR 0024.
func AllLabelReasons() []LabelReason {
	return []LabelReason{
		ReasonBadCheckDigit, ReasonInvalidIdentifier, ReasonConflictingAIs, ReasonMissingAI,
		ReasonDuplicateAI, ReasonUnknownAI, ReasonBadDate, ReasonBadCharacter, ReasonMalformedElement,
	}
}

// The gs1 package returns a message, and no error value, for these cases; the
// messages are fixed text of the shared package and a test pins them here.
const (
	checkDigitText = "check digit does not match"
	dateText       = ": date "
	characterText  = "82 character set"
)

// ClassifyLabelError maps an error of gs1.ParseScan to its reason code, by
// error value wherever the package has one.
func ClassifyLabelError(err error) LabelReason {
	switch {
	case errors.Is(err, gs1.ErrInvalidSSCC), errors.Is(err, gs1.ErrInvalidGTIN), errors.Is(err, gs1.ErrInvalidGLN):
		if strings.Contains(err.Error(), checkDigitText) {
			return ReasonBadCheckDigit
		}
		return ReasonInvalidIdentifier
	case errors.Is(err, gs1.ErrConflictingAIs):
		return ReasonConflictingAIs
	case errors.Is(err, gs1.ErrMissingAI):
		return ReasonMissingAI
	case errors.Is(err, gs1.ErrDuplicateAI):
		return ReasonDuplicateAI
	case errors.Is(err, gs1.ErrUnknownAI):
		return ReasonUnknownAI
	case errors.Is(err, gs1.ErrInvalidScan):
		msg := err.Error()
		if strings.Contains(msg, dateText) {
			return ReasonBadDate
		}
		if strings.Contains(msg, characterText) {
			return ReasonBadCharacter
		}
	}
	return ReasonMalformedElement
}

const (
	// MaxScanLength is the longest raw scan accepted as a GS1-128 element string
	// (the limit of `scan` in the OpenAPI contract).
	MaxScanLength = 512
	// MaxScannedLength bounds the scanned string a finding carries.
	MaxScannedLength = 128

	gs1128Prefix   = "]C1"
	groupSeparator = '\x1d'
	groupToken     = "<GS>"
)

// IsElementString reports whether raw is a GS1-128 element string: the ]C1
// symbology identifier, a non-empty body, at most MaxScanLength characters in
// all, and a body of printable ASCII (0x21 to 0x7E) and the GS separator only.
// Anything else that fails to parse is junk, not a label, and is not recorded.
func IsElementString(raw string) bool {
	if len(raw) <= len(gs1128Prefix) || len(raw) > MaxScanLength || !strings.HasPrefix(raw, gs1128Prefix) {
		return false
	}
	for i := len(gs1128Prefix); i < len(raw); i++ {
		c := raw[i]
		if c != groupSeparator && (c < '!' || c > '~') {
			return false
		}
	}
	return true
}

// SafeScanned is the scanned string as a finding carries it: the GS separator
// shown as <GS> and the whole cut at MaxScannedLength characters (a token that
// would not fit whole is dropped).
func SafeScanned(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		piece := raw[i : i+1]
		if raw[i] == groupSeparator {
			piece = groupToken
		}
		if b.Len()+len(piece) > MaxScannedLength {
			break
		}
		b.WriteString(piece)
	}
	return b.String()
}

// LabelViolation is a refused GS1-128 element string: why, and what was scanned.
type LabelViolation struct {
	Reason  LabelReason
	Scanned string
}

// NewLabelViolation builds the violation of a raw scan whose parse failed with err.
func NewLabelViolation(raw string, err error) LabelViolation {
	return LabelViolation{Reason: ClassifyLabelError(err), Scanned: SafeScanned(raw)}
}

// RejectLabel records a LabelNonCompliant finding for a refused label: nothing
// is booked and the version advances like for every refused scan. supplierRef is
// the supplier as the ASN knows it.
func (r *Receipt) RejectLabel(v LabelViolation, supplierRef string, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	return r.reject(at, nil, CartonDiscrepancy{Kind: KindLabelNonCompliant, Expected: string(v.Reason), Actual: v.Scanned, SupplierRef: supplierRef}), nil
}

// RejectDestination records a WrongDestination finding for a label addressed to
// another location: Actual is the GLN on the label, sscc the label's SSCC (zero
// when the label has none). Nothing is booked.
func (r *Receipt) RejectDestination(sscc gs1.SSCC, shipTo gs1.GLN, supplierRef string, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	return r.reject(at, nil, CartonDiscrepancy{Kind: KindWrongDestination, SSCC: sscc, Actual: shipTo.String(), SupplierRef: supplierRef}), nil
}

// ErrInvalidSiteGLN is returned for an entry of RECEIVING_SITE_GLNS that is not
// a valid 13-digit GLN.
var ErrInvalidSiteGLN = errors.New("receiving site GLN is invalid")

// SiteGLNs is the list of this warehouse's receiving GLNs. The zero value (and
// an empty list) turns the destination rule off.
type SiteGLNs struct{ glns []gs1.GLN }

// ParseSiteGLNs reads a comma-separated list of 13-digit GLNs, trimming blanks
// and ignoring empty entries. An entry that is not a valid GLN is
// ErrInvalidSiteGLN: the process refuses to boot on it.
func ParseSiteGLNs(raw string) (SiteGLNs, error) {
	var out SiteGLNs
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		g, err := gs1.ParseGLN(entry)
		if err != nil {
			return SiteGLNs{}, fmt.Errorf("%w: %q: %w", ErrInvalidSiteGLN, entry, err)
		}
		out.glns = append(out.glns, g)
	}
	return out, nil
}

// Enabled reports whether the destination rule is on (the list is not empty).
func (s SiteGLNs) Enabled() bool { return len(s.glns) > 0 }

// Accepts reports whether a label with the given ship-to GLN may be received:
// always when the rule is off or the label has no AI 410, else only when the
// GLN is one of the site's.
func (s SiteGLNs) Accepts(shipTo gs1.GLN) bool {
	if !s.Enabled() || shipTo.IsZero() {
		return true
	}
	for _, g := range s.glns {
		if g == shipTo {
			return true
		}
	}
	return false
}

// LabelData is what a label said besides what it identifies (ADR 0024): dates
// as ISO YYYY-MM-DD (a literal "-00" day is read as the last day of its month
// when compared), the serial, the purchase order and the ship-to GLN. Every
// field is optional.
type LabelData struct {
	BestBefore     string // AI 15
	Expiry         string // AI 17
	ProductionDate string // AI 11
	PackagingDate  string // AI 13
	Serial         string // AI 21
	PurchaseOrder  string // AI 400
	ShipToGLN      string // AI 410
	ShipForGLN     string // AI 413, stored and never enforced
}

// IsZero reports whether the label carries nothing.
func (l LabelData) IsZero() bool { return l == LabelData{} }

// SsccSource says who gave a carton its SSCC.
type SsccSource string

// The two sources.
const (
	// SsccSupplier: the SSCC came from the supplier's label (or its ASN).
	SsccSupplier SsccSource = "supplier"
	// SsccReceiver: the receiver allocated it (AssignSscc).
	SsccReceiver SsccSource = "receiver"
)
