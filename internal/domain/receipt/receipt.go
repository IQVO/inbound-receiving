// Package receipt holds the Receipt aggregate: the physical receiving of one
// ASN's goods at the dock (ADR 0002). It is opened against a snapshot of the
// ASN, counts what actually arrives per line (Good and Damaged separately)
// and, on close, reports the discrepancies against what was announced.
//
// The Receipt never changes the ASN or the appointment itself: those are
// separate aggregates driven by the same use case, one transaction each.
// "At most one open receipt per ASN" spans aggregates, so it is enforced by
// the application and a partial unique index (ADR 0002), not here.
package receipt

import (
	"errors"
	"regexp"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

var (
	// ErrInvalidID is returned when a receipt id is not `rcpt-<uuid>`.
	ErrInvalidID = errors.New("receipt id must be rcpt-<uuid>")
	// ErrAsnNotReceivable is returned when opening a receipt against an ASN
	// that is not Registered or Receiving.
	ErrAsnNotReceivable = errors.New("asn is not receivable: it must be Registered or Receiving")
	// ErrReceiptClosed is returned when receiving into or closing a Closed
	// receipt.
	ErrReceiptClosed = errors.New("receipt is closed")
	// ErrLineNotOnAsn is returned when receiving against a line number the
	// ASN does not have.
	ErrLineNotOnAsn = errors.New("line is not on the asn")
	// ErrInvalidCondition is returned for a condition other than Good or
	// Damaged.
	ErrInvalidCondition = errors.New("condition must be Good or Damaged")
	// ErrInvalidReceived is returned when persisted received quantities are
	// negative or beyond the quantity bound.
	ErrInvalidReceived = errors.New("received quantities must be between 0 and 2147483647 per line")
	// ErrInvalidState is returned when a persisted state is not a known one.
	ErrInvalidState = errors.New("unknown receipt state")
	// ErrInvalidVersion is returned when a persisted version is below 1.
	ErrInvalidVersion = errors.New("receipt version must be at least 1")
	// ErrInvalidTimestamps is returned when opened_at is unset, or closed_at
	// is set exactly when the receipt is not Closed.
	ErrInvalidTimestamps = errors.New("receipt opened_at must be set and closed_at set if and only if the receipt is closed")
)

var idPattern = regexp.MustCompile(`^rcpt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ID identifies a receipt: `rcpt-` followed by a lowercase UUID.
type ID string

// NewID validates a receipt id.
func NewID(value string) (ID, error) {
	if !idPattern.MatchString(value) {
		return "", ErrInvalidID
	}
	return ID(value), nil
}

// String returns the id text.
func (id ID) String() string { return string(id) }

// State is the receipt lifecycle state.
type State string

// The lifecycle: Open -> Closed.
const (
	StateOpen   State = "Open"
	StateClosed State = "Closed"
)

func (s State) valid() bool { return s == StateOpen || s == StateClosed }

// Condition says whether received units are fit to put into stock.
type Condition string

// The two conditions a unit can be received in. Only Good units are handed
// over to inventory-storage (ADR 0003).
const (
	ConditionGood    Condition = "Good"
	ConditionDamaged Condition = "Damaged"
)

func (c Condition) valid() bool { return c == ConditionGood || c == ConditionDamaged }

// ParseCondition validates a condition text.
func ParseCondition(value string) (Condition, error) {
	c := Condition(value)
	if !c.valid() {
		return "", ErrInvalidCondition
	}
	return c, nil
}

// Kind is the kind of a discrepancy found on close.
type Kind string

// The discrepancy kinds. A line can carry several at once (for example
// Short and Damaged).
const (
	// KindShort: good + damaged received is below the expected quantity.
	KindShort Kind = "Short"
	// KindOver: good + damaged received is above the expected quantity.
	KindOver Kind = "Over"
	// KindDamaged: at least one damaged unit was received.
	KindDamaged Kind = "Damaged"
)

// LineState is the unvalidated shape of a receipt line, as it arrives from a
// repository.
type LineState struct {
	LineNo          int
	SKU             string
	ExpectedQty     int64
	ReceivedGood    int64
	ReceivedDamaged int64
}

// Line is one receipt line: what the ASN announced and what arrived.
type Line struct {
	lineNo          int
	sku             shared.SKU
	expectedQty     int64
	receivedGood    int64
	receivedDamaged int64
}

// LineNo returns the 1-based line number, equal to the ASN's.
func (l Line) LineNo() int { return l.lineNo }

// SKU returns the expected SKU.
func (l Line) SKU() shared.SKU { return l.sku }

// ExpectedQty returns the announced quantity.
func (l Line) ExpectedQty() int64 { return l.expectedQty }

// ReceivedGood returns the units received in Good condition.
func (l Line) ReceivedGood() int64 { return l.receivedGood }

// ReceivedDamaged returns the units received Damaged.
func (l Line) ReceivedDamaged() int64 { return l.receivedDamaged }

// Received returns good + damaged.
func (l Line) Received() int64 { return l.receivedGood + l.receivedDamaged }

// Discrepancy is one difference between a line's announced and received
// quantities, reported when the receipt closes.
type Discrepancy struct {
	LineNo      int
	SKU         shared.SKU
	Kind        Kind
	ExpectedQty int64
	ReceivedQty int64
	DamagedQty  int64
}

// Persisted is the stored shape of a receipt, used to rebuild one.
// AppointmentID and DoorCode are empty when the receipt was opened without
// them; ClosedAt is the zero time while the receipt is Open.
type Persisted struct {
	ID            string
	AsnNumber     string
	AppointmentID string
	DoorCode      string
	State         State
	Lines         []LineState
	OpenedAt      time.Time
	ClosedAt      time.Time
	Version       int64
}

// Receipt is the aggregate root.
//
// Invariants: it is opened only against a Registered or Receiving ASN; its
// lines mirror the ASN's (1..n in order, unique SKUs, positive expected
// quantities) and never change shape; received quantities only grow, each
// line's total stays within the quantity bound and only an Open receipt can
// receive; over-receipt is allowed and surfaces as a discrepancy on close; a
// Closed receipt never changes; Version starts at 1 and increases by one per
// accepted change.
type Receipt struct {
	id            ID
	asnNumber     asn.Number
	appointmentID appointment.ID
	doorCode      appointment.DoorCode
	state         State
	lines         []Line
	openedAt      time.Time
	closedAt      time.Time
	version       int64
}

// Open starts receiving the ASN described by snapshot, at version 1 in state
// Open, and raises ReceiptOpened. appointmentID and doorCode are optional
// (empty when absent): a walk-in delivery has no appointment.
func Open(id string, snapshot asn.Snapshot, appointmentID, doorCode string, now time.Time) (*Receipt, []Event, error) {
	if !snapshot.State.Receivable() {
		return nil, nil, ErrAsnNotReceivable
	}
	lines := make([]LineState, 0, len(snapshot.Lines))
	for _, l := range snapshot.Lines {
		lines = append(lines, LineState{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	r, err := build(Persisted{
		ID:            id,
		AsnNumber:     string(snapshot.Number),
		AppointmentID: appointmentID,
		DoorCode:      doorCode,
		State:         StateOpen,
		Lines:         lines,
		OpenedAt:      now,
		Version:       1,
	})
	if err != nil {
		return nil, nil, err
	}
	opened := ReceiptOpened{Header: r.header(now), AppointmentID: r.appointmentID, DoorCode: r.doorCode}
	return r, []Event{opened}, nil
}

// Rehydrate rebuilds a persisted receipt, re-validating every invariant.
func Rehydrate(p Persisted) (*Receipt, error) {
	if !p.State.valid() {
		return nil, ErrInvalidState
	}
	if p.Version < 1 {
		return nil, ErrInvalidVersion
	}
	return build(p)
}

func build(p Persisted) (*Receipt, error) {
	id, err := NewID(p.ID)
	if err != nil {
		return nil, err
	}
	number, err := asn.NewNumber(p.AsnNumber)
	if err != nil {
		return nil, err
	}
	appointmentID, doorCode, err := buildOrigin(p.AppointmentID, p.DoorCode)
	if err != nil {
		return nil, err
	}
	if err := validateTimestamps(p); err != nil {
		return nil, err
	}
	lines, err := buildLines(p.Lines)
	if err != nil {
		return nil, err
	}
	return &Receipt{
		id:            id,
		asnNumber:     number,
		appointmentID: appointmentID,
		doorCode:      doorCode,
		state:         p.State,
		lines:         lines,
		openedAt:      p.OpenedAt.UTC(),
		closedAt:      p.ClosedAt.UTC(),
		version:       p.Version,
	}, nil
}

func buildOrigin(appointmentID, doorCode string) (appointment.ID, appointment.DoorCode, error) {
	var id appointment.ID
	var door appointment.DoorCode
	var err error
	if appointmentID != "" {
		if id, err = appointment.NewID(appointmentID); err != nil {
			return "", "", err
		}
	}
	if doorCode != "" {
		if door, err = appointment.NewDoorCode(doorCode); err != nil {
			return "", "", err
		}
	}
	return id, door, nil
}

func validateTimestamps(p Persisted) error {
	if p.OpenedAt.IsZero() {
		return ErrInvalidTimestamps
	}
	if (p.State == StateClosed) == p.ClosedAt.IsZero() {
		return ErrInvalidTimestamps
	}
	return nil
}

func buildLines(states []LineState) ([]Line, error) {
	if len(states) == 0 {
		return nil, asn.ErrNoLines
	}
	seen := make(map[shared.SKU]struct{}, len(states))
	lines := make([]Line, 0, len(states))
	for i, s := range states {
		if s.LineNo != i+1 {
			return nil, asn.ErrInvalidLineNo
		}
		sku, err := shared.NewSKU(s.SKU)
		if err != nil {
			return nil, err
		}
		if err := shared.ValidateQuantity(s.ExpectedQty); err != nil {
			return nil, err
		}
		if err := validateReceived(s.ReceivedGood, s.ReceivedDamaged); err != nil {
			return nil, err
		}
		if _, dup := seen[sku]; dup {
			return nil, asn.ErrDuplicateSKU
		}
		seen[sku] = struct{}{}
		lines = append(lines, Line{
			lineNo:          s.LineNo,
			sku:             sku,
			expectedQty:     s.ExpectedQty,
			receivedGood:    s.ReceivedGood,
			receivedDamaged: s.ReceivedDamaged,
		})
	}
	return lines, nil
}

func validateReceived(good, damaged int64) error {
	if good < 0 || damaged < 0 {
		return ErrInvalidReceived
	}
	if good > shared.MaxQuantity || damaged > shared.MaxQuantity || good+damaged > shared.MaxQuantity {
		return ErrInvalidReceived
	}
	return nil
}

// ID returns the identity.
func (r *Receipt) ID() ID { return r.id }

// AsnNumber returns the ASN being received.
func (r *Receipt) AsnNumber() asn.Number { return r.asnNumber }

// AppointmentID returns the appointment the receipt was opened from, or ""
// for a walk-in delivery.
func (r *Receipt) AppointmentID() appointment.ID { return r.appointmentID }

// DoorCode returns the dock door, or "" when none was given.
func (r *Receipt) DoorCode() appointment.DoorCode { return r.doorCode }

// State returns the lifecycle state.
func (r *Receipt) State() State { return r.state }

// Lines returns a copy of the lines in line-number order.
func (r *Receipt) Lines() []Line { return append([]Line(nil), r.lines...) }

// OpenedAt returns when the receipt was opened (UTC).
func (r *Receipt) OpenedAt() time.Time { return r.openedAt }

// ClosedAt returns when the receipt was closed (UTC), or the zero time
// while it is Open.
func (r *Receipt) ClosedAt() time.Time { return r.closedAt }

// Version returns the current version.
func (r *Receipt) Version() int64 { return r.version }

// ReceiveLine records qty units of the line in the given condition and
// raises ReceiptLineReceived. Over-receipt is accepted; it shows as an Over
// discrepancy on close.
func (r *Receipt) ReceiveLine(lineNo int, qty int64, condition Condition, at time.Time) ([]Event, error) {
	if r.state != StateOpen {
		return nil, ErrReceiptClosed
	}
	if lineNo < 1 || lineNo > len(r.lines) {
		return nil, ErrLineNotOnAsn
	}
	if err := shared.ValidateQuantity(qty); err != nil {
		return nil, err
	}
	if !condition.valid() {
		return nil, ErrInvalidCondition
	}
	line := &r.lines[lineNo-1]
	if line.Received()+qty > shared.MaxQuantity {
		return nil, shared.ErrInvalidQuantity
	}
	if condition == ConditionGood {
		line.receivedGood += qty
	} else {
		line.receivedDamaged += qty
	}
	r.version++
	received := ReceiptLineReceived{
		Header:    r.header(at),
		LineNo:    line.lineNo,
		SKU:       line.sku,
		Quantity:  qty,
		Condition: condition,
	}
	return []Event{received}, nil
}

// Close ends receiving and raises ReceiptClosed carrying the discrepancies.
// A line nothing was received for is Short; closing a receipt with no unit
// received at all is allowed.
func (r *Receipt) Close(at time.Time) ([]Event, error) {
	if r.state != StateOpen {
		return nil, ErrReceiptClosed
	}
	r.state = StateClosed
	r.closedAt = at.UTC()
	r.version++
	closed := ReceiptClosed{Header: r.header(at), Discrepancies: r.Discrepancies()}
	return []Event{closed}, nil
}

// Discrepancies computes the differences between announced and received
// quantities as they stand now: per line, in line order, one entry per kind
// in the order Short, Over, Damaged. It is never nil.
func (r *Receipt) Discrepancies() []Discrepancy {
	out := make([]Discrepancy, 0)
	for _, l := range r.lines {
		received := l.Received()
		if received < l.expectedQty {
			out = append(out, l.discrepancy(KindShort))
		}
		if received > l.expectedQty {
			out = append(out, l.discrepancy(KindOver))
		}
		if l.receivedDamaged > 0 {
			out = append(out, l.discrepancy(KindDamaged))
		}
	}
	return out
}

func (l Line) discrepancy(kind Kind) Discrepancy {
	return Discrepancy{
		LineNo:      l.lineNo,
		SKU:         l.sku,
		Kind:        kind,
		ExpectedQty: l.expectedQty,
		ReceivedQty: l.Received(),
		DamagedQty:  l.receivedDamaged,
	}
}

func (r *Receipt) header(at time.Time) Header {
	return Header{ID: r.id, Asn: r.asnNumber, At: at.UTC()}
}
