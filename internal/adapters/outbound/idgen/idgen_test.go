package idgen_test

import (
	"regexp"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func TestUUIDIdsPassTheDomainPatterns(t *testing.T) {
	var g idgen.UUID
	a1, a2 := g.NewAppointmentID(), g.NewAppointmentID()
	if _, err := appointment.NewID(a1); err != nil || a1 == a2 {
		t.Fatalf("appointment ids %q %q: %v", a1, a2, err)
	}
	r1, r2 := g.NewReceiptID(), g.NewReceiptID()
	if _, err := receipt.NewID(r1); err != nil || r1 == r2 {
		t.Fatalf("receipt ids %q %q: %v", r1, r2, err)
	}
}

func TestLPNIsAnInternalULIDPlate(t *testing.T) {
	var g idgen.UUID
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		lpn := g.NewLPN()
		if !lpnPattern.MatchString(lpn) || seen[lpn] {
			t.Fatalf("lpn %q is malformed or repeated", lpn)
		}
		seen[lpn] = true
	}
}

var lpnPattern = regexp.MustCompile(`^LPN-[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
