// Package asn holds the Asn aggregate: an Advance Ship Notice, the supplier's
// announcement of what it is about to deliver (ADR 0002). The ASN is the
// consistency boundary for its own lines and its own lifecycle; the physical
// receiving of those lines belongs to the Receipt aggregate, which only
// holds a snapshot of the ASN.
package asn

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

const (
	// MaxNumberLength is the longest ASN number.
	MaxNumberLength = 64
	// MaxSupplierRefLength is the longest supplier reference, in characters.
	MaxSupplierRefLength = 64
)

var (
	// ErrInvalidNumber is returned when an ASN number is not 1..64
	// characters of [A-Za-z0-9._-].
	ErrInvalidNumber = errors.New("asn number must be 1..64 characters from [A-Za-z0-9._-]")
	// ErrInvalidSupplierRef is returned when the supplier reference is blank,
	// too long or contains a control character.
	ErrInvalidSupplierRef = errors.New("supplier reference must be 1..64 non-blank characters without control characters")
	// ErrNoLines is returned when an ASN has no line.
	ErrNoLines = errors.New("asn must have at least one line")
	// ErrInvalidLineNo is returned when line numbers are not exactly 1..n in order.
	ErrInvalidLineNo = errors.New("asn line numbers must be 1..n in order")
	// ErrDuplicateSKU is returned when two lines carry the same SKU.
	ErrDuplicateSKU = errors.New("asn lines must have distinct SKUs")
	// ErrInvalidState is returned when a persisted state is not a known one.
	ErrInvalidState = errors.New("unknown asn state")
	// ErrInvalidVersion is returned when a persisted version is below 1.
	ErrInvalidVersion = errors.New("asn version must be at least 1")
	// ErrAsnInProgress is returned when cancelling an ASN that is already
	// being received.
	ErrAsnInProgress = errors.New("asn is being received and cannot be cancelled")
	// ErrAsnTerminal is returned when changing an ASN that is Closed or
	// Cancelled.
	ErrAsnTerminal = errors.New("asn is closed or cancelled")
	// ErrAsnNotReceiving is returned when completing an ASN that is not
	// being received.
	ErrAsnNotReceiving = errors.New("asn is not being received")
)

// Number identifies an ASN. It is the aggregate identity.
type Number string

// NewNumber validates an ASN number.
func NewNumber(value string) (Number, error) {
	if value == "" || len(value) > MaxNumberLength {
		return "", ErrInvalidNumber
	}
	for i := 0; i < len(value); i++ {
		if !isNumberByte(value[i]) {
			return "", ErrInvalidNumber
		}
	}
	return Number(value), nil
}

func isNumberByte(c byte) bool {
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

// String returns the ASN number text.
func (n Number) String() string { return string(n) }

// State is the ASN lifecycle state.
type State string

// The ASN lifecycle: Registered -> Receiving -> Closed, or Registered ->
// Cancelled. Cancelled and Closed are terminal.
const (
	Registered State = "Registered"
	Receiving  State = "Receiving"
	Closed     State = "Closed"
	Cancelled  State = "Cancelled"
)

func (s State) valid() bool {
	return s == Registered || s == Receiving || s == Closed || s == Cancelled
}

// Receivable reports whether a receipt may be opened against an ASN in this
// state.
func (s State) Receivable() bool { return s == Registered || s == Receiving }

// LineInput is the unvalidated shape of an ASN line, as it arrives from a
// request or a repository.
type LineInput struct {
	LineNo      int
	SKU         string
	ExpectedQty int64
}

// Line is one validated ASN line.
type Line struct {
	lineNo      int
	sku         shared.SKU
	expectedQty int64
}

// LineNo returns the 1-based line number.
func (l Line) LineNo() int { return l.lineNo }

// SKU returns the expected SKU.
func (l Line) SKU() shared.SKU { return l.sku }

// ExpectedQty returns the announced quantity.
func (l Line) ExpectedQty() int64 { return l.expectedQty }

// Asn is the aggregate root.
//
// Invariants: the number is valid and never changes; the supplier reference
// is non-blank; there is at least one line; line numbers are exactly 1..n in
// order; SKUs are unique across lines and every expected quantity is
// positive; Version starts at 1 and increases by one per accepted change.
// Only a Registered ASN can be cancelled.
type Asn struct {
	number          Number
	supplierRef     string
	expectedArrival time.Time
	lines           []Line
	state           State
	version         int64
}

// Register creates an ASN at version 1 in state Registered and raises
// ASNRegistered. A zero expectedArrival means the supplier gave none.
func Register(number, supplierRef string, expectedArrival time.Time, lines []LineInput, now time.Time) (*Asn, []Event, error) {
	a, err := build(number, supplierRef, expectedArrival, lines, Registered, 1)
	if err != nil {
		return nil, nil, err
	}
	registered := ASNRegistered{
		Header:          a.header(now),
		SupplierRef:     a.supplierRef,
		ExpectedArrival: a.expectedArrival,
		Lines:           a.Lines(),
	}
	return a, []Event{registered}, nil
}

// Rehydrate rebuilds a persisted ASN, re-validating every invariant.
func Rehydrate(number, supplierRef string, expectedArrival time.Time, lines []LineInput, state State, version int64) (*Asn, error) {
	if !state.valid() {
		return nil, ErrInvalidState
	}
	if version < 1 {
		return nil, ErrInvalidVersion
	}
	return build(number, supplierRef, expectedArrival, lines, state, version)
}

func build(number, supplierRef string, expectedArrival time.Time, inputs []LineInput, state State, version int64) (*Asn, error) {
	n, err := NewNumber(number)
	if err != nil {
		return nil, err
	}
	if err := validateSupplierRef(supplierRef); err != nil {
		return nil, err
	}
	lines, err := buildLines(inputs)
	if err != nil {
		return nil, err
	}
	return &Asn{
		number:          n,
		supplierRef:     supplierRef,
		expectedArrival: expectedArrival.UTC(),
		lines:           lines,
		state:           state,
		version:         version,
	}, nil
}

func validateSupplierRef(ref string) error {
	if strings.TrimSpace(ref) == "" || utf8.RuneCountInString(ref) > MaxSupplierRefLength {
		return ErrInvalidSupplierRef
	}
	if strings.IndexFunc(ref, unicode.IsControl) >= 0 {
		return ErrInvalidSupplierRef
	}
	return nil
}

func buildLines(inputs []LineInput) ([]Line, error) {
	if len(inputs) == 0 {
		return nil, ErrNoLines
	}
	seen := make(map[shared.SKU]struct{}, len(inputs))
	lines := make([]Line, 0, len(inputs))
	for i, in := range inputs {
		if in.LineNo != i+1 {
			return nil, ErrInvalidLineNo
		}
		sku, err := shared.NewSKU(in.SKU)
		if err != nil {
			return nil, err
		}
		if err := shared.ValidateQuantity(in.ExpectedQty); err != nil {
			return nil, err
		}
		if _, dup := seen[sku]; dup {
			return nil, ErrDuplicateSKU
		}
		seen[sku] = struct{}{}
		lines = append(lines, Line{lineNo: in.LineNo, sku: sku, expectedQty: in.ExpectedQty})
	}
	return lines, nil
}

// Number returns the identity.
func (a *Asn) Number() Number { return a.number }

// SupplierRef returns the supplier reference.
func (a *Asn) SupplierRef() string { return a.supplierRef }

// ExpectedArrival returns the announced arrival, or the zero time when none
// was given.
func (a *Asn) ExpectedArrival() time.Time { return a.expectedArrival }

// Lines returns a copy of the lines in line-number order.
func (a *Asn) Lines() []Line { return append([]Line(nil), a.lines...) }

// State returns the lifecycle state.
func (a *Asn) State() State { return a.state }

// Version returns the current version.
func (a *Asn) Version() int64 { return a.version }

// Snapshot is the read-only copy of an ASN that a Receipt is opened against.
type Snapshot struct {
	Number Number
	State  State
	Lines  []Line
}

// Snapshot returns the ASN as the Receipt aggregate sees it.
func (a *Asn) Snapshot() Snapshot {
	return Snapshot{Number: a.number, State: a.state, Lines: a.Lines()}
}

// Cancel cancels a Registered ASN and raises ASNCancelled. An ASN that is
// already being received is ErrAsnInProgress; a Closed or Cancelled one is
// ErrAsnTerminal. The reason is optional.
func (a *Asn) Cancel(reason string, now time.Time) ([]Event, error) {
	if a.state == Receiving {
		return nil, ErrAsnInProgress
	}
	if a.state != Registered {
		return nil, ErrAsnTerminal
	}
	if err := shared.ValidateReason(reason); err != nil {
		return nil, err
	}
	a.state = Cancelled
	a.version++
	return []Event{ASNCancelled{Header: a.header(now), Reason: reason}}, nil
}

// BeginReceiving moves a Registered ASN to Receiving. It is an internal
// transition driven by the receipt use case and raises no event. An ASN that
// is already Receiving is left as is; a Closed or Cancelled one is
// ErrAsnTerminal.
func (a *Asn) BeginReceiving() error {
	if a.state == Receiving {
		return nil
	}
	if a.state != Registered {
		return ErrAsnTerminal
	}
	a.state = Receiving
	a.version++
	return nil
}

// Complete moves a Receiving ASN to Closed when its receipt closes. It is an
// internal transition and raises no event.
func (a *Asn) Complete() error {
	if a.state != Receiving {
		return ErrAsnNotReceiving
	}
	a.state = Closed
	a.version++
	return nil
}

func (a *Asn) header(now time.Time) Header {
	return Header{Number: a.number, At: now.UTC()}
}
