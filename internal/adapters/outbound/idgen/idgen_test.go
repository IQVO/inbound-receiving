package idgen_test

import (
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
