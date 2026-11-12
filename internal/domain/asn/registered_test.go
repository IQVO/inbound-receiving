package asn

import (
	"testing"
	"time"
)

var lateBase = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

func lateAsn(t *testing.T, registeredAt time.Time) *Asn {
	t.Helper()
	a, events, err := RegisterDraft(Draft{
		Number: "ASN-LATE-1", SupplierRef: "ACME",
		Lines: []LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	}, registeredAt)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	return a
}

// The ASN keeps its registration time (ADR 0025): RegisterDraft stamps it from
// the same now that stamps ASNRegistered, the accessor returns it UTC, and it
// round-trips through Persisted like every other flat field.
func TestAsnKeepsRegisteredAt(t *testing.T) {
	a := lateAsn(t, lateBase)
	if got := a.RegisteredAt(); !got.Equal(lateBase) {
		t.Fatalf("RegisteredAt() = %v, want %v", got, lateBase)
	}
	again, err := RehydrateDraft(a.Persisted())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if got := again.RegisteredAt(); !got.Equal(lateBase) {
		t.Fatalf("after round-trip RegisteredAt() = %v, want %v", got, lateBase)
	}
}

// A rehydrated legacy row (registered_at unknown, the zero time) stays valid
// and can never be late.
func TestAsnRegisteredAtUnknownForLegacyRow(t *testing.T) {
	a, err := RehydrateDraft(Persisted{
		Draft: Draft{Number: "ASN-OLD-1", SupplierRef: "ACME", Lines: []LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 3}}},
		State: Registered, Version: 1,
	})
	if err != nil {
		t.Fatalf("rehydrate legacy: %v", err)
	}
	if got := a.RegisteredAt(); !got.IsZero() {
		t.Fatalf("legacy RegisteredAt() = %v, want the zero time", got)
	}
}

func TestASNRegisteredCarriesRegisteredAt(t *testing.T) {
	a, events, _ := RegisterDraft(Draft{
		Number: "ASN-LATE-2", SupplierRef: "ACME",
		Lines: []LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	}, lateBase)
	reg := events[0].(ASNRegistered)
	if got := reg.RegisteredAt; !got.Equal(a.RegisteredAt()) {
		t.Fatalf("ASNRegistered.RegisteredAt = %v, want %v", got, a.RegisteredAt())
	}
}

func TestLateForArrival(t *testing.T) {
	arrived := lateBase.Add(time.Hour)
	cases := []struct {
		name        string
		registered  time.Time
		arrivedAt   time.Time
		wantLate    bool
		wantSeconds int64
	}{
		{"registered equals the arrival: on time", arrived, arrived, false, 0},
		{"registered one second before the arrival: on time", arrived.Add(-time.Second), arrived, false, 0},
		{"registered one second after the arrival: late by one second", arrived.Add(time.Second), arrived, true, 1},
		{"registered an hour after the arrival: late by 3600 seconds", arrived.Add(time.Hour), arrived, true, 3600},
		{"no arrival fact seen: never late", lateBase, time.Time{}, false, 0},
		{"registration time unknown (a legacy row): never late", time.Time{}, arrived, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := lateAsn(t, tc.registered)
			ev := a.LateForArrival("appt-1", "visit-1", tc.arrivedAt, lateBase.Add(2*time.Hour))
			if !tc.wantLate {
				if ev != nil {
					t.Fatalf("LateForArrival = %+v, want nil", ev)
				}
				return
			}
			wantLateEvent(t, ev, tc)
		})
	}
}

// wantLateEvent pins the payload of the late fact against the case's times.
func wantLateEvent(t *testing.T, ev *AsnLateForArrival, tc struct {
	name        string
	registered  time.Time
	arrivedAt   time.Time
	wantLate    bool
	wantSeconds int64
}) {
	t.Helper()
	if ev == nil {
		t.Fatal("LateForArrival = nil, want the event")
	}
	if ev.EventName() != "AsnLateForArrival" {
		t.Fatalf("EventName() = %q", ev.EventName())
	}
	if ev.AsnNumber() != "ASN-LATE-1" || ev.SupplierRef != "ACME" {
		t.Fatalf("event = %+v, want the ASN's number and supplier", ev)
	}
	if ev.AppointmentID != "appt-1" || ev.VisitID != "visit-1" {
		t.Fatalf("event = %+v, want the appointment and visit ids carried through", ev)
	}
	if !ev.AsnRegisteredAt.Equal(tc.registered) || !ev.ArrivedAt.Equal(tc.arrivedAt) {
		t.Fatalf("event times = %v / %v, want %v / %v", ev.AsnRegisteredAt, ev.ArrivedAt, tc.registered, tc.arrivedAt)
	}
	if ev.LatenessSeconds != tc.wantSeconds {
		t.Fatalf("LatenessSeconds = %d, want %d", ev.LatenessSeconds, tc.wantSeconds)
	}
	if !ev.OccurredAt().Equal(lateBase.Add(2 * time.Hour)) {
		t.Fatalf("OccurredAt() = %v", ev.OccurredAt())
	}
}
