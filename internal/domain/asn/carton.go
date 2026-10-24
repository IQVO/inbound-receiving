package asn

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

var (
	// ErrInvalidSscc is returned when a carton or pallet SSCC is not 18 digits
	// with a valid mod-10 check digit (contracts G3).
	ErrInvalidSscc = errors.New("sscc must be 18 digits with a valid check digit")
	// ErrDuplicateSscc is returned when an SSCC repeats within the ASN (or a
	// pallet SSCC is also a carton SSCC), and by the use case when it already
	// exists on another ASN of the site.
	ErrDuplicateSscc = errors.New("sscc must be unique")
	// ErrInvalidCartonKind is returned for a kind other than HOMOGENEOUS_CASE
	// or MIXED.
	ErrInvalidCartonKind = errors.New("carton kind must be HOMOGENEOUS_CASE or MIXED")
	// ErrInvalidCarton is returned for a carton that is structurally unusable:
	// no contents, a non-positive quantity, a bad lot or expiry, a negative
	// weight or a non-positive dimension.
	ErrInvalidCarton = errors.New("carton must have contents with positive quantities, a lot of 1..20 characters, an ISO expiry date and positive weight and dimensions when given")
	// ErrHomogeneousCartonMixed is returned when a HOMOGENEOUS_CASE carton
	// lists more than one SKU.
	ErrHomogeneousCartonMixed = errors.New("a homogeneous case carton must hold exactly one sku")
	// ErrCartonContentsMismatch is returned when cartons are present but their
	// contents disagree with the lines: a content names a line the ASN does not
	// have or another SKU than the line's, or the per-line sums differ from the
	// expected quantities.
	ErrCartonContentsMismatch = errors.New("carton contents must add up to every line's expected quantity with the line's sku")
)

// CartonKind says whether a carton holds one SKU or several.
type CartonKind string

// The carton kinds (contracts section 2).
const (
	CartonHomogeneousCase CartonKind = "HOMOGENEOUS_CASE"
	CartonMixed           CartonKind = "MIXED"
)

// Carton limits.
const (
	// MaxLotLength is the longest lot (GS1 AI 10).
	MaxLotLength = 20
	// ExpiryLayout is the date layout of an expiry (GS1 AI 17, as ISO 8601).
	ExpiryLayout = "2006-01-02"
)

// ContentInput is the unvalidated shape of one carton content, as it arrives
// from a request or a repository. Qty is in eaches.
type ContentInput struct {
	LineNo int
	SKU    string
	GTIN   string
	Qty    int64
	Lot    string
	Expiry string
}

// DimsInput is the unvalidated outer dimensions of a carton, in millimetres.
type DimsInput struct {
	LengthMm int64
	WidthMm  int64
	HeightMm int64
}

// CartonInput is the unvalidated shape of a carton. ParentSSCC is the pallet
// SSCC ("" when none); GrossWeightG 0 means unknown; Dims nil means unknown.
type CartonInput struct {
	SSCC         string
	ParentSSCC   string
	Kind         string
	Contents     []ContentInput
	GrossWeightG int64
	Dims         *DimsInput
}

// CartonContent is one validated carton content.
type CartonContent struct {
	lineNo int
	sku    shared.SKU
	gtin   gs1.GTIN
	qty    int64
	lot    string
	expiry string
}

// LineNo returns the ASN line the content rolls up into.
func (c CartonContent) LineNo() int { return c.lineNo }

// SKU returns the content's SKU.
func (c CartonContent) SKU() shared.SKU { return c.sku }

// GTIN returns the 14-digit GTIN of the content.
func (c CartonContent) GTIN() gs1.GTIN { return c.gtin }

// Qty returns the quantity in eaches.
func (c CartonContent) Qty() int64 { return c.qty }

// Lot returns the lot, or "" when none.
func (c CartonContent) Lot() string { return c.lot }

// Expiry returns the ISO expiry date, or "" when none.
func (c CartonContent) Expiry() string { return c.expiry }

// Dims are the validated outer dimensions of a carton, in millimetres.
type Dims struct {
	lengthMm int64
	widthMm  int64
	heightMm int64
}

// LengthMm returns the length.
func (d Dims) LengthMm() int64 { return d.lengthMm }

// WidthMm returns the width.
func (d Dims) WidthMm() int64 { return d.widthMm }

// HeightMm returns the height.
func (d Dims) HeightMm() int64 { return d.heightMm }

// Carton is one validated carton of the ASN: a logistic unit with an SSCC and
// a manifest of what is inside (eaches, per line).
type Carton struct {
	sscc         gs1.SSCC
	parent       gs1.SSCC
	kind         CartonKind
	contents     []CartonContent
	grossWeightG int64
	dims         *Dims
}

// SSCC returns the carton's SSCC.
func (c Carton) SSCC() gs1.SSCC { return c.sscc }

// ParentSSCC returns the pallet SSCC, or the zero value when none.
func (c Carton) ParentSSCC() gs1.SSCC { return c.parent }

// Kind returns the carton kind.
func (c Carton) Kind() CartonKind { return c.kind }

// Contents returns a copy of the manifest.
func (c Carton) Contents() []CartonContent { return append([]CartonContent(nil), c.contents...) }

// GrossWeightG returns the gross weight in grams, 0 when unknown.
func (c Carton) GrossWeightG() int64 { return c.grossWeightG }

// Dims returns the outer dimensions and whether they are known.
func (c Carton) Dims() (Dims, bool) {
	if c.dims == nil {
		return Dims{}, false
	}
	return *c.dims, true
}

// buildCartons validates the carton inputs against the (already validated)
// lines: every SSCC valid and unique, no pallet SSCC that is also a carton,
// and the contents adding up to every line's expected quantity.
func buildCartons(inputs []CartonInput, lines []Line) ([]Carton, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	cartons := make([]Carton, 0, len(inputs))
	seen := make(map[gs1.SSCC]struct{}, len(inputs))
	sums := make(map[int]int64, len(lines))
	for _, in := range inputs {
		c, err := buildCarton(in, lines)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[c.sscc]; dup {
			return nil, ErrDuplicateSscc
		}
		seen[c.sscc] = struct{}{}
		for _, x := range c.contents {
			sums[x.lineNo] += x.qty
		}
		cartons = append(cartons, c)
	}
	for _, c := range cartons {
		if _, isCarton := seen[c.parent]; isCarton {
			return nil, ErrDuplicateSscc
		}
	}
	for _, l := range lines {
		if sums[l.lineNo] != l.expectedQty {
			return nil, ErrCartonContentsMismatch
		}
	}
	return cartons, nil
}

func buildCarton(in CartonInput, lines []Line) (Carton, error) {
	sscc, err := parseSSCC(in.SSCC)
	if err != nil {
		return Carton{}, err
	}
	var parent gs1.SSCC
	if in.ParentSSCC != "" {
		if parent, err = parseSSCC(in.ParentSSCC); err != nil {
			return Carton{}, err
		}
	}
	kind, err := ParseCartonKind(in.Kind)
	if err != nil {
		return Carton{}, err
	}
	dims, err := buildDims(in)
	if err != nil {
		return Carton{}, err
	}
	contents, err := buildContents(in.Contents, lines)
	if err != nil {
		return Carton{}, err
	}
	if kind == CartonHomogeneousCase && skuCount(contents) > 1 {
		return Carton{}, ErrHomogeneousCartonMixed
	}
	return Carton{sscc: sscc, parent: parent, kind: kind, contents: contents, grossWeightG: in.GrossWeightG, dims: dims}, nil
}

func parseSSCC(value string) (gs1.SSCC, error) {
	s, err := gs1.ParseSSCC(value)
	if err != nil {
		return gs1.SSCC{}, fmt.Errorf("%w: %w", ErrInvalidSscc, err)
	}
	return s, nil
}

// ParseCartonKind validates a carton kind text.
func ParseCartonKind(value string) (CartonKind, error) {
	switch CartonKind(value) {
	case CartonHomogeneousCase, CartonMixed:
		return CartonKind(value), nil
	default:
		return "", ErrInvalidCartonKind
	}
}

func buildDims(in CartonInput) (*Dims, error) {
	if in.GrossWeightG < 0 {
		return nil, ErrInvalidCarton
	}
	if in.Dims == nil {
		return nil, nil
	}
	d := *in.Dims
	if d.LengthMm < 1 || d.WidthMm < 1 || d.HeightMm < 1 {
		return nil, ErrInvalidCarton
	}
	return &Dims{lengthMm: d.LengthMm, widthMm: d.WidthMm, heightMm: d.HeightMm}, nil
}

func buildContents(inputs []ContentInput, lines []Line) ([]CartonContent, error) {
	if len(inputs) == 0 {
		return nil, ErrInvalidCarton
	}
	out := make([]CartonContent, 0, len(inputs))
	for _, in := range inputs {
		c, err := buildContent(in, lines)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func buildContent(in ContentInput, lines []Line) (CartonContent, error) {
	if in.LineNo < 1 || in.LineNo > len(lines) {
		return CartonContent{}, ErrCartonContentsMismatch
	}
	sku, err := shared.NewSKU(in.SKU)
	if err != nil {
		return CartonContent{}, err
	}
	if lines[in.LineNo-1].sku != sku {
		return CartonContent{}, ErrCartonContentsMismatch
	}
	if err := shared.ValidateQuantity(in.Qty); err != nil {
		return CartonContent{}, err
	}
	gtin, err := gs1.ParseGTIN(in.GTIN)
	if err != nil {
		return CartonContent{}, err
	}
	if err := gs1.RejectVariableMeasure(gtin); err != nil {
		return CartonContent{}, err
	}
	if err := validateLot(in.Lot); err != nil {
		return CartonContent{}, err
	}
	if err := validateExpiry(in.Expiry); err != nil {
		return CartonContent{}, err
	}
	return CartonContent{lineNo: in.LineNo, sku: sku, gtin: gtin, qty: in.Qty, lot: in.Lot, expiry: in.Expiry}, nil
}

// validateLot accepts "" (no lot) or 1..MaxLotLength printable ASCII
// characters without a space.
func validateLot(lot string) error {
	if len(lot) > MaxLotLength {
		return ErrInvalidCarton
	}
	for i := 0; i < len(lot); i++ {
		if lot[i] < '!' || lot[i] > '~' {
			return ErrInvalidCarton
		}
	}
	return nil
}

// validateExpiry accepts "" (no expiry) or a calendar date as YYYY-MM-DD.
func validateExpiry(expiry string) error {
	if expiry == "" {
		return nil
	}
	if _, err := time.Parse(ExpiryLayout, expiry); err != nil {
		return ErrInvalidCarton
	}
	return nil
}

func skuCount(contents []CartonContent) int {
	seen := make(map[shared.SKU]struct{}, len(contents))
	for _, c := range contents {
		seen[c.sku] = struct{}{}
	}
	return len(seen)
}
