package receipt

// W13Code is the receiving condition code of the X12 944/861 W13 segment (Item
// Detail Exception). The codes are the ones of the standards research behind
// ADR 0023; the empty value means the kind has no W13 code ("unmapped").
type W13Code string

// The W13 receiving condition codes this context uses.
const (
	W13Damaged          W13Code = "01"
	W13Short            W13Code = "02"
	W13Over             W13Code = "03"
	W13IncorrectProduct W13Code = "05"
	W13Hold             W13Code = "09"
)

// Disposition is a GS1 CBV disposition. CBV has no `quarantined`: a hold is the
// business step `holding` with a disposition such as non_conformant.
type Disposition string

// The CBV dispositions this context uses.
const (
	DispositionDamaged          Disposition = "damaged"
	DispositionMismatchQuantity Disposition = "mismatch_quantity"
	DispositionMismatchClass    Disposition = "mismatch_class"
	DispositionMismatchInstance Disposition = "mismatch_instance"
	DispositionNonConformant    Disposition = "non_conformant"
)

// Coding is the standard vocabulary of one kind: its W13 code (empty when
// unmapped) and its CBV disposition (ADR 0023).
type Coding struct {
	W13 W13Code
	CBV Disposition
}

// Mapped reports whether the kind has a W13 code.
func (c Coding) Mapped() bool { return c.W13 != "" }

// KindIncorrectProduct and KindHold are the two line kinds of the standard
// (W13 05 and 09) that this context lacked before ADR 0023. Neither is part of
// ReceiptClosed: they live on the receiving advice.
const (
	// KindIncorrectProduct: a manifest carton was refused because its label
	// names another product than its ASN line (derived, never typed in).
	KindIncorrectProduct Kind = "IncorrectProduct"
	// KindHold: units of a line were declared held by an explicit command.
	KindHold Kind = "Hold"
)

// codings is the table of ADR 0023, the single place to change a code.
var codings = map[Kind]Coding{
	KindShort:                       {W13Short, DispositionMismatchQuantity},
	KindOver:                        {W13Over, DispositionMismatchQuantity},
	KindDamaged:                     {W13Damaged, DispositionDamaged},
	KindIncorrectProduct:            {W13IncorrectProduct, DispositionMismatchClass},
	KindHold:                        {W13Hold, DispositionNonConformant},
	KindUnexpectedCarton:            {W13IncorrectProduct, DispositionMismatchInstance},
	KindUnknownGtin:                 {W13IncorrectProduct, DispositionMismatchClass},
	KindMixedCaseDeclaredAsCasePack: {W13IncorrectProduct, DispositionMismatchClass},
	KindMissingCarton:               {W13Short, DispositionMismatchQuantity},
	KindDuplicateSscc:               {"", DispositionMismatchInstance},
	KindCaseQtyMismatch:             {"", DispositionMismatchQuantity},
	KindMixedCartonContentMismatch:  {"", DispositionMismatchQuantity},
	KindUnlabelledCarton:            {"", DispositionNonConformant},
	KindCartonOverLimit:             {"", DispositionNonConformant},
	// ADR 0024: the label kinds have no W13 code either.
	KindLabelNonCompliant: {"", DispositionNonConformant},
	KindWrongDestination:  {"", DispositionMismatchInstance},
}

// CodingOf returns the W13 code and CBV disposition of a kind. It reports false
// for a kind the aggregate never raises.
func CodingOf(k Kind) (Coding, bool) {
	c, ok := codings[k]
	return c, ok
}

// AllKinds returns every kind the aggregate can raise: the three line kinds of
// ReceiptClosed, the two of the advice and the eleven carton kinds.
func AllKinds() []Kind {
	return []Kind{
		KindShort, KindOver, KindDamaged, KindIncorrectProduct, KindHold,
		KindUnexpectedCarton, KindMissingCarton, KindDuplicateSscc, KindUnknownGtin, KindCaseQtyMismatch,
		KindMixedCaseDeclaredAsCasePack, KindUnlabelledCarton, KindCartonOverLimit, KindMixedCartonContentMismatch,
		KindLabelNonCompliant, KindWrongDestination,
	}
}
