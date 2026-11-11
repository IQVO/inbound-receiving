package kafka

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const (
	goldenID = "3f8f6c2e-9b1a-4d6e-8a52-0c7d1e4b9a10"
	apptID   = "appt-123e4567-e89b-12d3-a456-426614174000"
	rcptID   = "rcpt-223e4567-e89b-12d3-a456-426614174000"
)

func fixedID() string { return goldenID }

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

func envelope(entity, eventName, subject, timeStr, data string) string {
	return `{"specversion":"1.0","id":"` + goldenID + `","source":"/warehouse/inbound-receiving",` +
		`"type":"com.warehouse.wms.inbound-receiving.` + entity + `.` + eventName + `","subject":"` + subject + `",` +
		`"datacontenttype":"application/json","dataschema":"urn:warehouse:inbound-receiving:events:` + eventName + `:v1",` +
		`"time":"` + timeStr + `","data":` + data + `}`
}

func registeredAsn(t *testing.T, arrival time.Time) asn.Event {
	t.Helper()
	_, events, err := asn.Register("ASN-1001", "ACME", arrival, []asn.LineInput{
		{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}, {LineNo: 2, SKU: "SKU-2", ExpectedQty: 5},
	}, at(8, 12, 0))
	if err != nil {
		t.Fatal(err)
	}
	return events[0]
}

func receiptHeader(h time.Time) receipt.Header {
	return receipt.Header{ID: rcptID, Asn: "ASN-1001", At: h}
}

// assertRow checks the exact CloudEvents bytes and the outbox row around
// them: full type, topic, subject, Kafka key, dataschema, id and the
// content-type header.
func assertRow(t *testing.T, msg outbox.Message, shortType, subject, key, want string) {
	t.Helper()
	if string(msg.Value) != want {
		t.Fatalf("value:\n got %s\nwant %s", msg.Value, want)
	}
	name := shortType[strings.Index(shortType, ".")+1:]
	wantType := "com.warehouse.wms.inbound-receiving." + shortType
	if msg.EventType != wantType || msg.EventID != goldenID || msg.Topic != "warehouse.inbound-receiving.events" ||
		string(msg.Key) != key || msg.Subject != subject ||
		msg.DataSchema != "urn:warehouse:inbound-receiving:events:"+name+":v1" {
		t.Fatalf("row = %+v", msg)
	}
	if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || msg.Headers[0].Value != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("headers = %+v", msg.Headers)
	}
}

func TestEncoderGoldenAsnEvents(t *testing.T) {
	enc := &Encoder{NewID: fixedID}
	registered, err := enc.EncodeAsn(registeredAsn(t, at(9, 8, 0)))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := enc.EncodeAsn(asn.ASNCancelled{Header: asn.Header{Number: "ASN-1001", At: at(8, 12, 30)}, Reason: "Supplier cancelled the shipment"})
	if err != nil {
		t.Fatal(err)
	}
	assertRow(t, registered[0], "asn.ASNRegistered", "ASN-1001", "ASN-1001", envelope("asn", "ASNRegistered", "ASN-1001", "2026-10-08T12:00:00Z",
		`{"asn_number":"ASN-1001","supplier_ref":"ACME","expected_arrival":"2026-10-09T08:00:00Z","lines":[{"line_no":1,"sku":"SKU-1","expected_qty":40},{"line_no":2,"sku":"SKU-2","expected_qty":5}]}`))
	assertRow(t, cancelled[0], "asn.ASNCancelled", "ASN-1001", "ASN-1001", envelope("asn", "ASNCancelled", "ASN-1001", "2026-10-08T12:30:00Z",
		`{"asn_number":"ASN-1001","reason":"Supplier cancelled the shipment"}`))
}

func TestEncoderGoldenReceiptEvents(t *testing.T) {
	enc := &Encoder{NewID: fixedID}
	cases := []struct {
		name  string
		event receipt.Event
		time  string
		data  string
	}{
		{
			"ReceiptOpened",
			receipt.ReceiptOpened{Header: receiptHeader(at(9, 8, 5)), AppointmentID: apptID, DoorCode: "WH1-DOCK-IN-01"}, "2026-10-09T08:05:00Z",
			`{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","appointment_id":"` + apptID + `","door_code":"WH1-DOCK-IN-01","opened_at":"2026-10-09T08:05:00Z"}`,
		},
		{
			"ReceiptLineReceived",
			receipt.ReceiptLineReceived{Header: receiptHeader(at(9, 8, 10)), LineNo: 1, SKU: "SKU-1", Quantity: 40, Condition: receipt.ConditionGood}, "2026-10-09T08:10:00Z",
			`{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","line_no":1,"sku":"SKU-1","quantity":40,"condition":"Good","received_at":"2026-10-09T08:10:00Z"}`,
		},
		{
			"ReceiptClosed",
			receipt.ReceiptClosed{Header: receiptHeader(at(9, 9, 30)), Discrepancies: []receipt.Discrepancy{
				{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindShort, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
				{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindDamaged, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
				{LineNo: 2, SKU: "SKU-2", Kind: receipt.KindOver, ExpectedQty: 5, ReceivedQty: 7, DamagedQty: 0},
			}}, "2026-10-09T09:30:00Z",
			`{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","closed_at":"2026-10-09T09:30:00Z","discrepancies":[` +
				`{"line_no":1,"sku":"SKU-1","kind":"Short","expected_qty":40,"received_qty":34,"damaged_qty":4,"w13_code":"02","cbv_disposition":"mismatch_quantity"},` +
				`{"line_no":1,"sku":"SKU-1","kind":"Damaged","expected_qty":40,"received_qty":34,"damaged_qty":4,"w13_code":"01","cbv_disposition":"damaged"},` +
				`{"line_no":2,"sku":"SKU-2","kind":"Over","expected_qty":5,"received_qty":7,"damaged_qty":0,"w13_code":"03","cbv_disposition":"mismatch_quantity"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := enc.EncodeReceipt(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			// subject = receipt id, Kafka key = ASN number
			assertRow(t, msgs[0], "receipt."+tc.name, rcptID, "ASN-1001", envelope("receipt", tc.name, rcptID, tc.time, tc.data))
		})
	}
}

func TestEncoderOptionalFieldsAreOmitted(t *testing.T) {
	enc := &Encoder{NewID: fixedID}
	registered, err := enc.EncodeAsn(registeredAsn(t, time.Time{}), asn.ASNCancelled{Header: asn.Header{Number: "ASN-1001", At: at(8, 1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := enc.EncodeReceipt(
		receipt.ReceiptOpened{Header: receiptHeader(at(9, 8, 5))},
		receipt.ReceiptClosed{Header: receiptHeader(at(9, 9, 30)), Discrepancies: []receipt.Discrepancy{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		string(registered[0].Value): `"data":{"asn_number":"ASN-1001","supplier_ref":"ACME","lines":[{"line_no":1,"sku":"SKU-1","expected_qty":40},{"line_no":2,"sku":"SKU-2","expected_qty":5}]}}`,
		string(registered[1].Value): `"data":{"asn_number":"ASN-1001"}}`,
		string(opened[0].Value):     `"data":{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","opened_at":"2026-10-09T08:05:00Z"}}`,
		string(opened[1].Value):     `"data":{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","closed_at":"2026-10-09T09:30:00Z","discrepancies":[]}}`,
	}
	for got, want := range wants {
		if !strings.HasSuffix(got, want) {
			t.Errorf("message = %s\nwant suffix %s", got, want)
		}
	}
}

func TestEncoderKeepsFractionalSecondsInRFC3339(t *testing.T) {
	enc := &Encoder{NewID: fixedID}
	msgs, err := enc.EncodeReceipt(receipt.ReceiptOpened{Header: receiptHeader(at(9, 8, 5).Add(500 * time.Millisecond))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msgs[0].Value), `"opened_at":"2026-10-09T08:05:00.5Z"`) {
		t.Fatalf("value = %s", msgs[0].Value)
	}
}

type unknownAsnEvent struct{ asn.Header }

func (unknownAsnEvent) EventName() string { return "Unknown" }

type unknownReceiptEvent struct{ receipt.Header }

func (unknownReceiptEvent) EventName() string { return "Unknown" }

func TestEncoderRejectsAnUnpublishedEvent(t *testing.T) {
	enc := NewEncoder()
	if msgs, err := enc.EncodeAsn(registeredAsn(t, time.Time{}), unknownAsnEvent{asn.Header{Number: "A", At: at(8, 1, 0)}}); err == nil || msgs != nil {
		t.Fatalf("asn: got %v, %v; want an error and no messages", msgs, err)
	}
	if msgs, err := enc.EncodeReceipt(unknownReceiptEvent{receiptHeader(at(8, 1, 0))}); err == nil || msgs != nil {
		t.Fatalf("receipt: got %v, %v", msgs, err)
	}
}

func TestEncoderRejectsAnEmptySubject(t *testing.T) {
	if _, err := NewEncoder().EncodeAsn(asn.ASNCancelled{Header: asn.Header{At: at(8, 1, 0)}}); err == nil {
		t.Fatal("an event without an aggregate id must not encode")
	}
}

func TestEncoderDefaultsAndTopicOverride(t *testing.T) {
	two := []asn.Event{registeredAsn(t, time.Time{}), registeredAsn(t, time.Time{})}
	msgs, err := (&Encoder{Topic: "itest-topic"}).EncodeAsn(two...)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Topic != "itest-topic" {
		t.Fatalf("topic = %q", msgs[0].Topic)
	}
	if _, err := uuid.Parse(msgs[0].EventID); err != nil || msgs[0].EventID == msgs[1].EventID {
		t.Fatalf("ids %q %q must be distinct UUIDs (%v)", msgs[0].EventID, msgs[1].EventID, err)
	}
	if !strings.Contains(string(msgs[0].Value), `"id":"`+msgs[0].EventID+`"`) {
		t.Fatal("the row id must be the CloudEvents id")
	}
}
