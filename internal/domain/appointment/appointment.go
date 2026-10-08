// Package appointment holds the DockAppointment aggregate: a carrier's
// booked time window at a dock door, covering one or more ASNs (ADR 0002).
// The aggregate guards its own lifecycle and window; the rule that two
// appointments must not overlap on one door spans aggregates and lives in
// the Schedule domain service.
package appointment

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

const (
	// MaxWindow is the longest bookable window: 4 hours. It is written in
	// nanoseconds (not `4 * time.Hour`) so the mutation gate has no
	// uncoverable constant arithmetic; a test pins the value.
	MaxWindow time.Duration = 14_400_000_000_000
	// CheckInLeadTime is how long before the window opens a carrier may
	// already check in: 30 minutes, in nanoseconds for the same reason.
	CheckInLeadTime time.Duration = 1_800_000_000_000
	// MaxDoorCodeLength is the longest door code, in characters.
	MaxDoorCodeLength = 64
	// MaxCarrierLength is the longest carrier name, in characters.
	MaxCarrierLength = 100
)

var (
	// ErrInvalidID is returned when an appointment id is not `appt-<uuid>`.
	ErrInvalidID = errors.New("appointment id must be appt-<uuid>")
	// ErrInvalidDoorCode is returned when a door code is empty, too long or
	// contains whitespace, a control character or '/'.
	ErrInvalidDoorCode = errors.New("door code must be 1..64 characters without whitespace, control characters or '/'")
	// ErrInvalidCarrier is returned when the carrier is blank, too long or
	// contains a control character.
	ErrInvalidCarrier = errors.New("carrier must be 1..100 non-blank characters without control characters")
	// ErrInvalidWindow is returned when the window does not end after it
	// starts or lasts longer than MaxWindow.
	ErrInvalidWindow = errors.New("window must end after it starts and last at most 4 hours")
	// ErrWindowInPast is returned when booking a window that starts before
	// the clock's now.
	ErrWindowInPast = errors.New("window must not start in the past")
	// ErrNoAsns is returned when an appointment covers no ASN.
	ErrNoAsns = errors.New("appointment must cover at least one asn")
	// ErrDuplicateAsn is returned when an ASN number is listed twice.
	ErrDuplicateAsn = errors.New("appointment asn numbers must be distinct")
	// ErrInvalidState is returned when a persisted state is not a known one.
	ErrInvalidState = errors.New("unknown appointment state")
	// ErrInvalidVersion is returned when a persisted version is below 1.
	ErrInvalidVersion = errors.New("appointment version must be at least 1")
	// ErrNotBooked is returned when checking in or cancelling an
	// appointment that is not Booked.
	ErrNotBooked = errors.New("appointment is not booked")
	// ErrNotCheckedIn is returned when completing an appointment that is not
	// CheckedIn.
	ErrNotCheckedIn = errors.New("appointment is not checked in")
	// ErrOutsideCheckInWindow is returned when checking in earlier than
	// CheckInLeadTime before the window or after it ended.
	ErrOutsideCheckInWindow = errors.New("check-in is only allowed from 30 minutes before the window until it ends")
	// ErrWindowOverlap is returned by Schedule when a door is already booked
	// in an overlapping window.
	ErrWindowOverlap = errors.New("door is already booked in an overlapping window")
)

var idPattern = regexp.MustCompile(`^appt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ID identifies a dock appointment: `appt-` followed by a lowercase UUID.
type ID string

// NewID validates an appointment id.
func NewID(value string) (ID, error) {
	if !idPattern.MatchString(value) {
		return "", ErrInvalidID
	}
	return ID(value), nil
}

// String returns the id text.
func (id ID) String() string { return string(id) }

// DoorCode is the code of the dock door (a facility-layout slot with role
// Dock) the carrier is booked at.
type DoorCode string

// NewDoorCode validates a door code.
func NewDoorCode(value string) (DoorCode, error) {
	if value == "" || utf8.RuneCountInString(value) > MaxDoorCodeLength {
		return "", ErrInvalidDoorCode
	}
	for _, r := range value {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrInvalidDoorCode
		}
	}
	return DoorCode(value), nil
}

// String returns the door code text.
func (d DoorCode) String() string { return string(d) }

// State is the appointment lifecycle state.
type State string

// The lifecycle: Booked -> CheckedIn -> Completed, or Booked -> Cancelled.
const (
	Booked    State = "Booked"
	CheckedIn State = "CheckedIn"
	Completed State = "Completed"
	Cancelled State = "Cancelled"
)

func (s State) valid() bool {
	return s == Booked || s == CheckedIn || s == Completed || s == Cancelled
}

// Active reports whether an appointment in this state still occupies its
// door (Booked or CheckedIn).
func (s State) Active() bool { return s == Booked || s == CheckedIn }

// Window is the half-open interval [start, end) a door is booked for.
type Window struct {
	start time.Time
	end   time.Time
}

// NewWindow validates a window and normalises it to UTC.
func NewWindow(start, end time.Time) (Window, error) {
	if !end.After(start) || end.Sub(start) > MaxWindow {
		return Window{}, ErrInvalidWindow
	}
	return Window{start: start.UTC(), end: end.UTC()}, nil
}

// Start returns the inclusive start.
func (w Window) Start() time.Time { return w.start }

// End returns the exclusive end.
func (w Window) End() time.Time { return w.end }

// Overlaps reports whether the two half-open windows share any instant.
func (w Window) Overlaps(other Window) bool {
	return w.start.Before(other.end) && other.start.Before(w.end)
}

// allowsCheckIn reports whether at lies in [start - CheckInLeadTime, end].
func (w Window) allowsCheckIn(at time.Time) bool {
	return !at.Before(w.start.Add(-CheckInLeadTime)) && !at.After(w.end)
}

// DockAppointment is the aggregate root.
//
// Invariants: the id and door code are valid; the carrier is non-blank; the
// window ends after it starts and lasts at most MaxWindow; a booking window
// does not start in the past; at least one ASN is covered and none twice;
// only a Booked appointment can be checked in or cancelled and only a
// CheckedIn one completed; check-in is allowed from CheckInLeadTime before
// the window starts until it ends; Version starts at 1 and increases by one
// per accepted change.
type DockAppointment struct {
	id         ID
	doorCode   DoorCode
	carrier    string
	window     Window
	asnNumbers []asn.Number
	state      State
	version    int64
}

// Book creates a Booked appointment at version 1 and raises
// DockAppointmentBooked. now is the injected clock: the window may start at
// now but not before it. Door availability is checked by Schedule.
func Book(id, doorCode, carrier string, start, end time.Time, asnNumbers []string, now time.Time) (*DockAppointment, []Event, error) {
	d, err := build(id, doorCode, carrier, start, end, asnNumbers, Booked, 1)
	if err != nil {
		return nil, nil, err
	}
	if d.window.start.Before(now) {
		return nil, nil, ErrWindowInPast
	}
	booked := DockAppointmentBooked{
		Header:     d.header(now),
		DoorCode:   d.doorCode,
		Carrier:    d.carrier,
		Window:     d.window,
		AsnNumbers: d.AsnNumbers(),
	}
	return d, []Event{booked}, nil
}

// Rehydrate rebuilds a persisted appointment, re-validating every invariant
// except that the window may be in the past.
func Rehydrate(id, doorCode, carrier string, start, end time.Time, asnNumbers []string, state State, version int64) (*DockAppointment, error) {
	if !state.valid() {
		return nil, ErrInvalidState
	}
	if version < 1 {
		return nil, ErrInvalidVersion
	}
	return build(id, doorCode, carrier, start, end, asnNumbers, state, version)
}

func build(id, doorCode, carrier string, start, end time.Time, asnNumbers []string, state State, version int64) (*DockAppointment, error) {
	appointmentID, err := NewID(id)
	if err != nil {
		return nil, err
	}
	door, err := NewDoorCode(doorCode)
	if err != nil {
		return nil, err
	}
	if err := validateCarrier(carrier); err != nil {
		return nil, err
	}
	window, err := NewWindow(start, end)
	if err != nil {
		return nil, err
	}
	numbers, err := buildAsnNumbers(asnNumbers)
	if err != nil {
		return nil, err
	}
	return &DockAppointment{
		id:         appointmentID,
		doorCode:   door,
		carrier:    carrier,
		window:     window,
		asnNumbers: numbers,
		state:      state,
		version:    version,
	}, nil
}

func validateCarrier(carrier string) error {
	if strings.TrimSpace(carrier) == "" || utf8.RuneCountInString(carrier) > MaxCarrierLength {
		return ErrInvalidCarrier
	}
	if strings.IndexFunc(carrier, unicode.IsControl) >= 0 {
		return ErrInvalidCarrier
	}
	return nil
}

func buildAsnNumbers(values []string) ([]asn.Number, error) {
	if len(values) == 0 {
		return nil, ErrNoAsns
	}
	seen := make(map[asn.Number]struct{}, len(values))
	numbers := make([]asn.Number, 0, len(values))
	for _, v := range values {
		n, err := asn.NewNumber(v)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[n]; dup {
			return nil, ErrDuplicateAsn
		}
		seen[n] = struct{}{}
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// ID returns the identity.
func (d *DockAppointment) ID() ID { return d.id }

// DoorCode returns the door the appointment is booked at.
func (d *DockAppointment) DoorCode() DoorCode { return d.doorCode }

// Carrier returns the carrier name.
func (d *DockAppointment) Carrier() string { return d.carrier }

// Window returns the booked window.
func (d *DockAppointment) Window() Window { return d.window }

// AsnNumbers returns a copy of the covered ASN numbers, in booking order.
func (d *DockAppointment) AsnNumbers() []asn.Number {
	return append([]asn.Number(nil), d.asnNumbers...)
}

// State returns the lifecycle state.
func (d *DockAppointment) State() State { return d.state }

// Version returns the current version.
func (d *DockAppointment) Version() int64 { return d.version }

// CheckIn records the carrier's arrival at the door and raises
// DockAppointmentCheckedIn. Only a Booked appointment can check in, and only
// from CheckInLeadTime before the window starts until it ends (inclusive).
func (d *DockAppointment) CheckIn(at time.Time) ([]Event, error) {
	if d.state != Booked {
		return nil, ErrNotBooked
	}
	if !d.window.allowsCheckIn(at) {
		return nil, ErrOutsideCheckInWindow
	}
	d.state = CheckedIn
	d.version++
	checkedIn := DockAppointmentCheckedIn{Header: d.header(at), DoorCode: d.doorCode, CheckedInAt: at.UTC()}
	return []Event{checkedIn}, nil
}

// Cancel cancels a Booked appointment and raises DockAppointmentCancelled.
// The reason is optional.
func (d *DockAppointment) Cancel(reason string, now time.Time) ([]Event, error) {
	if d.state != Booked {
		return nil, ErrNotBooked
	}
	if err := shared.ValidateReason(reason); err != nil {
		return nil, err
	}
	d.state = Cancelled
	d.version++
	cancelled := DockAppointmentCancelled{Header: d.header(now), DoorCode: d.doorCode, Reason: reason}
	return []Event{cancelled}, nil
}

// Complete finishes a CheckedIn appointment (its receipt closed) and raises
// DockAppointmentCompleted.
func (d *DockAppointment) Complete(at time.Time) ([]Event, error) {
	if d.state != CheckedIn {
		return nil, ErrNotCheckedIn
	}
	d.state = Completed
	d.version++
	completed := DockAppointmentCompleted{Header: d.header(at), DoorCode: d.doorCode, CompletedAt: at.UTC()}
	return []Event{completed}, nil
}

func (d *DockAppointment) header(at time.Time) Header {
	return Header{ID: d.id, At: at.UTC()}
}

// Schedule is the domain service guarding the cross-aggregate rule that no
// two appointments in Booked or CheckedIn may overlap on the same door.
type Schedule struct{}

// CheckNoOverlap returns ErrWindowOverlap when candidate's window overlaps
// the window of any other active appointment on its door. activeOnSameDoor
// is what the repository found for candidate's door; the candidate itself,
// appointments on another door and appointments that no longer occupy a door
// are ignored defensively.
func (Schedule) CheckNoOverlap(candidate *DockAppointment, activeOnSameDoor []*DockAppointment) error {
	for _, other := range activeOnSameDoor {
		if other.id == candidate.id {
			continue
		}
		if other.doorCode != candidate.doorCode {
			continue
		}
		if !other.state.Active() {
			continue
		}
		if candidate.window.Overlaps(other.window) {
			return ErrWindowOverlap
		}
	}
	return nil
}
