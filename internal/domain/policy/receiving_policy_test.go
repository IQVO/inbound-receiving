package policy

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

var at = time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)

func TestDeclareCreatesVersionOneAndRaisesTheEvent(t *testing.T) {
	p, ev, err := Declare("", "SUP-1", BandInput{Over: 0.05, OverStop: 0.10, Under: 0.02, UnderStop: 0.20}, 0, at)
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if p.Version() != 1 {
		t.Fatalf("version = %d, want 1", p.Version())
	}
	if p.OverTolerance() != 0.05 || p.OverHardStop() != 0.10 || p.UnderTolerance() != 0.02 || p.UnderHardStop() != 0.20 {
		t.Fatalf("bands = %+v", p)
	}
	if ev.EventName() != "ReceivingPolicyDeclared" {
		t.Fatalf("event name = %q", ev.EventName())
	}
	if ev.SupplierRef != "SUP-1" || ev.OwnerRef != shared.OwnerRef("") {
		t.Fatalf("event keys = %q/%q", ev.OwnerRef, ev.SupplierRef)
	}
	if !ev.DeclaredAt.Equal(at) || ev.Version != 1 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestDeclareIsCreateOrReplaceAndBumpsTheVersion(t *testing.T) {
	p, _, err := Declare("", "SUP-1", BandInput{Over: 0.05, OverStop: 0.10}, 0, at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p2, ev, err := Declare("", "SUP-1", BandInput{Over: 0.01, OverStop: 0.02}, p.Version(), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if p2.Version() != 2 {
		t.Fatalf("version = %d, want 2", p2.Version())
	}
	if ev.Version != 2 {
		t.Fatalf("event version = %d, want 2", ev.Version)
	}
	if p2.OverTolerance() != 0.01 || p2.OverHardStop() != 0.02 {
		t.Fatalf("replaced bands = %+v", p2)
	}
}

func TestDeclareRefusesAnInvalidBand(t *testing.T) {
	cases := []struct {
		name  string
		bands BandInput
	}{
		{"tolerance below zero", BandInput{Over: -0.01, OverStop: 0.10}},
		{"hard stop below tolerance", BandInput{Over: 0.10, OverStop: 0.05}},
		{"tolerance equals hard stop", BandInput{Over: 0.10, OverStop: 0.10}},
		{"hard stop above one", BandInput{Over: 0.05, OverStop: 1.01}},
		{"under tolerance below zero", BandInput{Under: -0.01, UnderStop: 0.10}},
		{"under stop equals tolerance", BandInput{Under: 0.10, UnderStop: 0.10}},
		{"under stop above one", BandInput{Under: 0.05, UnderStop: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := Declare("", "SUP-1", c.bands, 0, at); !errors.Is(err, ErrInvalidBand) {
				t.Fatalf("err = %v, want ErrInvalidBand", err)
			}
		})
	}
}

func TestDeclareAcceptsTheBoundaryBands(t *testing.T) {
	if _, _, err := Declare("", "SUP-1", BandInput{Over: 0, OverStop: 0.01}, 0, at); err != nil {
		t.Fatalf("zero tolerance: %v", err)
	}
	if _, _, err := Declare("", "SUP-1", BandInput{Over: 0.05, OverStop: 1}, 0, at); err != nil {
		t.Fatalf("hard stop of one: %v", err)
	}
}

func TestDeclareValidatesTheKey(t *testing.T) {
	if _, _, err := Declare("", "  ", BandInput{Over: 0.05, OverStop: 0.10}, 0, at); err == nil {
		t.Fatal("blank supplier accepted")
	}
	if _, _, err := Declare("not an owner", "SUP-1", BandInput{Over: 0.05, OverStop: 0.10}, 0, at); err == nil {
		t.Fatal("invalid owner accepted")
	}
}

func TestOwnerAndSupplierAreTheKey(t *testing.T) {
	house, _, err := Declare("", "SUP-1", BandInput{Over: 0.05, OverStop: 0.10}, 0, at)
	if err != nil {
		t.Fatalf("house: %v", err)
	}
	client, _, err := Declare("owner-77", "SUP-1", BandInput{Over: 0.01, OverStop: 0.02}, 0, at)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if house.OwnerRef() == client.OwnerRef() || house.OverTolerance() == client.OverTolerance() {
		t.Fatal("the two policies are not distinct")
	}
	if house.Key() == client.Key() {
		t.Fatalf("keys equal: %q", house.Key())
	}
}

func TestRehydrateRoundTripsAndRefusesMalformedRows(t *testing.T) {
	p, _, err := Declare("owner-77", "SUP-1", BandInput{Over: 0.05, OverStop: 0.10, Under: 0.02, UnderStop: 0.20}, 3, at)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	back, err := Rehydrate(p.Persisted())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if back.Key() != p.Key() || back.Version() != p.Version() || back.OverTolerance() != p.OverTolerance() {
		t.Fatalf("round trip = %+v", back)
	}
	if _, err := Rehydrate(Persisted{OwnerRef: "owner-77", SupplierRef: "SUP-1", Over: 0.10, OverStop: 0.05, Version: 3, DeclaredAt: at}); !errors.Is(err, ErrInvalidBand) {
		t.Fatalf("err = %v, want ErrInvalidBand", err)
	}
	if _, err := Rehydrate(Persisted{OwnerRef: "owner-77", SupplierRef: "SUP-1", Over: 0.05, OverStop: 0.10, Version: 0, DeclaredAt: at}); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("err = %v, want ErrInvalidVersion", err)
	}
	if _, err := Rehydrate(Persisted{OwnerRef: "owner-77", SupplierRef: "SUP-1", Over: 0.05, OverStop: 0.10, Version: 1}); !errors.Is(err, ErrInvalidDeclaredAt) {
		t.Fatalf("err = %v, want ErrInvalidDeclaredAt", err)
	}
}
