package kafka

import (
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// The AsnLateForArrival wire format of ADR 0025: the ASN's number, its
// supplier, the appointment and visit it was received under, both timestamps
// and the lateness in whole seconds.
func TestEncoderGoldenAsnLateForArrival(t *testing.T) {
	ev := asn.AsnLateForArrival{
		Header:          asn.Header{Number: "ASN-1001", At: at(9, 8, 5)},
		SupplierRef:     "ACME",
		AppointmentID:   apptID,
		VisitID:         "visit-77c0",
		AsnRegisteredAt: at(9, 8, 4),
		ArrivedAt:       at(9, 7, 0),
		LatenessSeconds: 64,
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeAsn(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("asn", "AsnLateForArrival", "ASN-1001", "2026-10-09T08:05:00Z",
		`{"asn_number":"ASN-1001","supplier_ref":"ACME","appointment_id":"`+apptID+`","visit_id":"visit-77c0",`+
			`"asn_registered_at":"2026-10-09T08:04:00Z","arrived_at":"2026-10-09T07:00:00Z","lateness_seconds":64}`)
	assertRow(t, msgs[0], "asn.AsnLateForArrival", "ASN-1001", "ASN-1001", want)
}

// A one-second lateness stays one second on the wire (the boundary of the
// strict comparison, both sides pinned by the domain tests).
func TestEncoderAsnLateForArrivalOneSecond(t *testing.T) {
	ev := asn.AsnLateForArrival{
		Header:          asn.Header{Number: "ASN-1001", At: at(9, 8, 5)},
		SupplierRef:     "ACME",
		AppointmentID:   apptID,
		VisitID:         "visit-77c0",
		AsnRegisteredAt: at(9, 7, 0).Add(time.Second),
		ArrivedAt:       at(9, 7, 0),
		LatenessSeconds: 1,
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeAsn(ev)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("EncodeAsn = %v, %v", msgs, err)
	}
	want := `"asn_registered_at":"2026-10-09T07:00:01Z","arrived_at":"2026-10-09T07:00:00Z","lateness_seconds":1}}`
	if got := string(msgs[0].Value); !hasSuffix(got, want) {
		t.Fatalf("value = %s\nwant suffix %s", got, want)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
