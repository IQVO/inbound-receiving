// Package appointment holds the DockAppointment: a carrier's booked time
// window at a dock door, covering one or more ASNs.
//
// Since ADR 0019 (the DockAppointment ownership transfer to dock-scheduling,
// plan YD-0002) this context no longer decides anything about appointments.
// The type is the read model of dock-scheduling's appointments: it is rebuilt
// from that context's events (and, for rows that predate the transfer, from
// the legacy aggregate's last state) and answers "does this appointment
// exist, which door, which ASNs, is it cancelled" for Receipt.Open. It has no
// commands and raises no event (ADR 0022 removed the legacy history,
// Replay).
package appointment

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

const (
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
	// starts. dock-scheduling owns the longest bookable window (a per-site
	// setting), so the copy imposes no upper bound.
	ErrInvalidWindow = errors.New("window must end after it starts")
	// ErrNoAsns is returned when an appointment covers no ASN.
	ErrNoAsns = errors.New("appointment must cover at least one asn")
	// ErrDuplicateAsn is returned when an ASN number is listed twice.
	ErrDuplicateAsn = errors.New("appointment asn numbers must be distinct")
	// ErrInvalidState is returned when a persisted state is not a known one.
	ErrInvalidState = errors.New("unknown appointment state")
	// ErrInvalidVersion is returned when a version is negative. Version 0 is a
	// row that predates the transfer: any dock-scheduling version wins over it.
	ErrInvalidVersion = errors.New("appointment version must not be negative")
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

// State is the appointment's lifecycle state as last seen here.
type State string

// The states of the legacy lifecycle (Booked -> CheckedIn -> Completed, or
// Booked -> Cancelled). A row fed by dock-scheduling is Booked or Cancelled:
// check-in, completion, no-show and reschedule are not consumed (ADR 0019).
const (
	Booked    State = "Booked"
	CheckedIn State = "CheckedIn"
	Completed State = "Completed"
	Cancelled State = "Cancelled"
)

func (s State) valid() bool {
	return s == Booked || s == CheckedIn || s == Completed || s == Cancelled
}

// Window is the half-open interval [start, end) a door is booked for.
type Window struct {
	start time.Time
	end   time.Time
}

// NewWindow validates a window and normalises it to UTC.
func NewWindow(start, end time.Time) (Window, error) {
	if !end.After(start) {
		return Window{}, ErrInvalidWindow
	}
	return Window{start: start.UTC(), end: end.UTC()}, nil
}

// Start returns the inclusive start.
func (w Window) Start() time.Time { return w.start }

// End returns the exclusive end.
func (w Window) End() time.Time { return w.end }

// DockAppointment is the local copy of one dock-scheduling appointment.
//
// Invariants: the id and door code are valid; the carrier is non-blank; the
// window ends after it starts; at least one ASN is covered and none twice;
// Version is the dock-scheduling aggregate version of the last event applied,
// 0 for a row that predates the transfer (any new version wins over it).
type DockAppointment struct {
	id         ID
	doorCode   DoorCode
	carrier    string
	window     Window
	asnNumbers []asn.Number
	state      State
	version    int64
}

// Rehydrate rebuilds a stored appointment, re-validating every invariant
// (the window may be in the past).
func Rehydrate(id, doorCode, carrier string, start, end time.Time, asnNumbers []string, state State, version int64) (*DockAppointment, error) {
	if !state.valid() {
		return nil, ErrInvalidState
	}
	if version < 0 {
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

// Covers reports whether the appointment lists the ASN.
func (d *DockAppointment) Covers(number asn.Number) bool {
	for _, n := range d.asnNumbers {
		if n == number {
			return true
		}
	}
	return false
}

// State returns the lifecycle state.
func (d *DockAppointment) State() State { return d.state }

// Version returns the dock-scheduling version of the last applied event (0
// for a row that predates the transfer).
func (d *DockAppointment) Version() int64 { return d.version }
