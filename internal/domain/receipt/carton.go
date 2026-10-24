package receipt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// AcceptCarton accepts the manifest of the scanned SSCC: every content line
// is booked through the same rules as ReceiveLine (so ReceiptLineReceived
// stays the sole quantity authority), then the carton's identity is recorded
// on ReceiptCartonReceived. A scan that cannot be accepted books nothing and
// raises its findings as CartonDiscrepancyRaised; the receipt still changes
// version, because the findings are part of its history.
func (r *Receipt) AcceptCarton(in CartonScan, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	if !in.Condition.valid() {
		return ScanResult{}, ErrInvalidCondition
	}
	idx := r.cartonIndex(in.SSCC)
	if idx >= 0 && r.cartons[idx].status == CartonAccepted {
		return r.reject(at, []Carton{r.cartons[idx]}, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if in.ReceivedElsewhere {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if idx < 0 {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindUnexpectedCarton, SSCC: in.SSCC}), nil
	}
	if len(in.Counted) > 0 && r.cartons[idx].kind != asn.CartonMixed {
		return ScanResult{}, ErrCountedContentsNotMixed
	}
	if findings := checkCarton(r.cartons[idx], in); len(findings) > 0 {
		return r.reject(at, nil, findings...), nil
	}
	return r.acceptCarton(idx, in, at)
}

// checkCarton compares a homogeneous carton with what its label says and with
// the active CASE level of its SKU. A mixed carton has no pack GTIN and a
// label that need not be homogeneous, so it is never checked.
func checkCarton(c Carton, in CartonScan) []CartonDiscrepancy {
	if c.kind != asn.CartonHomogeneousCase {
		return nil
	}
	sku, qty := c.contents[0].SKU, cartonQty(c)
	var out []CartonDiscrepancy
	if !in.LabelContentGTIN.IsZero() {
		if in.LabelContentSKU == "" {
			out = append(out, CartonDiscrepancy{Kind: KindUnknownGtin, SSCC: in.SSCC, Actual: in.LabelContentGTIN.String()})
		} else if in.LabelContentSKU != sku {
			out = append(out, CartonDiscrepancy{Kind: KindMixedCaseDeclaredAsCasePack, SSCC: in.SSCC, Expected: string(sku), Actual: string(in.LabelContentSKU)})
		}
	}
	if in.LabelCount != 0 && in.LabelCount != qty {
		out = append(out, mismatch(in.SSCC, qty, in.LabelCount))
	}
	if pack, ok := in.CasePacks[sku]; ok && pack.UnitsPerPack != qty {
		out = append(out, mismatch(in.SSCC, pack.UnitsPerPack, qty))
	}
	return out
}

func mismatch(sscc gs1.SSCC, expected, actual int64) CartonDiscrepancy {
	return CartonDiscrepancy{Kind: KindCaseQtyMismatch, SSCC: sscc, Expected: strconv.FormatInt(expected, 10), Actual: strconv.FormatInt(actual, 10)}
}

func cartonQty(c Carton) int64 {
	var total int64
	for _, x := range c.contents {
		total += x.Qty
	}
	return total
}

func (r *Receipt) acceptCarton(idx int, in CartonScan, at time.Time) (ScanResult, error) {
	c := r.cartons[idx]
	lines := r.Lines()
	manifest := c.contents
	var findings []CartonDiscrepancy
	if len(in.Counted) > 0 {
		var err error
		if manifest, findings, err = r.audit(c, in); err != nil {
			return ScanResult{}, err
		}
	}
	contents := make([]CartonContent, 0, len(manifest))
	var events []Event
	for _, x := range manifest {
		if err := bookLine(lines, x.LineNo, x.Qty, in.Condition); err != nil {
			return ScanResult{}, err
		}
		x.Condition = in.Condition
		contents = append(contents, x)
		line := ReceiptLineReceived{
			Header: r.header(at), LineNo: x.LineNo, SKU: x.SKU, Quantity: x.Qty, Condition: in.Condition,
			OwnerRef: r.ownerRef, SSCC: in.SSCC,
		}
		if pack, ok := in.CasePacks[x.SKU]; ok && c.kind == asn.CartonHomogeneousCase {
			line.PackGTIN, line.Cases = pack.GTIN, 1
		}
		events = append(events, line)
	}
	c.contents, c.status, c.sealed, c.receivedAt = contents, CartonAccepted, true, at.UTC()
	events = append(events, r.cartonReceived(c, at))
	if f, over := overLimit(in); over {
		findings = append(findings, f)
	}
	events = append(events, r.discrepancyEvents(findings, at)...)
	r.lines, r.cartons[idx] = lines, c
	r.version++
	return ScanResult{Events: events, Accepted: true, Cartons: []Carton{c}, Discrepancies: findings}, nil
}

// overLimit compares the declared weight and longest side with the limits;
// one finding names every breached limit (ADR 0014).
func overLimit(in CartonScan) (CartonDiscrepancy, bool) {
	var expected, actual []string
	if in.Limits.MaxGrossWeightG > 0 && in.Declared.GrossWeightG > in.Limits.MaxGrossWeightG {
		expected = append(expected, "grossWeightG:"+strconv.FormatInt(in.Limits.MaxGrossWeightG, 10))
		actual = append(actual, "grossWeightG:"+strconv.FormatInt(in.Declared.GrossWeightG, 10))
	}
	if in.Limits.MaxLongestSideMm > 0 && in.Declared.LongestSideMm > in.Limits.MaxLongestSideMm {
		expected = append(expected, "longestSideMm:"+strconv.FormatInt(in.Limits.MaxLongestSideMm, 10))
		actual = append(actual, "longestSideMm:"+strconv.FormatInt(in.Declared.LongestSideMm, 10))
	}
	if len(expected) == 0 {
		return CartonDiscrepancy{}, false
	}
	return CartonDiscrepancy{Kind: KindCartonOverLimit, SSCC: in.SSCC, Expected: strings.Join(expected, ","), Actual: strings.Join(actual, ",")}, true
}

// audit turns the counted contents of a mixed carton into the contents that
// are received (the COUNTED quantities) and one MixedCartonContentMismatch
// per SKU whose count differs from the manifest.
func (r *Receipt) audit(c Carton, in CartonScan) ([]CartonContent, []CartonDiscrepancy, error) {
	counted := make(map[shared.SKU]CountedContent, len(in.Counted))
	for _, x := range in.Counted {
		if _, dup := counted[x.SKU]; dup || x.Qty < 0 {
			return nil, nil, ErrInvalidCount
		}
		counted[x.SKU] = x
	}
	var contents []CartonContent
	var findings []CartonDiscrepancy
	listed := map[shared.SKU]bool{}
	for _, m := range c.contents {
		listed[m.SKU] = true
		got := counted[m.SKU].Qty
		if got != m.Qty {
			findings = append(findings, skuMismatch(in.SSCC, m.SKU, m.Qty, got))
		}
		if got > 0 {
			m.Qty = got
			contents = append(contents, m)
		}
	}
	for _, x := range in.Counted {
		if listed[x.SKU] {
			continue
		}
		lineNo, ok := r.LineOfSKU(x.SKU)
		if !ok {
			return nil, nil, ErrLineNotOnAsn
		}
		if x.Qty > 0 {
			findings = append(findings, skuMismatch(in.SSCC, x.SKU, 0, x.Qty))
			contents = append(contents, CartonContent{LineNo: lineNo, SKU: x.SKU, GTIN: x.GTIN, Qty: x.Qty})
		}
	}
	return contents, findings, nil
}

func skuMismatch(sscc gs1.SSCC, sku shared.SKU, expected, actual int64) CartonDiscrepancy {
	return CartonDiscrepancy{
		Kind: KindMixedCartonContentMismatch, SSCC: sscc,
		Expected: string(sku) + ":" + strconv.FormatInt(expected, 10), Actual: string(sku) + ":" + strconv.FormatInt(actual, 10),
	}
}

func (r *Receipt) cartonReceived(c Carton, at time.Time) ReceiptCartonReceived {
	return ReceiptCartonReceived{
		Header: r.header(at), OwnerRef: r.ownerRef, SSCC: c.sscc, ParentSSCC: c.parentSscc,
		Sealed: c.sealed, LPN: c.lpn, Contents: c.Contents(),
	}
}

// ReceiveCases receives cases scanned by their case GTIN: Cases x
// UnitsPerPack eaches are booked on the SKU's line by ONE ReceiptLineReceived,
// and every case becomes its own sealed carton with an internal license plate
// on ReceiptCartonReceived. Against a carton ASN a case without an SSCC also
// raises UnlabelledCarton; against a flat ASN nobody promised a label.
func (r *Receipt) ReceiveCases(in CaseScan, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	if !in.Condition.valid() {
		return ScanResult{}, ErrInvalidCondition
	}
	if in.Cases < 1 || in.Cases > MaxCasesPerScan {
		return ScanResult{}, ErrInvalidCases
	}
	if int64(len(in.LPNs)) != in.Cases {
		return ScanResult{}, ErrScanMismatch
	}
	lineNo, ok := r.LineOfSKU(in.SKU)
	if !ok {
		return ScanResult{}, ErrLineNotOnAsn
	}
	if err := shared.ValidateQuantity(in.Pack.UnitsPerPack); err != nil {
		return ScanResult{}, err
	}
	qty := in.Cases * in.Pack.UnitsPerPack
	lines := r.Lines()
	if err := bookLine(lines, lineNo, qty, in.Condition); err != nil {
		return ScanResult{}, err
	}
	labelled := r.hasManifest()
	events := []Event{ReceiptLineReceived{
		Header: r.header(at), LineNo: lineNo, SKU: in.SKU, Quantity: qty, Condition: in.Condition,
		OwnerRef: r.ownerRef, PackGTIN: in.Pack.GTIN, Cases: in.Cases,
	}}
	res := ScanResult{Accepted: true}
	for _, lpn := range in.LPNs {
		c := Carton{
			lpn: lpn, kind: asn.CartonHomogeneousCase, status: CartonAccepted, sealed: true, receivedAt: at.UTC(),
			contents: []CartonContent{{LineNo: lineNo, SKU: in.SKU, GTIN: in.ChildGTIN, Qty: in.Pack.UnitsPerPack, Lot: in.Lot, Expiry: in.Expiry, Condition: in.Condition}},
		}
		r.cartons = append(r.cartons, c)
		res.Cartons = append(res.Cartons, c)
		events = append(events, r.cartonReceived(c, at))
		if labelled {
			res.Discrepancies = append(res.Discrepancies, CartonDiscrepancy{Kind: KindUnlabelledCarton, Actual: lpn})
		}
	}
	res.Events = events
	res.Events = append(res.Events, r.discrepancyEvents(res.Discrepancies, at)...)
	r.lines = lines
	r.version++
	return res, nil
}

// RejectUnknownGtin records that a scanned GTIN does not resolve in the
// PackagingView: nothing is booked, the finding is raised.
func (r *Receipt) RejectUnknownGtin(gtin gs1.GTIN, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	return r.reject(at, nil, CartonDiscrepancy{Kind: KindUnknownGtin, Actual: gtin.String()}), nil
}

func (r *Receipt) requireOpen() error {
	if r.state != StateOpen {
		return ErrReceiptClosed
	}
	return nil
}

// reject raises the findings of a scan that was not accepted.
func (r *Receipt) reject(at time.Time, cartons []Carton, findings ...CartonDiscrepancy) ScanResult {
	r.version++
	return ScanResult{Events: r.discrepancyEvents(findings, at), Cartons: cartons, Discrepancies: findings}
}

func (r *Receipt) discrepancyEvents(findings []CartonDiscrepancy, at time.Time) []Event {
	out := make([]Event, 0, len(findings))
	for _, d := range findings {
		out = append(out, CartonDiscrepancyRaised{Header: r.header(at), CartonDiscrepancy: d})
	}
	return out
}

// missingCartonEvents raises MissingCarton for every manifest carton that was
// never accepted.
func (r *Receipt) missingCartonEvents(at time.Time) []Event {
	var missing []CartonDiscrepancy
	for _, c := range r.cartons {
		if c.status == CartonExpected {
			missing = append(missing, CartonDiscrepancy{Kind: KindMissingCarton, Expected: c.sscc.String()})
		}
	}
	return r.discrepancyEvents(missing, at)
}

// bookLine adds qty to the line of lines in the given condition, enforcing
// the quantity bound. lines is a COPY of the receipt's lines, so a failure
// part-way through a carton leaves the receipt untouched.
func bookLine(lines []Line, lineNo int, qty int64, condition Condition) error {
	line := &lines[lineNo-1]
	if err := shared.ValidateQuantity(qty); err != nil {
		return err
	}
	if line.Received()+qty > shared.MaxQuantity {
		return shared.ErrInvalidQuantity
	}
	if condition == ConditionGood {
		line.receivedGood += qty
	} else {
		line.receivedDamaged += qty
	}
	return nil
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

// manifestStates turns the ASN's cartons into Expected ledger entries.
func manifestStates(cartons []asn.Carton) []CartonState {
	out := make([]CartonState, 0, len(cartons))
	for _, c := range cartons {
		contents := make([]CartonContentState, 0, len(c.Contents()))
		for _, x := range c.Contents() {
			contents = append(contents, CartonContentState{
				LineNo: x.LineNo(), SKU: string(x.SKU()), GTIN: x.GTIN().String(), Qty: x.Qty(), Lot: x.Lot(), Expiry: x.Expiry(),
			})
		}
		out = append(out, CartonState{
			LPN: c.SSCC().String(), SSCC: c.SSCC().String(), ParentSSCC: c.ParentSSCC().String(),
			Kind: string(c.Kind()), Status: CartonExpected, Contents: contents,
		})
	}
	return out
}

// buildCartons rebuilds the ledger from its stored shape, re-validating it
// against the lines.
func buildCartons(states []CartonState, lines []Line) ([]Carton, error) {
	if len(states) == 0 {
		return nil, nil
	}
	out := make([]Carton, 0, len(states))
	for _, s := range states {
		c, err := buildCarton(s, len(lines))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func buildCarton(s CartonState, lineCount int) (Carton, error) {
	if s.LPN == "" || !s.Status.valid() || len(s.Contents) == 0 {
		return Carton{}, ErrInvalidCarton
	}
	accepted := s.Status == CartonAccepted
	if accepted == s.ReceivedAt.IsZero() || (s.Sealed && !accepted) {
		return Carton{}, ErrInvalidCarton
	}
	sscc, err := optionalSSCC(s.SSCC)
	if err != nil {
		return Carton{}, err
	}
	parent, err := optionalSSCC(s.ParentSSCC)
	if err != nil {
		return Carton{}, err
	}
	kind, err := asn.ParseCartonKind(s.Kind)
	if err != nil {
		return Carton{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
	}
	contents := make([]CartonContent, 0, len(s.Contents))
	for _, x := range s.Contents {
		c, err := buildCartonContent(x, accepted, lineCount)
		if err != nil {
			return Carton{}, err
		}
		contents = append(contents, c)
	}
	return Carton{
		lpn: s.LPN, sscc: sscc, parentSscc: parent, kind: kind, status: s.Status, sealed: s.Sealed,
		contents: contents, receivedAt: s.ReceivedAt.UTC(),
	}, nil
}

func optionalSSCC(value string) (gs1.SSCC, error) {
	if value == "" {
		return gs1.SSCC{}, nil
	}
	s, err := gs1.ParseSSCC(value)
	if err != nil {
		return gs1.SSCC{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
	}
	return s, nil
}

func buildCartonContent(s CartonContentState, accepted bool, lineCount int) (CartonContent, error) {
	if s.LineNo < 1 || s.LineNo > lineCount {
		return CartonContent{}, ErrInvalidCarton
	}
	sku, err := shared.NewSKU(s.SKU)
	if err != nil {
		return CartonContent{}, err
	}
	gtin, err := gs1.ParseGTIN(s.GTIN)
	if err != nil {
		return CartonContent{}, err
	}
	if err := shared.ValidateQuantity(s.Qty); err != nil {
		return CartonContent{}, err
	}
	var condition Condition
	if accepted {
		if condition, err = ParseCondition(s.Condition); err != nil {
			return CartonContent{}, fmt.Errorf("%w: %w", ErrInvalidCarton, err)
		}
	} else if s.Condition != "" {
		return CartonContent{}, ErrInvalidCarton
	}
	return CartonContent{LineNo: s.LineNo, SKU: sku, GTIN: gtin, Qty: s.Qty, Lot: s.Lot, Expiry: s.Expiry, Condition: condition}, nil
}

// Persisted returns the stored shape of the receipt, the inverse of Rehydrate.
func (r *Receipt) Persisted() Persisted {
	lines := make([]LineState, 0, len(r.lines))
	for _, l := range r.lines {
		lines = append(lines, LineState{
			LineNo: l.lineNo, SKU: string(l.sku), ExpectedQty: l.expectedQty,
			ReceivedGood: l.receivedGood, ReceivedDamaged: l.receivedDamaged,
		})
	}
	cartons := make([]CartonState, 0, len(r.cartons))
	for _, c := range r.cartons {
		cartons = append(cartons, c.state())
	}
	return Persisted{
		ID: string(r.id), AsnNumber: string(r.asnNumber), OwnerRef: string(r.ownerRef),
		AppointmentID: string(r.appointmentID), DoorCode: string(r.doorCode), State: r.state,
		Lines: lines, Cartons: cartons, OpenedAt: r.openedAt, ClosedAt: r.closedAt, Version: r.version,
	}
}

func (c Carton) state() CartonState {
	contents := make([]CartonContentState, 0, len(c.contents))
	for _, x := range c.contents {
		contents = append(contents, CartonContentState{
			LineNo: x.LineNo, SKU: string(x.SKU), GTIN: x.GTIN.String(), Qty: x.Qty,
			Lot: x.Lot, Expiry: x.Expiry, Condition: string(x.Condition),
		})
	}
	return CartonState{
		LPN: c.lpn, SSCC: c.sscc.String(), ParentSSCC: c.parentSscc.String(), Kind: string(c.kind),
		Status: c.status, Sealed: c.sealed, Contents: contents, ReceivedAt: c.receivedAt,
	}
}
