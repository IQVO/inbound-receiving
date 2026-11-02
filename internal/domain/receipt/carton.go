package receipt

import (
	"errors"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

var (
	// ErrInvalidCases is returned for a case-GTIN scan of fewer than one or
	// more than MaxCasesPerScan cases.
	ErrInvalidCases = errors.New("cases must be between 1 and 1000 per scan")
	// ErrInvalidCarton is returned when a persisted carton ledger entry is
	// malformed (unknown status or kind, invalid SSCC or GTIN, an accepted
	// carton without a receive time).
	ErrInvalidCarton = errors.New("receipt carton is malformed")
	// ErrScanMismatch is returned when a carton scan's inputs are
	// inconsistent with each other (the use case resolves them; this guards
	// the aggregate).
	ErrScanMismatch = errors.New("scan inputs are inconsistent")
	// ErrCountedContentsNotMixed is returned when countedContents is given
	// for a carton that is not a MIXED one: only a mixed carton has no pack
	// GTIN to check its quantity against, so only it is audited by count.
	ErrCountedContentsNotMixed = errors.New("counted contents apply to a mixed carton only")
	// ErrInvalidCount is returned for a counted content with a negative
	// quantity or a repeated SKU.
	ErrInvalidCount = errors.New("counted contents are invalid")
)

// MaxCasesPerScan bounds the cases of one case-GTIN scan: each case becomes
// its own ReceiptCartonReceived.
const MaxCasesPerScan = 1000

// The carton discrepancy kinds of ADR 0012 decision 7 and ADR 0014.
const (
	// KindUnexpectedCarton: a valid SSCC that is not on the manifest was scanned.
	KindUnexpectedCarton Kind = "UnexpectedCarton"
	// KindMissingCarton: at close, a manifest carton was never accepted.
	KindMissingCarton Kind = "MissingCarton"
	// KindDuplicateSscc: an SSCC was scanned twice, or was already received.
	KindDuplicateSscc Kind = "DuplicateSscc"
	// KindUnknownGtin: a scanned GTIN does not resolve in the PackagingView.
	KindUnknownGtin Kind = "UnknownGtin"
	// KindCaseQtyMismatch: a carton's quantity differs from the units per
	// pack of the active CASE level, or from its label's count.
	KindCaseQtyMismatch Kind = "CaseQtyMismatch"
	// KindMixedCaseDeclaredAsCasePack: a homogeneous carton's label contradicts its manifest SKU.
	KindMixedCaseDeclaredAsCasePack Kind = "MixedCaseDeclaredAsCasePack"
	// KindUnlabelledCarton: a case with no SSCC was received against a carton ASN.
	KindUnlabelledCarton Kind = "UnlabelledCarton"
	// KindCartonOverLimit: a carton's DECLARED gross weight or longest side
	// is above the configured handling limit. The carton is still accepted
	// (ADR 0014); Expected holds the breached limits, Actual the declared values.
	KindCartonOverLimit Kind = "CartonOverLimit"
	// KindMixedCartonContentMismatch: the counted contents of a mixed carton
	// differ from its manifest for a SKU (Expected and Actual are "sku:qty").
	KindMixedCartonContentMismatch Kind = "MixedCartonContentMismatch"
)

// CartonLimits are the handling limits a carton is checked against at
// acceptance. A zero limit is unchecked (ADR 0014).
type CartonLimits struct {
	MaxGrossWeightG  int64
	MaxLongestSideMm int64
}

// DeclaredCarton is what the ASN declared about a carton's weight and size;
// zero means it declared nothing, which is never over a limit.
type DeclaredCarton struct {
	GrossWeightG  int64
	LongestSideMm int64
}

// CountedContent is one line of an audit count of a mixed carton: Qty eaches
// of SKU were counted (0 = looked for and found none). GTIN is the each GTIN
// of a SKU the manifest does not list.
type CountedContent struct {
	SKU  shared.SKU
	GTIN gs1.GTIN
	Qty  int64
}

// CartonStatus is where a carton of the receipt's ledger stands.
type CartonStatus string

// The carton statuses: a manifest carton is Expected until its SSCC is
// scanned; a carton received without being on the manifest (a case by GTIN)
// is Accepted from the start.
const (
	CartonExpected CartonStatus = "Expected"
	CartonAccepted CartonStatus = "Accepted"
)

// CartonContent is one content of a carton, in eaches. Condition is empty on
// an Expected carton.
type CartonContent struct {
	LineNo    int
	SKU       shared.SKU
	GTIN      gs1.GTIN
	Qty       int64
	Lot       string
	Expiry    string
	Condition Condition
}

// Carton is one entry of the receipt's carton ledger. Its LPN is the SSCC, or
// an internal `LPN-<ulid>` for a carton received without one.
type Carton struct {
	lpn        string
	sscc       gs1.SSCC
	parentSscc gs1.SSCC
	kind       asn.CartonKind
	status     CartonStatus
	sealed     bool
	contents   []CartonContent
	receivedAt time.Time
}

// LPN returns the license plate: the SSCC, or the internal LPN.
func (c Carton) LPN() string { return c.lpn }

// SSCC returns the SSCC, or the zero value for an unlabelled carton.
func (c Carton) SSCC() gs1.SSCC { return c.sscc }

// ParentSSCC returns the pallet SSCC, or the zero value.
func (c Carton) ParentSSCC() gs1.SSCC { return c.parentSscc }

// Kind returns the carton kind.
func (c Carton) Kind() asn.CartonKind { return c.kind }

// Status returns Expected or Accepted.
func (c Carton) Status() CartonStatus { return c.status }

// Sealed reports whether the carton was received unopened.
func (c Carton) Sealed() bool { return c.sealed }

// Contents returns a copy of the contents.
func (c Carton) Contents() []CartonContent { return append([]CartonContent(nil), c.contents...) }

// ReceivedAt returns when the carton was accepted, or the zero time while Expected.
func (c Carton) ReceivedAt() time.Time { return c.receivedAt }

// CartonContentState is the unvalidated stored shape of a carton content.
type CartonContentState struct {
	LineNo    int
	SKU       string
	GTIN      string
	Qty       int64
	Lot       string
	Expiry    string
	Condition string
}

// CartonState is the unvalidated stored shape of a ledger entry. SSCC and
// ParentSSCC are "" when absent; ReceivedAt is the zero time while Expected.
type CartonState struct {
	LPN        string
	SSCC       string
	ParentSSCC string
	Kind       string
	Status     CartonStatus
	Sealed     bool
	Contents   []CartonContentState
	ReceivedAt time.Time
}

// CasePack is the active CASE level of a SKU as the PackagingView reports it.
type CasePack struct {
	GTIN         gs1.GTIN
	UnitsPerPack int64
}

// CartonScan is the resolved input of AcceptCarton: one scanned SSCC plus the
// facts the aggregate cannot know (the use case reads them from the
// PackagingView and the site-wide ledger).
type CartonScan struct {
	SSCC      gs1.SSCC
	Condition Condition
	// LabelContentGTIN, LabelContentSKU and LabelCount come from AI 02 and AI
	// 37 of a GS1-128 case label; zero values when the label has none.
	// LabelContentSKU is empty when LabelContentGTIN does not resolve.
	LabelContentGTIN gs1.GTIN
	LabelContentSKU  shared.SKU
	LabelCount       int64
	// ReceivedElsewhere: the SSCC was already accepted on another receipt.
	ReceivedElsewhere bool
	// CasePacks holds the active CASE level of each SKU that has one.
	CasePacks map[shared.SKU]CasePack
	// Declared and Limits drive CartonOverLimit (R9).
	Declared DeclaredCarton
	Limits   CartonLimits
	// Counted is the optional audit count of a MIXED carton (R10); when
	// present the RECEIVED quantity is the counted one, never the label.
	Counted []CountedContent
}

// CaseScan is the resolved input of ReceiveCases: Cases cases of one SKU,
// scanned by their case GTIN. LPNs are the internal license plates, one per case.
type CaseScan struct {
	SKU       shared.SKU
	Pack      CasePack
	ChildGTIN gs1.GTIN
	Cases     int64
	Condition Condition
	Lot       string
	Expiry    string
	LPNs      []string
}

// CartonDiscrepancy is a finding about a physical carton. SSCC, Expected and
// Actual are empty when they do not apply.
type CartonDiscrepancy struct {
	Kind     Kind
	SSCC     gs1.SSCC
	Expected string
	Actual   string
}

// ScanResult is what a carton operation did: the events raised (line events
// first, then carton events, then discrepancies), whether a carton was
// accepted, the carton entries concerned and the discrepancies raised.
type ScanResult struct {
	Events        []Event
	Accepted      bool
	Cartons       []Carton
	Discrepancies []CartonDiscrepancy
}

// hasManifest reports whether the ASN announced labelled cartons: a labelled
// carton exists on the ledger only through the manifest.
func (r *Receipt) hasManifest() bool {
	for _, c := range r.cartons {
		if !c.sscc.IsZero() {
			return true
		}
	}
	return false
}

func (r *Receipt) cartonIndex(sscc gs1.SSCC) int {
	for i, c := range r.cartons {
		if c.sscc == sscc && !sscc.IsZero() {
			return i
		}
	}
	return -1
}

// OwnerRef returns the owner reference copied from the ASN, or "".
func (r *Receipt) OwnerRef() shared.OwnerRef { return r.ownerRef }

// Cartons returns a copy of the carton ledger in manifest order, then in
// the order of acceptance.
func (r *Receipt) Cartons() []Carton { return append([]Carton(nil), r.cartons...) }

// Carton returns the ledger entry with the given SSCC.
func (r *Receipt) Carton(sscc gs1.SSCC) (Carton, bool) {
	idx := r.cartonIndex(sscc)
	if idx < 0 {
		return Carton{}, false
	}
	return r.cartons[idx], true
}

// CartonByLPN returns the ledger entry with the given license plate (the
// SSCC, or an internal LPN).
func (r *Receipt) CartonByLPN(lpn string) (Carton, bool) {
	for _, c := range r.cartons {
		if c.lpn == lpn && lpn != "" {
			return c, true
		}
	}
	return Carton{}, false
}

// LineOfSKU returns the line number carrying sku.
func (r *Receipt) LineOfSKU(sku shared.SKU) (int, bool) {
	for _, l := range r.lines {
		if l.sku == sku {
			return l.lineNo, true
		}
	}
	return 0, false
}

func (s CartonStatus) valid() bool { return s == CartonExpected || s == CartonAccepted }
