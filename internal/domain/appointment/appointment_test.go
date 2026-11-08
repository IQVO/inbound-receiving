package appointment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

const (
	id1 = "appt-123e4567-e89b-12d3-a456-426614174000"
)

var (
	t0    = time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	start = t0.Add(time.Hour)
	end   = t0.Add(3 * time.Hour)
)

func rehydrate(t *testing.T, state State, version int64) *DockAppointment {
	t.Helper()
	d, err := Rehydrate(id1, "DOOR-1", "ACME Freight", start, end, []string{"ASN-1", "ASN-2"}, state, version)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	return d
}

func TestRehydrate(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	d, err := Rehydrate(id1, "DOOR-1", "ACME Freight", start.In(brt), end.In(brt), []string{"ASN-1", "ASN-2"}, Booked, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID() != id1 || d.DoorCode() != "DOOR-1" || d.Carrier() != "ACME Freight" || d.State() != Booked || d.Version() != 7 {
		t.Fatalf("appointment = %+v", d)
	}
	if !d.Window().Start().Equal(start) || d.Window().Start().Location() != time.UTC || !d.Window().End().Equal(end) || d.Window().End().Location() != time.UTC {
		t.Fatalf("window = %v..%v, want UTC %v..%v", d.Window().Start(), d.Window().End(), start, end)
	}
	if got := d.AsnNumbers(); len(got) != 2 || got[0] != "ASN-1" || got[1] != "ASN-2" {
		t.Fatalf("asn numbers = %v", got)
	}
}

func TestRehydrateAcceptsAWindowInThePastAndVersionZero(t *testing.T) {
	d, err := Rehydrate(id1, "DOOR-1", "ACME", t0.Add(-48*time.Hour), t0.Add(-47*time.Hour), []string{"ASN-1"}, Completed, 0)
	if err != nil {
		t.Fatalf("a legacy row (past window, version 0) must rehydrate: %v", err)
	}
	if d.Version() != 0 || d.State() != Completed {
		t.Fatalf("appointment = %+v", d)
	}
}

func TestRehydrateAcceptsALongWindow(t *testing.T) {
	// dock-scheduling's longest window is a per-site setting: the copy has no cap.
	if _, err := Rehydrate(id1, "DOOR-1", "ACME", start, start.Add(12*time.Hour), []string{"ASN-1"}, Booked, 1); err != nil {
		t.Fatalf("a 12h window must rehydrate: %v", err)
	}
}

func TestRehydrateRejects(t *testing.T) {
	long := strings.Repeat("x", MaxCarrierLength+1)
	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"bad id", func() error { _, e := Rehydrate("nope", "D", "C", start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidID},
		{"bad door", func() error { _, e := Rehydrate(id1, "", "C", start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidDoorCode},
		{"blank carrier", func() error { _, e := Rehydrate(id1, "D", "  ", start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidCarrier},
		{"long carrier", func() error { _, e := Rehydrate(id1, "D", long, start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidCarrier},
		{"control carrier", func() error { _, e := Rehydrate(id1, "D", "A\nB", start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidCarrier},
		// IndexFunc returns 0 when the control character is the first rune;
		// only the 0-index case distinguishes >= 0 from > 0.
		{"leading control carrier", func() error { _, e := Rehydrate(id1, "D", "\nAB", start, end, []string{"A"}, Booked, 1); return e }, ErrInvalidCarrier},
		{"empty window", func() error { _, e := Rehydrate(id1, "D", "C", start, start, []string{"A"}, Booked, 1); return e }, ErrInvalidWindow},
		{"inverted window", func() error { _, e := Rehydrate(id1, "D", "C", end, start, []string{"A"}, Booked, 1); return e }, ErrInvalidWindow},
		{"no asns", func() error { _, e := Rehydrate(id1, "D", "C", start, end, nil, Booked, 1); return e }, ErrNoAsns},
		{"duplicate asn", func() error { _, e := Rehydrate(id1, "D", "C", start, end, []string{"A", "A"}, Booked, 1); return e }, ErrDuplicateAsn},
		{"bad asn", func() error { _, e := Rehydrate(id1, "D", "C", start, end, []string{""}, Booked, 1); return e }, asn.ErrInvalidNumber},
		{"unknown state", func() error { _, e := Rehydrate(id1, "D", "C", start, end, []string{"A"}, State("Lost"), 1); return e }, ErrInvalidState},
		{"negative version", func() error { _, e := Rehydrate(id1, "D", "C", start, end, []string{"A"}, Booked, -1); return e }, ErrInvalidVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRehydrateAcceptsBoundaries(t *testing.T) {
	door := strings.Repeat("d", MaxDoorCodeLength)
	carrier := strings.Repeat("c", MaxCarrierLength)
	if _, err := Rehydrate(id1, door, carrier, start, start.Add(time.Nanosecond), []string{"A"}, Booked, 0); err != nil {
		t.Fatalf("boundary values must rehydrate: %v", err)
	}
}

func TestNewID(t *testing.T) {
	if id, err := NewID(id1); err != nil || id.String() != id1 {
		t.Fatalf("NewID = %q, %v", id, err)
	}
	for _, bad := range []string{"", "appt-", "appt-XYZ", "ASN-1", id1 + "x", " " + id1, strings.ToUpper(id1)} {
		if _, err := NewID(bad); !errors.Is(err, ErrInvalidID) {
			t.Errorf("NewID(%q) err = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestNewDoorCode(t *testing.T) {
	for _, ok := range []string{"DOOR-1", "d", strings.Repeat("x", MaxDoorCodeLength)} {
		if got, err := NewDoorCode(ok); err != nil || got.String() != ok {
			t.Errorf("NewDoorCode(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "a b", "a/b", "a\tb", "a\x00b", strings.Repeat("x", MaxDoorCodeLength+1)} {
		if _, err := NewDoorCode(bad); !errors.Is(err, ErrInvalidDoorCode) {
			t.Errorf("NewDoorCode(%q) err = %v, want ErrInvalidDoorCode", bad, err)
		}
	}
}

func TestNewWindow(t *testing.T) {
	w, err := NewWindow(start, end)
	if err != nil || !w.Start().Equal(start) || !w.End().Equal(end) {
		t.Fatalf("NewWindow = %+v, %v", w, err)
	}
	for name, c := range map[string][2]time.Time{
		"empty":    {start, start},
		"inverted": {end, start},
	} {
		if _, err := NewWindow(c[0], c[1]); !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("%s: err = %v, want ErrInvalidWindow", name, err)
		}
	}
}

func TestAsnNumbersReturnsACopyAndCovers(t *testing.T) {
	d := rehydrate(t, Booked, 1)
	got := d.AsnNumbers()
	got[0] = "MUTATED"
	if d.AsnNumbers()[0] != "ASN-1" {
		t.Fatal("AsnNumbers must return a copy")
	}
	if !d.Covers("ASN-1") || !d.Covers("ASN-2") || d.Covers("ASN-3") || d.Covers("") {
		t.Fatal("Covers must be true exactly for the listed ASNs")
	}
}

func names(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventName())
	}
	return out
}

func TestReplayByState(t *testing.T) {
	booked, changed, firstReceipt := t0, t0.Add(4*time.Hour), t0.Add(90*time.Minute)
	tl := Timeline{BookedAt: booked, ChangedAt: changed, CheckedInAt: firstReceipt}
	tests := []struct {
		state State
		want  string
	}{
		{Booked, "DockAppointmentBooked"},
		{CheckedIn, "DockAppointmentBooked DockAppointmentCheckedIn"},
		{Completed, "DockAppointmentBooked DockAppointmentCheckedIn DockAppointmentCompleted"},
		{Cancelled, "DockAppointmentBooked DockAppointmentCancelled"},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			events := rehydrate(t, tc.state, 3).Replay(tl)
			if got := strings.Join(names(events), " "); got != tc.want {
				t.Fatalf("events = %q, want %q", got, tc.want)
			}
			for _, e := range events {
				if e.AppointmentID() != id1 || e.OccurredAt().Location() != time.UTC {
					t.Fatalf("event %s id=%s at=%v", e.EventName(), e.AppointmentID(), e.OccurredAt())
				}
			}
		})
	}
}

func TestReplayPayloadsAndMoments(t *testing.T) {
	tl := Timeline{BookedAt: t0, ChangedAt: t0.Add(4 * time.Hour), CheckedInAt: t0.Add(90 * time.Minute)}
	t.Run("completed replays booked, checked in and completed with their payloads", func(t *testing.T) { assertCompletedReplay(t, tl) })
	t.Run("a CheckedIn row checked in at its last change", func(t *testing.T) { assertCheckedInReplay(t, tl) })
	t.Run("cancelled carries the last change and no reason", func(t *testing.T) { assertCancelledReplay(t, tl) })
}

func assertCompletedReplay(t *testing.T, tl Timeline) {
	t.Helper()
	events := rehydrate(t, Completed, 2).Replay(tl)
	assertReplayedBooked(t, events[0], tl)
	assertReplayedCheckedIn(t, events[1], tl)
	assertReplayedCompleted(t, events[2], tl)
}

func assertReplayedBooked(t *testing.T, e Event, tl Timeline) {
	t.Helper()
	b, ok := e.(DockAppointmentBooked)
	if !ok || b.DoorCode != "DOOR-1" || b.Carrier != "ACME Freight" || !b.Window.Start().Equal(start) || !b.Window.End().Equal(end) ||
		len(b.AsnNumbers) != 2 || !b.At.Equal(tl.BookedAt) {
		t.Fatalf("booked = %+v", e)
	}
}

func assertReplayedCheckedIn(t *testing.T, e Event, tl Timeline) {
	t.Helper()
	in, ok := e.(DockAppointmentCheckedIn)
	if !ok || !in.CheckedInAt.Equal(tl.CheckedInAt) || !in.At.Equal(tl.CheckedInAt) || in.DoorCode != "DOOR-1" {
		t.Fatalf("checked in = %+v", e)
	}
}

func assertReplayedCompleted(t *testing.T, e Event, tl Timeline) {
	t.Helper()
	done, ok := e.(DockAppointmentCompleted)
	if !ok || !done.CompletedAt.Equal(tl.ChangedAt) || !done.At.Equal(tl.ChangedAt) || done.DoorCode != "DOOR-1" {
		t.Fatalf("completed = %+v", e)
	}
}

func assertCheckedInReplay(t *testing.T, tl Timeline) {
	t.Helper()
	events := rehydrate(t, CheckedIn, 2).Replay(tl)
	if in := events[1].(DockAppointmentCheckedIn); !in.CheckedInAt.Equal(tl.ChangedAt) {
		t.Fatalf("a CheckedIn row checked in at its last change, got %v", in.CheckedInAt)
	}
}

func assertCancelledReplay(t *testing.T, tl Timeline) {
	t.Helper()
	events := rehydrate(t, Cancelled, 2).Replay(tl)
	if c := events[1].(DockAppointmentCancelled); !c.At.Equal(tl.ChangedAt) || c.DoorCode != "DOOR-1" || c.Reason != "" {
		t.Fatalf("cancelled = %+v", c)
	}
}

func TestReplayNeverGoesBackInTime(t *testing.T) {
	d := rehydrate(t, Completed, 1)
	booked, changed := t0, t0.Add(time.Hour)
	for name, tl := range map[string]Timeline{
		"changed before booked":      {BookedAt: booked, ChangedAt: booked.Add(-time.Hour)},
		"no check-in moment":         {BookedAt: booked, ChangedAt: changed},
		"check-in before booked":     {BookedAt: booked, ChangedAt: changed, CheckedInAt: booked.Add(-time.Minute)},
		"check-in after the changed": {BookedAt: booked, ChangedAt: changed, CheckedInAt: changed.Add(time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			events := d.Replay(tl)
			for i := 1; i < len(events); i++ {
				if events[i].OccurredAt().Before(events[i-1].OccurredAt()) {
					t.Fatalf("event %d (%s at %v) is before event %d (%s at %v)", i, events[i].EventName(), events[i].OccurredAt(),
						i-1, events[i-1].EventName(), events[i-1].OccurredAt())
				}
			}
			if events[0].OccurredAt().Before(booked) {
				t.Fatalf("booked at %v is before the row was created", events[0].OccurredAt())
			}
		})
	}
}

func TestReplayCheckInMomentIsKeptWhenInsideTheRange(t *testing.T) {
	d := rehydrate(t, Completed, 1)
	booked, changed := t0, t0.Add(time.Hour)
	// The bounds are inclusive on both sides.
	for _, in := range []time.Time{booked, changed, booked.Add(time.Minute)} {
		got := d.Replay(Timeline{BookedAt: booked, ChangedAt: changed, CheckedInAt: in})[1].(DockAppointmentCheckedIn)
		if !got.CheckedInAt.Equal(in) {
			t.Errorf("check-in moment %v was replaced by %v", in, got.CheckedInAt)
		}
	}
}
