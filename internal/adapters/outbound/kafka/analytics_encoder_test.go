package kafka

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// analyticsEnvelope is envelope() on the analytics stream: the only
// difference from the integration bytes is the dataschema stream segment.
func analyticsEnvelope(entity, eventName, subject, timeStr, data string) string {
	return strings.Replace(envelope(entity, eventName, subject, timeStr, data),
		"urn:warehouse:inbound-receiving:events:", "urn:warehouse:inbound-receiving:analytics:", 1)
}

// goldenCase is one published event with its expected CloudEvents fields
// (the integration payload, which the analytics payload equals: ADR 0006).
type goldenCase struct {
	entity, name, subject, key, time, data string
	encode                                 func(e interface {
		EncodeAsn(...asn.Event) ([]outbox.Message, error)
		EncodeAppointment(...appointment.Event) ([]outbox.Message, error)
		EncodeReceipt(...receipt.Event) ([]outbox.Message, error)
	}) ([]outbox.Message, error)
}

type encoderAPI = interface {
	EncodeAsn(...asn.Event) ([]outbox.Message, error)
	EncodeAppointment(...appointment.Event) ([]outbox.Message, error)
	EncodeReceipt(...receipt.Event) ([]outbox.Message, error)
}

func asnCase(name, time, data string, ev asn.Event) goldenCase {
	return goldenCase{"asn", name, "ASN-1001", "ASN-1001", time, data,
		func(e encoderAPI) ([]outbox.Message, error) { return e.EncodeAsn(ev) }}
}

func apptCase(name, time, data string, ev appointment.Event) goldenCase {
	return goldenCase{"dockappointment", name, apptID, apptID, time, data,
		func(e encoderAPI) ([]outbox.Message, error) { return e.EncodeAppointment(ev) }}
}

func rcptCase(name, time, data string, ev receipt.Event) goldenCase {
	return goldenCase{"receipt", name, rcptID, "ASN-1001", time, data,
		func(e encoderAPI) ([]outbox.Message, error) { return e.EncodeReceipt(ev) }}
}

func goldenEvents(t *testing.T) []goldenCase {
	t.Helper()
	apptData := `"appointment_id":"` + apptID + `","door_code":"WH1-DOCK-IN-01"`
	rcptData := `"receipt_id":"` + rcptID + `","asn_number":"ASN-1001"`
	return []goldenCase{
		asnCase("ASNRegistered", "2026-10-08T12:00:00Z",
			`{"asn_number":"ASN-1001","supplier_ref":"ACME","expected_arrival":"2026-10-09T08:00:00Z","lines":[{"line_no":1,"sku":"SKU-1","expected_qty":40},{"line_no":2,"sku":"SKU-2","expected_qty":5}]}`,
			registeredAsn(t, at(9, 8, 0))),
		asnCase("ASNCancelled", "2026-10-08T12:30:00Z", `{"asn_number":"ASN-1001","reason":"Supplier cancelled the shipment"}`,
			asn.ASNCancelled{Header: asn.Header{Number: "ASN-1001", At: at(8, 12, 30)}, Reason: "Supplier cancelled the shipment"}),
		apptCase("DockAppointmentBooked", "2026-10-08T12:05:00Z",
			`{"appointment_id":"`+apptID+`","door_code":"WH1-DOCK-IN-01","carrier":"ACME Freight","window_start":"2026-10-09T08:00:00Z","window_end":"2026-10-09T10:00:00Z","asn_numbers":["ASN-1001"]}`,
			bookedEvent(t)),
		apptCase("DockAppointmentCheckedIn", "2026-10-09T07:50:00Z", `{`+apptData+`,"checked_in_at":"2026-10-09T07:50:00Z"}`,
			appointment.DockAppointmentCheckedIn{Header: apptHeader(at(9, 7, 50)), DoorCode: "WH1-DOCK-IN-01", CheckedInAt: at(9, 7, 50)}),
		apptCase("DockAppointmentCancelled", "2026-10-08T18:00:00Z", `{`+apptData+`,"reason":"Carrier delayed"}`,
			appointment.DockAppointmentCancelled{Header: apptHeader(at(8, 18, 0)), DoorCode: "WH1-DOCK-IN-01", Reason: "Carrier delayed"}),
		apptCase("DockAppointmentCompleted", "2026-10-09T09:30:00Z", `{`+apptData+`,"completed_at":"2026-10-09T09:30:00Z"}`,
			appointment.DockAppointmentCompleted{Header: apptHeader(at(9, 9, 30)), DoorCode: "WH1-DOCK-IN-01", CompletedAt: at(9, 9, 30)}),
		rcptCase("ReceiptOpened", "2026-10-09T08:05:00Z",
			`{`+rcptData+`,"appointment_id":"`+apptID+`","door_code":"WH1-DOCK-IN-01","opened_at":"2026-10-09T08:05:00Z"}`,
			receipt.ReceiptOpened{Header: receiptHeader(at(9, 8, 5)), AppointmentID: apptID, DoorCode: "WH1-DOCK-IN-01"}),
		rcptCase("ReceiptLineReceived", "2026-10-09T08:10:00Z",
			`{`+rcptData+`,"line_no":1,"sku":"SKU-1","quantity":40,"condition":"Good","received_at":"2026-10-09T08:10:00Z"}`,
			receipt.ReceiptLineReceived{Header: receiptHeader(at(9, 8, 10)), LineNo: 1, SKU: "SKU-1", Quantity: 40, Condition: receipt.ConditionGood}),
		rcptCase("ReceiptClosed", "2026-10-09T09:30:00Z",
			`{`+rcptData+`,"closed_at":"2026-10-09T09:30:00Z","discrepancies":[`+
				`{"line_no":1,"sku":"SKU-1","kind":"Short","expected_qty":40,"received_qty":34,"damaged_qty":4},`+
				`{"line_no":1,"sku":"SKU-1","kind":"Damaged","expected_qty":40,"received_qty":34,"damaged_qty":4},`+
				`{"line_no":2,"sku":"SKU-2","kind":"Over","expected_qty":5,"received_qty":7,"damaged_qty":0}]}`,
			receipt.ReceiptClosed{Header: receiptHeader(at(9, 9, 30)), Discrepancies: []receipt.Discrepancy{
				{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindShort, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
				{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindDamaged, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
				{LineNo: 2, SKU: "SKU-2", Kind: receipt.KindOver, ExpectedQty: 5, ReceivedQty: 7, DamagedQty: 0},
			}}),
	}
}

// TestAnalyticsEncoder_GoldenWireFormat pins the exact analytics bytes of
// every published type and the outbox row around them.
func TestAnalyticsEncoder_GoldenWireFormat(t *testing.T) {
	cases := goldenEvents(t)
	if len(cases) != 9 {
		t.Fatalf("the catalogue has nine types, got %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := tc.encode(&AnalyticsEncoder{NewID: fixedID})
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d messages", len(msgs))
			}
			msg := msgs[0]
			if want := analyticsEnvelope(tc.entity, tc.name, tc.subject, tc.time, tc.data); string(msg.Value) != want {
				t.Fatalf("value:\n got %s\nwant %s", msg.Value, want)
			}
			wantType := "com.warehouse.wms.inbound-receiving." + tc.entity + "." + tc.name
			if msg.EventType != wantType || msg.EventID != goldenID || msg.Topic != "warehouse.inbound-receiving.analytics" ||
				string(msg.Key) != tc.key || msg.Subject != tc.subject ||
				msg.DataSchema != "urn:warehouse:inbound-receiving:analytics:"+tc.name+":v1" {
				t.Fatalf("row = %+v", msg)
			}
			if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || msg.Headers[0].Value != "application/cloudevents+json; charset=UTF-8" {
				t.Fatalf("headers = %+v", msg.Headers)
			}
		})
	}
}

// TestFanoutEncoder_TwoRowsOneID: integration row first (byte-identical to
// Encoder's), analytics row second, the same id and type on both, for every
// one of the nine types.
func TestFanoutEncoder_TwoRowsOneID(t *testing.T) {
	for _, tc := range goldenEvents(t) {
		t.Run(tc.name, func(t *testing.T) {
			const id = "11111111-1111-4111-8111-111111111111"
			f := &FanoutEncoder{Integration: &Encoder{}, Analytics: &AnalyticsEncoder{}, NewID: func() string { return id }}
			msgs, err := tc.encode(f)
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 2 {
				t.Fatalf("got %d messages, want 2", len(msgs))
			}
			in, an := msgs[0], msgs[1]
			assertSharedIdentity(t, in, an, id)
			single, err := tc.encode(&Encoder{NewID: func() string { return id }})
			if err != nil {
				t.Fatal(err)
			}
			assertFanoutBytes(t, tc.name, in, an, single[0])
		})
	}
}

func assertSharedIdentity(t *testing.T, in, an outbox.Message, id string) {
	t.Helper()
	if in.EventID != id || an.EventID != id {
		t.Fatalf("ids = %s / %s, want %s on both", in.EventID, an.EventID, id)
	}
	if in.Topic != Topic || an.Topic != AnalyticsTopic {
		t.Fatalf("topics = %s / %s", in.Topic, an.Topic)
	}
	if in.EventType != an.EventType || in.Subject != an.Subject || string(in.Key) != string(an.Key) {
		t.Fatalf("rows differ beyond topic/dataschema: %+v / %+v", in, an)
	}
}

func assertFanoutBytes(t *testing.T, name string, in, an, single outbox.Message) {
	t.Helper()
	if string(in.Value) != string(single.Value) {
		t.Fatalf("integration bytes changed by the fan-out:\n got %s\nwant %s", in.Value, single.Value)
	}
	if want := strings.Replace(string(single.Value), ":events:", ":analytics:", 1); string(an.Value) != want {
		t.Fatalf("analytics bytes:\n got %s\nwant %s", an.Value, want)
	}
	if in.DataSchema != "urn:warehouse:inbound-receiving:events:"+name+":v1" || an.DataSchema != "urn:warehouse:inbound-receiving:analytics:"+name+":v1" {
		t.Fatalf("dataschemas = %s / %s", in.DataSchema, an.DataSchema)
	}
}

func TestFanoutEncoder_OrderIsIntegrationThenAnalyticsPerEvent(t *testing.T) {
	ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	n := 0
	f := &FanoutEncoder{NewID: func() string { n++; return ids[n-1] }}
	msgs, err := f.EncodeReceipt(
		receipt.ReceiptOpened{Header: receiptHeader(at(9, 8, 5))},
		receipt.ReceiptClosed{Header: receiptHeader(at(9, 9, 30)), Discrepancies: []receipt.Discrepancy{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	for i := range 2 {
		in, an := msgs[2*i], msgs[2*i+1]
		if in.Topic != Topic || an.Topic != AnalyticsTopic || in.EventID != ids[i] || an.EventID != ids[i] {
			t.Fatalf("event %d: %+v / %+v", i, in, an)
		}
	}
}

func TestFanoutEncoder_RejectsAnUnpublishedEvent(t *testing.T) {
	f := NewFanoutEncoder()
	if msgs, err := f.EncodeAsn(registeredAsn(t, time.Time{}), unknownAsnEvent{asn.Header{Number: "A", At: at(8, 1, 0)}}); err == nil || msgs != nil {
		t.Fatalf("asn: got %v, %v; want an error and no messages", msgs, err)
	}
	if msgs, err := f.EncodeAppointment(unknownAppointmentEvent{apptHeader(at(8, 1, 0))}); err == nil || msgs != nil {
		t.Fatalf("appointment: got %v, %v", msgs, err)
	}
	if msgs, err := f.EncodeReceipt(unknownReceiptEvent{receiptHeader(at(8, 1, 0))}); err == nil || msgs != nil {
		t.Fatalf("receipt: got %v, %v", msgs, err)
	}
	if _, err := f.EncodeAsn(asn.ASNCancelled{Header: asn.Header{At: at(8, 1, 0)}}); err == nil {
		t.Fatal("an event without an aggregate id must not encode")
	}
}

func TestFanoutEncoder_ZeroValueDefaults(t *testing.T) {
	msgs, err := (&FanoutEncoder{}).EncodeAsn(registeredAsn(t, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Topic != Topic || msgs[1].Topic != AnalyticsTopic || msgs[0].EventID != msgs[1].EventID {
		t.Fatalf("got %+v", msgs)
	}
	if _, err := uuid.Parse(msgs[0].EventID); err != nil {
		t.Fatalf("id %q is not a UUID: %v", msgs[0].EventID, err)
	}
}

func TestFanoutEncoder_TopicOverrides(t *testing.T) {
	f := &FanoutEncoder{Integration: &Encoder{Topic: "itest-events"}, Analytics: &AnalyticsEncoder{Topic: "itest-analytics"}}
	msgs, err := f.EncodeAsn(registeredAsn(t, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Topic != "itest-events" || msgs[1].Topic != "itest-analytics" {
		t.Fatalf("topics = %s / %s", msgs[0].Topic, msgs[1].Topic)
	}
}

func TestAnalyticsEncoder_DefaultsAndTopicOverride(t *testing.T) {
	two := []asn.Event{registeredAsn(t, time.Time{}), registeredAsn(t, time.Time{})}
	msgs, err := (&AnalyticsEncoder{Topic: "itest-analytics"}).EncodeAsn(two...)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Topic != "itest-analytics" || msgs[0].EventID == msgs[1].EventID {
		t.Fatalf("got %+v", msgs)
	}
	if _, err := uuid.Parse(msgs[0].EventID); err != nil {
		t.Fatalf("id %q is not a UUID: %v", msgs[0].EventID, err)
	}
	if m, err := (&AnalyticsEncoder{}).EncodeAsn(registeredAsn(t, time.Time{})); err != nil || m[0].Topic != AnalyticsTopic {
		t.Fatalf("default topic: %v %+v", err, m)
	}
	if _, err := (&AnalyticsEncoder{}).EncodeAppointment(unknownAppointmentEvent{apptHeader(at(8, 1, 0))}); err == nil {
		t.Fatal("an unpublished appointment event must be rejected")
	}
	if _, err := (&AnalyticsEncoder{}).EncodeReceipt(unknownReceiptEvent{receiptHeader(at(8, 1, 0))}); err == nil {
		t.Fatal("an unpublished receipt event must be rejected")
	}
}
