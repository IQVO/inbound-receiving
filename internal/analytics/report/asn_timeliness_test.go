package report

import (
	"reflect"
	"testing"
	"time"
)

var lateT0 = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

func lateEvent(id string) Event {
	return Event{
		Kind: KindAsnLateForArrival, EventID: id, At: lateT0.Add(time.Hour),
		AsnNumber: "ASN-L", SupplierRef: "ACME", AppointmentID: "appt-1", VisitID: "visit-1",
		AsnRegisteredAt: lateT0, ArrivedAt: lateT0.Add(-time.Second), LatenessSeconds: 1,
	}
}

// AsnLateForArrival is the twelfth projected kind and an ASN one.
func TestAsnLateKind(t *testing.T) {
	found := false
	for _, k := range Kinds() {
		if k == KindAsnLateForArrival {
			found = true
		}
	}
	if !found {
		t.Fatalf("Kinds() = %v, want AsnLateForArrival among them", Kinds())
	}
	if len(Kinds()) != 12 {
		t.Fatalf("Kinds() = %d kinds, want 12", len(Kinds()))
	}
	if !KindAsnLateForArrival.IsAsn() || KindAsnLateForArrival.IsReceipt() || KindAsnLateForArrival.IsAppointment() {
		t.Fatal("AsnLateForArrival must belong to the ASN family only")
	}
}

// A valid late event passes; each missing or degenerate field is a
// deterministic rejection (the projector dead-letters it).
func TestAsnLateValidation(t *testing.T) {
	if err := lateEvent("ok-1").Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	broken := map[string]Event{
		"no supplier":       func() Event { e := lateEvent("x1"); e.SupplierRef = ""; return e }(),
		"no appointment id": func() Event { e := lateEvent("x2"); e.AppointmentID = ""; return e }(),
		"no visit id":       func() Event { e := lateEvent("x3"); e.VisitID = ""; return e }(),
		"no registration":   func() Event { e := lateEvent("x4"); e.AsnRegisteredAt = time.Time{}; return e }(),
		"no arrival":        func() Event { e := lateEvent("x5"); e.ArrivedAt = time.Time{}; return e }(),
		"zero lateness":     func() Event { e := lateEvent("x6"); e.LatenessSeconds = 0; return e }(),
	}
	for name, e := range broken {
		if err := e.Validate(); err == nil {
			t.Errorf("%s: accepted, want a rejection", name)
		}
	}
}

// The share is 1 - late/registered, defined once; no registration means no
// share. The bound keeps the top ten suppliers of the day, ties by name.
func TestAsnTimelinessOf(t *testing.T) {
	got := AsnTimelinessOf(map[string]int{"ACME": 4, "BOLT": 2, "NONE": 0}, map[string]int{"ACME": 1})
	want := []SupplierTimeliness{
		{SupplierRef: "ACME", Registered: 4, Late: 1, ShareOnTime: ptr(0.75)},
		{SupplierRef: "BOLT", Registered: 2, Late: 0, ShareOnTime: ptr(1)},
		{SupplierRef: "NONE", Registered: 0, Late: 0, ShareOnTime: nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AsnTimelinessOf = %+v, want %+v", got, want)
	}
}

// The bound: at most MaxTimelinessSuppliers rows, the day's biggest by
// registered count first, ties broken by supplier_ref ascending.
func TestAsnTimelinessBound(t *testing.T) {
	if MaxTimelinessSuppliers != 10 {
		t.Fatalf("MaxTimelinessSuppliers = %d, want 10 (ADR 0025)", MaxTimelinessSuppliers)
	}
	registered, late := map[string]int{}, map[string]int{}
	for i := 0; i < 12; i++ {
		name := "S" + string(rune('A'+i)) // S A..L, twelve suppliers
		registered[name] = 12 - i         // SA biggest ... SL smallest
	}
	registered["SA"], registered["SB"] = 13, 13 // a tie at the top, broken by name
	got := AsnTimelinessOf(registered, late)
	if len(got) != MaxTimelinessSuppliers {
		t.Fatalf("rows = %d, want %d", len(got), MaxTimelinessSuppliers)
	}
	if got[0].SupplierRef != "SA" || got[1].SupplierRef != "SB" {
		t.Fatalf("tie broken wrong: %s then %s", got[0].SupplierRef, got[1].SupplierRef)
	}
	for _, row := range got {
		if row.SupplierRef == "SK" || row.SupplierRef == "SL" {
			t.Fatalf("%s must be outside the bound", row.SupplierRef)
		}
	}
}

func ptr(v float64) *float64 { return &v }

// A count tie keeps the NAME order whatever the map iterated: the stable
// sort must not move equal counts (the boundary mutant that reorders a tie
// fails here deterministically).
func TestAsnTimelinessTieKeepsNameOrder(t *testing.T) {
	got := AsnTimelinessOf(map[string]int{"ZULU": 5, "ALPHA": 5, "MIKE": 5}, nil)
	want := []string{"ALPHA", "MIKE", "ZULU"}
	for i, name := range want {
		if got[i].SupplierRef != name {
			t.Fatalf("row %d = %s, want %s (tie must stay name-ascending)", i, got[i].SupplierRef, name)
		}
	}
}

// Name order opposite to count order: rank follows the COUNT, not the name
// (kills a comparator that falls back to the name for differing counts).
func TestAsnTimelinessRankBeatsName(t *testing.T) {
	got := AsnTimelinessOf(map[string]int{"AAA": 1, "ZZZ": 9}, nil)
	if got[0].SupplierRef != "ZZZ" || got[1].SupplierRef != "AAA" {
		t.Fatalf("rank = %s then %s, want ZZZ (9 registered) then AAA (1)", got[0].SupplierRef, got[1].SupplierRef)
	}
}
