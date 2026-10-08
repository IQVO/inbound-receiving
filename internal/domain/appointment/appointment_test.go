package appointment

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

const (
	id1 = "appt-123e4567-e89b-12d3-a456-426614174000"
	id2 = "appt-223e4567-e89b-12d3-a456-426614174000"
)

var (
	t0    = time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	start = t0.Add(time.Hour)
	end   = t0.Add(3 * time.Hour)
)

func book(t *testing.T) *DockAppointment {
	t.Helper()
	d, _, err := Book(id1, "DOOR-1", "ACME Freight", start, end, []string{"ASN-1", "ASN-2"}, t0)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return d
}

func bookAt(t *testing.T, id, door string, from, to time.Time) *DockAppointment {
	t.Helper()
	d, _, err := Book(id, door, "ACME", from, to, []string{"ASN-1"}, t0)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return d
}

func onlyEvent(t *testing.T, events []Event) Event {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	return events[0]
}

func assertHeader(t *testing.T, e Event, name string, at time.Time) {
	t.Helper()
	if e.EventName() != name || e.AppointmentID() != id1 || !e.OccurredAt().Equal(at) || e.OccurredAt().Location() != time.UTC {
		t.Fatalf("event = %s id=%s at=%v (%v), want %s at %v UTC", e.EventName(), e.AppointmentID(), e.OccurredAt(), e.OccurredAt().Location(), name, at)
	}
}

func TestBook(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	d, events, err := Book(id1, "DOOR-1", "ACME Freight", start.In(brt), end.In(brt), []string{"ASN-1", "ASN-2"}, t0.In(brt))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID() != id1 || d.DoorCode() != "DOOR-1" || d.Carrier() != "ACME Freight" || d.State() != Booked || d.Version() != 1 {
		t.Fatalf("appointment = %+v", d)
	}
	assertWindowUTC(t, d.Window())
	if got := d.AsnNumbers(); len(got) != 2 || got[0] != "ASN-1" || got[1] != "ASN-2" {
		t.Fatalf("asn numbers = %v", got)
	}
	e, ok := onlyEvent(t, events).(DockAppointmentBooked)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "DockAppointmentBooked", t0)
	if e.DoorCode != "DOOR-1" || e.Carrier != "ACME Freight" || len(e.AsnNumbers) != 2 {
		t.Fatalf("event = %+v", e)
	}
	assertWindowUTC(t, e.Window)
}

func assertWindowUTC(t *testing.T, w Window) {
	t.Helper()
	if !w.Start().Equal(start) || !w.End().Equal(end) || w.Start().Location() != time.UTC || w.End().Location() != time.UTC {
		t.Fatalf("window = %v - %v", w.Start(), w.End())
	}
}

func TestBookAllowsAWindowStartingNow(t *testing.T) {
	if _, _, err := Book(id1, "DOOR-1", "ACME", t0, t0.Add(time.Hour), []string{"ASN-1"}, t0); err != nil {
		t.Fatalf("window starting exactly now rejected: %v", err)
	}
}

func TestBookRejectsAWindowInThePast(t *testing.T) {
	d, events, err := Book(id1, "DOOR-1", "ACME", t0.Add(-time.Nanosecond), t0.Add(time.Hour), []string{"ASN-1"}, t0)
	if !errors.Is(err, ErrWindowInPast) || d != nil || events != nil {
		t.Fatalf("Book = %v %v %v", d, events, err)
	}
}

func TestBookRejects(t *testing.T) {
	asns := []string{"ASN-1"}
	cases := []struct {
		name    string
		id      string
		door    string
		carrier string
		from    time.Time
		to      time.Time
		asns    []string
		want    error
	}{
		{"bad id", "appt-1", "DOOR-1", "ACME", start, end, asns, ErrInvalidID},
		{"upper-case uuid", strings.ToUpper(id1), "DOOR-1", "ACME", start, end, asns, ErrInvalidID},
		{"empty door", id1, "", "ACME", start, end, asns, ErrInvalidDoorCode},
		{"blank carrier", id1, "DOOR-1", "   ", start, end, asns, ErrInvalidCarrier},
		{"empty carrier", id1, "DOOR-1", "", start, end, asns, ErrInvalidCarrier},
		{"long carrier", id1, "DOOR-1", strings.Repeat("c", MaxCarrierLength+1), start, end, asns, ErrInvalidCarrier},
		{"control carrier", id1, "DOOR-1", "AC\x00ME", start, end, asns, ErrInvalidCarrier},
		{"leading control carrier", id1, "DOOR-1", "\nACME", start, end, asns, ErrInvalidCarrier},
		{"end equals start", id1, "DOOR-1", "ACME", start, start, asns, ErrInvalidWindow},
		{"end before start", id1, "DOOR-1", "ACME", end, start, asns, ErrInvalidWindow},
		{"window too long", id1, "DOOR-1", "ACME", start, start.Add(MaxWindow + time.Nanosecond), asns, ErrInvalidWindow},
		{"no asns", id1, "DOOR-1", "ACME", start, end, nil, ErrNoAsns},
		{"bad asn number", id1, "DOOR-1", "ACME", start, end, []string{"ASN 1"}, asn.ErrInvalidNumber},
		{"duplicate asn", id1, "DOOR-1", "ACME", start, end, []string{"ASN-1", "ASN-2", "ASN-1"}, ErrDuplicateAsn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, events, err := Book(tc.id, tc.door, tc.carrier, tc.from, tc.to, tc.asns, t0)
			if !errors.Is(err, tc.want) || d != nil || events != nil {
				t.Fatalf("Book = %v %v %v; want %v", d, events, err, tc.want)
			}
		})
	}
}

func TestBookAcceptsBoundaries(t *testing.T) {
	long := strings.Repeat("c", MaxCarrierLength)
	door := strings.Repeat("d", MaxDoorCodeLength)
	if _, _, err := Book(id1, door, long, start, start.Add(MaxWindow), []string{"A"}, t0); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	if _, _, err := Book(id1, "D", "C", start, start.Add(time.Nanosecond), []string{"A"}, t0); err != nil {
		t.Fatalf("shortest window rejected: %v", err)
	}
}

func TestNewID(t *testing.T) {
	oracle := regexp.MustCompile(`^appt-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	for _, v := range []string{id1, id2, "appt-00000000-0000-0000-0000-000000000000", "appt-ffffffff-ffff-ffff-ffff-ffffffffffff", "", "appt-", "rcpt-123e4567-e89b-12d3-a456-426614174000", id1 + "0", " " + id1, "appt-123e4567-e89b-12d3-a456-42661417400g"} {
		got, err := NewID(v)
		if (err == nil) != oracle.MatchString(v) {
			t.Errorf("NewID(%q): err=%v", v, err)
		}
		if err == nil && got.String() != v {
			t.Errorf("NewID(%q) = %q", v, got)
		}
	}
}

func TestNewDoorCode(t *testing.T) {
	for _, v := range []string{"D", "WH1-DOCK-IN-01", strings.Repeat("d", MaxDoorCodeLength), strings.Repeat("å", MaxDoorCodeLength)} {
		got, err := NewDoorCode(v)
		if err != nil || got.String() != v {
			t.Errorf("NewDoorCode(%q) = %q, %v", v, got, err)
		}
	}
	for _, v := range []string{"", strings.Repeat("d", MaxDoorCodeLength+1), "DOOR 1", " D", "D\t", "D/1", "D\x00", "\x07D"} {
		if got, err := NewDoorCode(v); !errors.Is(err, ErrInvalidDoorCode) || got != "" {
			t.Errorf("NewDoorCode(%q) = %q, %v; want ErrInvalidDoorCode", v, got, err)
		}
	}
}

func TestNewWindow(t *testing.T) {
	w, err := NewWindow(start, start.Add(MaxWindow))
	if err != nil || !w.Start().Equal(start) || !w.End().Equal(start.Add(MaxWindow)) {
		t.Fatalf("window = %+v, %v", w, err)
	}
	if _, err := NewWindow(start, start.Add(MaxWindow+time.Nanosecond)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := NewWindow(start, start); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("empty window: %v", err)
	}
	if _, err := NewWindow(start, start.Add(-time.Second)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("inverted window: %v", err)
	}
}

func TestWindowOverlaps(t *testing.T) {
	base, _ := NewWindow(start, start.Add(2*time.Hour))
	at := func(fromMin, toMin int) Window {
		w, err := NewWindow(start.Add(time.Duration(fromMin)*time.Minute), start.Add(time.Duration(toMin)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	cases := []struct {
		name  string
		other Window
		want  bool
	}{
		{"identical", at(0, 120), true},
		{"inside", at(30, 60), true},
		{"containing", at(-30, 150), true},
		{"overlaps the start", at(-60, 30), true},
		{"overlaps the end", at(90, 200), true},
		{"touches the start", at(-60, 0), false},
		{"touches the end", at(120, 180), false},
		{"entirely before", at(-120, -60), false},
		{"entirely after", at(150, 200), false},
		{"one minute into the start", at(-60, 1), true},
		{"one minute before the end", at(119, 200), true},
	}
	for _, tc := range cases {
		if got := base.Overlaps(tc.other); got != tc.want {
			t.Errorf("%s: base.Overlaps(other) = %v, want %v", tc.name, got, tc.want)
		}
		if got := tc.other.Overlaps(base); got != tc.want {
			t.Errorf("%s: other.Overlaps(base) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckIn(t *testing.T) {
	d := book(t)
	at := start.Add(5 * time.Minute)
	events, err := d.CheckIn(at.In(time.FixedZone("BRT", -3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(DockAppointmentCheckedIn)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	assertHeader(t, e, "DockAppointmentCheckedIn", at)
	if e.DoorCode != "DOOR-1" || !e.CheckedInAt.Equal(at) || e.CheckedInAt.Location() != time.UTC {
		t.Fatalf("event = %+v", e)
	}
	if d.State() != CheckedIn || d.Version() != 2 {
		t.Fatalf("state=%s version=%d", d.State(), d.Version())
	}
}

func TestCheckInWindowBoundaries(t *testing.T) {
	allowed := []time.Time{start.Add(-CheckInLeadTime), start.Add(-CheckInLeadTime + time.Nanosecond), start, end.Add(-time.Nanosecond), end}
	for _, at := range allowed {
		if _, err := book(t).CheckIn(at); err != nil {
			t.Errorf("CheckIn(%v) = %v; want allowed", at.Sub(start), err)
		}
	}
	refused := []time.Time{start.Add(-CheckInLeadTime - time.Nanosecond), start.Add(-2 * time.Hour), end.Add(time.Nanosecond), end.Add(time.Hour)}
	for _, at := range refused {
		d := book(t)
		events, err := d.CheckIn(at)
		if !errors.Is(err, ErrOutsideCheckInWindow) || events != nil || d.State() != Booked || d.Version() != 1 {
			t.Errorf("CheckIn(%v) = %v %v; want ErrOutsideCheckInWindow", at.Sub(start), events, err)
		}
	}
}

func TestCheckInRequiresBooked(t *testing.T) {
	checkedIn := book(t)
	_, _ = checkedIn.CheckIn(start)
	cancelled := book(t)
	_, _ = cancelled.Cancel("", t0)
	completed := book(t)
	_, _ = completed.CheckIn(start)
	_, _ = completed.Complete(start.Add(time.Hour))
	for name, d := range map[string]*DockAppointment{"checked in": checkedIn, "cancelled": cancelled, "completed": completed} {
		version := d.Version()
		if events, err := d.CheckIn(start); !errors.Is(err, ErrNotBooked) || events != nil || d.Version() != version {
			t.Errorf("%s: events=%v err=%v", name, events, err)
		}
	}
}

func TestCancel(t *testing.T) {
	d := book(t)
	later := t0.Add(time.Minute)
	events, err := d.Cancel("carrier delayed", later)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(DockAppointmentCancelled)
	if !ok || e.DoorCode != "DOOR-1" || e.Reason != "carrier delayed" {
		t.Fatalf("event = %+v", events[0])
	}
	assertHeader(t, e, "DockAppointmentCancelled", later)
	if d.State() != Cancelled || d.Version() != 2 {
		t.Fatalf("state=%s version=%d", d.State(), d.Version())
	}
}

func TestCancelWithoutReason(t *testing.T) {
	events, err := book(t).Cancel("", t0)
	if err != nil || events[0].(DockAppointmentCancelled).Reason != "" {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func TestCancelRejectsInvalidReason(t *testing.T) {
	d := book(t)
	if events, err := d.Cancel("bad\nreason", t0); !errors.Is(err, shared.ErrInvalidReason) || events != nil || d.State() != Booked || d.Version() != 1 {
		t.Fatalf("events=%v err=%v state=%s v=%d", events, err, d.State(), d.Version())
	}
}

func TestCancelRequiresBooked(t *testing.T) {
	checkedIn := book(t)
	_, _ = checkedIn.CheckIn(start)
	cancelled := book(t)
	_, _ = cancelled.Cancel("", t0)
	for name, d := range map[string]*DockAppointment{"checked in": checkedIn, "cancelled": cancelled} {
		version := d.Version()
		if events, err := d.Cancel("", t0); !errors.Is(err, ErrNotBooked) || events != nil || d.Version() != version {
			t.Errorf("%s: events=%v err=%v", name, events, err)
		}
	}
}

func TestComplete(t *testing.T) {
	d := book(t)
	_, _ = d.CheckIn(start)
	at := start.Add(time.Hour)
	events, err := d.Complete(at.In(time.FixedZone("BRT", -3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := onlyEvent(t, events).(DockAppointmentCompleted)
	if !ok || e.DoorCode != "DOOR-1" || !e.CompletedAt.Equal(at) || e.CompletedAt.Location() != time.UTC {
		t.Fatalf("event = %+v", events[0])
	}
	assertHeader(t, e, "DockAppointmentCompleted", at)
	if d.State() != Completed || d.Version() != 3 {
		t.Fatalf("state=%s version=%d", d.State(), d.Version())
	}
}

func TestCompleteRequiresCheckedIn(t *testing.T) {
	booked := book(t)
	cancelled := book(t)
	_, _ = cancelled.Cancel("", t0)
	completed := book(t)
	_, _ = completed.CheckIn(start)
	_, _ = completed.Complete(start)
	for name, d := range map[string]*DockAppointment{"booked": booked, "cancelled": cancelled, "completed": completed} {
		version := d.Version()
		if events, err := d.Complete(start); !errors.Is(err, ErrNotCheckedIn) || events != nil || d.Version() != version {
			t.Errorf("%s: events=%v err=%v", name, events, err)
		}
	}
}

func TestStateActive(t *testing.T) {
	want := map[State]bool{Booked: true, CheckedIn: true, Completed: false, Cancelled: false, "": false}
	for s, active := range want {
		if s.Active() != active {
			t.Errorf("%q.Active() = %v, want %v", s, s.Active(), active)
		}
	}
}

func TestAsnNumbersReturnsACopy(t *testing.T) {
	d := book(t)
	got := d.AsnNumbers()
	got[0] = "HACKED"
	if d.AsnNumbers()[0] != "ASN-1" {
		t.Fatal("AsnNumbers() exposed the internal slice")
	}
}

func TestRehydrate(t *testing.T) {
	past := t0.Add(-48 * time.Hour)
	for _, state := range []State{Booked, CheckedIn, Completed, Cancelled} {
		d, err := Rehydrate(id1, "DOOR-1", "ACME", past, past.Add(time.Hour), []string{"ASN-1"}, state, 4)
		if err != nil || d.State() != state || d.Version() != 4 || d.ID() != id1 || d.Carrier() != "ACME" {
			t.Fatalf("state %s: %+v %v", state, d, err)
		}
	}
	d, err := Rehydrate(id1, "DOOR-1", "ACME", start, end, []string{"ASN-1"}, Booked, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Cancel("", t0); err != nil || d.Version() != 2 {
		t.Fatalf("err=%v v=%d", err, d.Version())
	}
}

func TestRehydrateRevalidates(t *testing.T) {
	ok := []string{"ASN-1"}
	cases := []struct {
		name    string
		id      string
		carrier string
		from    time.Time
		to      time.Time
		asns    []string
		state   State
		version int64
		want    error
	}{
		{"unknown state", id1, "ACME", start, end, ok, "Bogus", 1, ErrInvalidState},
		{"version zero", id1, "ACME", start, end, ok, Booked, 0, ErrInvalidVersion},
		{"bad id", "x", "ACME", start, end, ok, Booked, 1, ErrInvalidID},
		{"blank carrier", id1, " ", start, end, ok, Booked, 1, ErrInvalidCarrier},
		{"inverted window", id1, "ACME", end, start, ok, Booked, 1, ErrInvalidWindow},
		{"no asns", id1, "ACME", start, end, nil, Booked, 1, ErrNoAsns},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Rehydrate(tc.id, "DOOR-1", tc.carrier, tc.from, tc.to, tc.asns, tc.state, tc.version)
			if !errors.Is(err, tc.want) || d != nil {
				t.Fatalf("Rehydrate = %v, %v; want %v", d, err, tc.want)
			}
		})
	}
	if _, err := Rehydrate(id1, "", "ACME", start, end, ok, Booked, 1); !errors.Is(err, ErrInvalidDoorCode) {
		t.Fatalf("empty door: %v", err)
	}
}

func TestCheckNoOverlap(t *testing.T) {
	candidate := bookAt(t, id1, "DOOR-1", start, end)
	cases := []struct {
		name string
		prep func(t *testing.T) *DockAppointment
		want error
	}{
		{"overlapping booked", func(t *testing.T) *DockAppointment {
			return bookAt(t, id2, "DOOR-1", start.Add(time.Hour), end.Add(time.Hour))
		}, ErrWindowOverlap},
		{"overlapping checked in", func(t *testing.T) *DockAppointment {
			d := bookAt(t, id2, "DOOR-1", start.Add(time.Hour), end.Add(time.Hour))
			_, _ = d.CheckIn(start.Add(time.Hour))
			return d
		}, ErrWindowOverlap},
		{"adjacent after", func(t *testing.T) *DockAppointment { return bookAt(t, id2, "DOOR-1", end, end.Add(time.Hour)) }, nil},
		{"adjacent before", func(t *testing.T) *DockAppointment { return bookAt(t, id2, "DOOR-1", start.Add(-time.Hour), start) }, nil},
		{"overlapping on another door", func(t *testing.T) *DockAppointment { return bookAt(t, id2, "DOOR-2", start, end) }, nil},
		{"overlapping but cancelled", func(t *testing.T) *DockAppointment {
			d := bookAt(t, id2, "DOOR-1", start, end)
			_, _ = d.Cancel("", t0)
			return d
		}, nil},
		{"overlapping but completed", func(t *testing.T) *DockAppointment {
			d := bookAt(t, id2, "DOOR-1", start, end)
			_, _ = d.CheckIn(start)
			_, _ = d.Complete(end)
			return d
		}, nil},
		{"the candidate itself", func(t *testing.T) *DockAppointment { return bookAt(t, id1, "DOOR-1", start, end) }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (Schedule{}).CheckNoOverlap(candidate, []*DockAppointment{tc.prep(t)}); !errors.Is(err, tc.want) {
				t.Fatalf("CheckNoOverlap = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckNoOverlapScansEveryAppointment(t *testing.T) {
	candidate := bookAt(t, id1, "DOOR-1", start, end)
	free := bookAt(t, id2, "DOOR-1", end, end.Add(time.Hour))
	clash := bookAt(t, "appt-323e4567-e89b-12d3-a456-426614174000", "DOOR-1", start.Add(time.Minute), start.Add(2*time.Minute))
	if err := (Schedule{}).CheckNoOverlap(candidate, []*DockAppointment{free, clash}); !errors.Is(err, ErrWindowOverlap) {
		t.Fatalf("clash after a free one: %v", err)
	}
	if err := (Schedule{}).CheckNoOverlap(candidate, nil); err != nil {
		t.Fatalf("empty schedule: %v", err)
	}
}
