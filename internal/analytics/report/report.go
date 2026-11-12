// Package report is the self-contained read-model region of the analytics
// read side (ADR 0006): the shape of the receiving performance report, the
// parameter rules (date range, day windows), the pure percentile and
// freshness logic, the validation of a projectable event, and the
// writer/reader ports the analytical store implements. It imports no other
// internal package, so the OLTP domain can never leak into the projection and
// the arch test keeps this region isolated.
package report

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is which of the twelve projected events a fact came from. The names are
// the CloudEvents event names (the last segment of `type`).
type Kind string

// The twelve events the projection folds; the analytics topic carries more (the
// advice overdue alarm is ignored).
const (
	KindAsnRegistered Kind = "ASNRegistered"
	KindAsnCancelled  Kind = "ASNCancelled"
	// KindAsnLateForArrival is the ASN-timeliness fact raised at OpenReceipt;
	// the KPI "% ASNs received before arrival, per supplier" folds it (ADR 0025).
	KindAsnLateForArrival    Kind = "AsnLateForArrival"
	KindAppointmentBooked    Kind = "DockAppointmentBooked"
	KindAppointmentCheckedIn Kind = "DockAppointmentCheckedIn"
	KindAppointmentCancelled Kind = "DockAppointmentCancelled"
	KindAppointmentCompleted Kind = "DockAppointmentCompleted"
	KindReceiptOpened        Kind = "ReceiptOpened"
	KindReceiptLineReceived  Kind = "ReceiptLineReceived"
	KindReceiptClosed        Kind = "ReceiptClosed"
	// KindReceiptStowCompleted is the dock-to-stock fact (ADR 0021).
	KindReceiptStowCompleted Kind = "ReceiptStowCompleted"
	// KindReceivingAdviceIssued is the receiving advice issued at close; the
	// KPI "% receipts with advice inside the SLA" folds it (ADR 0023).
	KindReceivingAdviceIssued Kind = "ReceivingAdviceIssued"
)

// Kinds returns every projected kind, in catalogue order.
func Kinds() []Kind {
	return []Kind{
		KindAsnRegistered, KindAsnCancelled, KindAsnLateForArrival,
		KindAppointmentBooked, KindAppointmentCheckedIn, KindAppointmentCancelled, KindAppointmentCompleted,
		KindReceiptOpened, KindReceiptLineReceived, KindReceiptClosed, KindReceiptStowCompleted, KindReceivingAdviceIssued,
	}
}

// IsAsn reports whether k is an ASN event.
func (k Kind) IsAsn() bool {
	return k == KindAsnRegistered || k == KindAsnCancelled || k == KindAsnLateForArrival
}

// IsAppointment reports whether k is a dock appointment event.
func (k Kind) IsAppointment() bool {
	return k == KindAppointmentBooked || k == KindAppointmentCheckedIn ||
		k == KindAppointmentCancelled || k == KindAppointmentCompleted
}

// IsReceipt reports whether k is a receipt event.
func (k Kind) IsReceipt() bool {
	return k == KindReceiptOpened || k == KindReceiptLineReceived || k == KindReceiptClosed || k == KindReceiptStowCompleted ||
		k == KindReceivingAdviceIssued
}

// Condition is the condition of a received quantity.
type Condition string

// The two conditions of a received quantity.
const (
	ConditionGood    Condition = "Good"
	ConditionDamaged Condition = "Damaged"
)

// Valid reports whether c is a known condition.
func (c Condition) Valid() bool { return c == ConditionGood || c == ConditionDamaged }

// DiscrepancyKind is the kind of a receiving discrepancy.
type DiscrepancyKind string

// The three discrepancy kinds a receipt closes with.
const (
	DiscrepancyShort   DiscrepancyKind = "Short"
	DiscrepancyOver    DiscrepancyKind = "Over"
	DiscrepancyDamaged DiscrepancyKind = "Damaged"
)

// Valid reports whether k is a known discrepancy kind.
func (k DiscrepancyKind) Valid() bool {
	return k == DiscrepancyShort || k == DiscrepancyOver || k == DiscrepancyDamaged
}

// LineReceived is the part of a ReceiptLineReceived the report reads.
type LineReceived struct {
	LineNo    int
	SKU       string
	Quantity  int64
	Condition Condition
}

// Discrepancy is one discrepancy a receipt closed with.
type Discrepancy struct {
	LineNo int
	SKU    string
	Kind   DiscrepancyKind
}

// StowCompleted is the part of a ReceiptStowCompleted the report reads: the
// gate arrival, the stow of the last Good carton, the seconds between them and
// how many Good cartons the receipt had (ADR 0021).
type StowCompleted struct {
	ArrivedAt          time.Time
	StowedAt           time.Time
	DockToStockSeconds int64
	CartonCount        int
}

// AdviceIssued is the part of a ReceivingAdviceIssued the report reads: whether
// the advice was inside the SLA (ADR 0023).
type AdviceIssued struct {
	WithinSLA bool
}

// Event is one decoded analytics event ready to project. Only the fields of
// its Kind are set.
type Event struct {
	Kind    Kind
	EventID string    // CloudEvents id: the idempotency key
	At      time.Time // CloudEvents time: when the event occurred

	// AsnNumber is set on the ASN events and on every receipt event.
	AsnNumber string
	// ExpectedArrival is set on an ASNRegistered that carries one.
	ExpectedArrival *time.Time
	// AppointmentID is set on the appointment events.
	AppointmentID string
	// ReceiptID is set on the receipt events.
	ReceiptID string
	// Line is set exactly on a ReceiptLineReceived.
	Line *LineReceived
	// Discrepancies is set on a ReceiptClosed (possibly empty).
	Discrepancies []Discrepancy
	// Stow is set exactly on a ReceiptStowCompleted.
	Stow *StowCompleted
	// Advice is set exactly on a ReceivingAdviceIssued.
	Advice *AdviceIssued

	// SupplierRef is set on the ASN events that carry one (ASNRegistered,
	// AsnLateForArrival, ADR 0025).
	SupplierRef string
	// VisitID, AsnRegisteredAt, ArrivedAt and LatenessSeconds are set exactly
	// on an AsnLateForArrival (ADR 0025).
	VisitID         string
	AsnRegisteredAt time.Time
	ArrivedAt       time.Time
	LatenessSeconds int64
}

// ErrInvalidEvent marks an event that can never be projected (the consumer
// dead-letters it).
var ErrInvalidEvent = errors.New("report: event cannot be projected")

// Validate checks the invariants every store relies on. Every failure wraps
// ErrInvalidEvent and is deterministic.
func (e Event) Validate() error {
	switch {
	case !e.Kind.known():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case e.EventID == "":
		return fmt.Errorf("%w: %s without an id", ErrInvalidEvent, e.Kind)
	case e.At.IsZero():
		return fmt.Errorf("%w: %s %s without a time", ErrInvalidEvent, e.Kind, e.EventID)
	}
	switch {
	case e.Kind.IsAsn() && e.AsnNumber == "":
		return fmt.Errorf("%w: %s %s without an asn_number", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind.IsAppointment() && e.AppointmentID == "":
		return fmt.Errorf("%w: %s %s without an appointment_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind.IsReceipt() && (e.ReceiptID == "" || e.AsnNumber == ""):
		return fmt.Errorf("%w: %s %s without a receipt_id and asn_number", ErrInvalidEvent, e.Kind, e.EventID)
	}
	return e.validateDetail()
}

func (e Event) validateDetail() error {
	if what := e.strayDetail(); what != "" {
		return fmt.Errorf("%w: %s %s carries %s", ErrInvalidEvent, e.Kind, e.EventID, what)
	}
	switch e.Kind {
	case KindReceiptLineReceived:
		l := e.Line
		switch {
		case l == nil:
			return fmt.Errorf("%w: %s %s without a line", ErrInvalidEvent, e.Kind, e.EventID)
		case l.LineNo < 1:
			return fmt.Errorf("%w: %s %s line_no %d is below 1", ErrInvalidEvent, e.Kind, e.EventID, l.LineNo)
		case l.Quantity < 1:
			return fmt.Errorf("%w: %s %s quantity %d is below 1", ErrInvalidEvent, e.Kind, e.EventID, l.Quantity)
		case !l.Condition.Valid():
			return fmt.Errorf("%w: %s %s unknown condition %q", ErrInvalidEvent, e.Kind, e.EventID, l.Condition)
		}
	case KindReceiptStowCompleted:
		return e.validateStow()
	case KindReceivingAdviceIssued:
		if e.Advice == nil {
			return fmt.Errorf("%w: %s %s without an advice", ErrInvalidEvent, e.Kind, e.EventID)
		}
	case KindReceiptClosed:
		for _, d := range e.Discrepancies {
			if d.LineNo < 1 || !d.Kind.Valid() {
				return fmt.Errorf("%w: %s %s discrepancy line %d kind %q", ErrInvalidEvent, e.Kind, e.EventID, d.LineNo, d.Kind)
			}
		}
	case KindAsnLateForArrival:
		return e.validateAsnLate()
	}
	return nil
}

// validateAsnLate checks the late fact's own fields: a supplier, an
// appointment and visit, both timestamps and a strictly positive lateness (an
// equal or earlier registration is on time and never raises the event).
func (e Event) validateAsnLate() error {
	switch {
	case e.SupplierRef == "":
		return fmt.Errorf("%w: %s %s without a supplier_ref", ErrInvalidEvent, e.Kind, e.EventID)
	case e.AppointmentID == "":
		return fmt.Errorf("%w: %s %s without an appointment_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.VisitID == "":
		return fmt.Errorf("%w: %s %s without a visit_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.AsnRegisteredAt.IsZero() || e.ArrivedAt.IsZero():
		return fmt.Errorf("%w: %s %s without asn_registered_at and arrived_at", ErrInvalidEvent, e.Kind, e.EventID)
	case e.LatenessSeconds < 1:
		return fmt.Errorf("%w: %s %s lateness_seconds %d is below 1", ErrInvalidEvent, e.Kind, e.EventID, e.LatenessSeconds)
	}
	return nil
}

// strayDetail names the detail an event carries that its Kind never does.
func (e Event) strayDetail() string {
	switch {
	case e.Kind != KindReceiptLineReceived && e.Line != nil:
		return "a received line"
	case e.Kind != KindReceiptStowCompleted && e.Stow != nil:
		return "a stow"
	case e.Kind != KindReceivingAdviceIssued && e.Advice != nil:
		return "an advice"
	case e.Kind != KindReceiptClosed && len(e.Discrepancies) > 0:
		return "discrepancies"
	}
	return ""
}

func (e Event) validateStow() error {
	s := e.Stow
	switch {
	case s == nil:
		return fmt.Errorf("%w: %s %s without a stow", ErrInvalidEvent, e.Kind, e.EventID)
	case s.ArrivedAt.IsZero() || s.StowedAt.IsZero():
		return fmt.Errorf("%w: %s %s without arrived_at and stowed_at", ErrInvalidEvent, e.Kind, e.EventID)
	case s.DockToStockSeconds < 0:
		return fmt.Errorf("%w: %s %s dock_to_stock_seconds %d is negative", ErrInvalidEvent, e.Kind, e.EventID, s.DockToStockSeconds)
	case s.CartonCount < 1:
		return fmt.Errorf("%w: %s %s carton_count %d is below 1", ErrInvalidEvent, e.Kind, e.EventID, s.CartonCount)
	}
	return nil
}

func (k Kind) known() bool {
	for _, c := range Kinds() {
		if k == c {
			return true
		}
	}
	return false
}

// AppointmentTimes is the "this happened at" candidate an appointment event
// offers for each of the four appointment milestones (nil where it offers
// none). A store keeps the earliest candidate per appointment, so arrival
// order never matters.
type AppointmentTimes struct {
	Booked    *time.Time
	CheckedIn *time.Time
	Cancelled *time.Time
	Completed *time.Time
}

// AppointmentTimesOf returns the milestone an appointment event marks (none
// for any other kind).
func AppointmentTimesOf(e Event) AppointmentTimes {
	at := e.At.UTC()
	var t AppointmentTimes
	switch e.Kind {
	case KindAppointmentBooked:
		t.Booked = &at
	case KindAppointmentCheckedIn:
		t.CheckedIn = &at
	case KindAppointmentCancelled:
		t.Cancelled = &at
	case KindAppointmentCompleted:
		t.Completed = &at
	}
	return t
}

// ReceiptTimes is the opened/closed candidate a receipt event offers.
type ReceiptTimes struct {
	Opened *time.Time
	Closed *time.Time
}

// ReceiptTimesOf returns the milestone a receipt event marks.
func ReceiptTimesOf(e Event) ReceiptTimes {
	at := e.At.UTC()
	var t ReceiptTimes
	switch e.Kind {
	case KindReceiptOpened:
		t.Opened = &at
	case KindReceiptClosed:
		t.Closed = &at
	}
	return t
}

// Projection is the WRITER port: Apply records the event id and folds the
// event into the model in ONE transaction. applied is false when the id was
// already recorded (a replay): nothing changed. An error wrapping
// ErrRejected is deterministic (the store can never accept this event); any
// other error is transient and the same event may be retried.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}

// ErrRejected marks an event the analytical store deterministically refuses
// (a data-exception or integrity-violation class error).
var ErrRejected = errors.New("report: event rejected by the analytical store")

// Reader is the READER port.
type Reader interface {
	// PerformanceDays returns one row per DayWindow of r, in day order, zeros
	// included.
	PerformanceDays(ctx context.Context, r Range) ([]PerformanceDay, error)
	// Current counts the open receipts and the arrivals expected inside the
	// window [from, to) (today).
	Current(ctx context.Context, today DayWindow) (CurrentCounts, error)
	// LastEventAt is the CloudEvents time of the newest event the projection
	// has applied (nil while it has applied none): the freshness basis.
	LastEventAt(ctx context.Context) (*time.Time, error)
}

// DiscrepancyCounts counts the discrepancies receipts closed with, by kind.
type DiscrepancyCounts struct {
	Short   int
	Over    int
	Damaged int
}

// AppointmentCounts counts the appointment milestones of a day.
type AppointmentCounts struct {
	Booked    int
	CheckedIn int
	Cancelled int
	Completed int
}

// PerformanceDay is one UTC day of the receiving performance report, counted
// over the day's window (the part of the day inside the requested range).
type PerformanceDay struct {
	// Day is the UTC calendar day, at midnight UTC.
	Day time.Time
	// ReceiptsClosed counts the receipts whose ReceiptClosed fell in the
	// window.
	ReceiptsClosed int
	// LinesReceived counts the ReceiptLineReceived events (receiving actions
	// against a line) in the window; UnitsGood and UnitsDamaged sum their
	// quantities by condition.
	LinesReceived int
	UnitsGood     int64
	UnitsDamaged  int64
	// Discrepancies counts, by kind, the discrepancies of the receipts closed
	// in the window.
	Discrepancies DiscrepancyCounts
	// ReceiptCycle is the open-to-close time of the receipts closed in the
	// window that the projection also saw opened.
	ReceiptCycle Timing
	// Appointments counts each appointment milestone that fell in the window.
	Appointments AppointmentCounts
	// DockDwell is the check-in-to-completed time of the appointments
	// completed in the window that the projection also saw checked in.
	DockDwell Timing
	// DockToStock is the gate-arrival-to-last-Good-carton-stowed time of the
	// receipts whose stow completed in the window (ADR 0021).
	DockToStock DockToStock
	// Advice is the receiving advices issued in the window and how many of them
	// were inside the SLA (ADR 0023).
	Advice AdviceSummary
	// AsnTimeliness is the per-supplier ASN-timeliness block of the day
	// (ADR 0025): the day's ASNs by registered_at day, how many were late,
	// and the on-time share, bounded by MaxTimelinessSuppliers.
	AsnTimeliness []SupplierTimeliness
}

// CurrentCounts is the state right now, independent of the report range.
type CurrentCounts struct {
	// OpenReceipts counts the receipts opened and not yet closed.
	OpenReceipts int
	// ExpectedToday counts the not-cancelled ASNs whose expected arrival
	// falls in today's window; ExpectedTodayStarted is those that already
	// have a receipt opened.
	ExpectedToday        int
	ExpectedTodayStarted int
}
