// Package kafka is the outbound Kafka adapter: it encodes the domain events
// of the Asn, DockAppointment and Receipt aggregates into CloudEvents 1.0
// outbox messages (Encoder) and writes drained outbox rows to the broker
// (RelaySink). Envelopes are built ONLY through
// internal/adapters/kafka/cloudevents. Payloads are exactly the ones pinned
// in apis/asyncapi.yaml.
package kafka

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// Topic is this service's integration-event topic.
const Topic = "warehouse.inbound-receiving.events"

// The `<entity>` segment of every published type:
// com.warehouse.wms.inbound-receiving.<entity>.<EventName>.
const (
	EntityAsn         = "asn"
	EntityAppointment = "dockappointment"
	EntityReceipt     = "receipt"
)

// schemaVersion is the dataschema version of every payload below
// (urn:warehouse:inbound-receiving:events:<EventName>:v1).
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, snake_case JSON). Optional fields are
// omitted when unset.

type asnLineData struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expected_qty"`
}

type asnRegisteredData struct {
	AsnNumber       string        `json:"asn_number"`
	SupplierRef     string        `json:"supplier_ref"`
	ExpectedArrival string        `json:"expected_arrival,omitempty"`
	Lines           []asnLineData `json:"lines"`
}

type asnCancelledData struct {
	AsnNumber string `json:"asn_number"`
	Reason    string `json:"reason,omitempty"`
}

type bookedData struct {
	AppointmentID string   `json:"appointment_id"`
	DoorCode      string   `json:"door_code"`
	Carrier       string   `json:"carrier"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	AsnNumbers    []string `json:"asn_numbers"`
}

type checkedInData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	CheckedInAt   string `json:"checked_in_at"`
}

type appointmentCancelledData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	Reason        string `json:"reason,omitempty"`
}

type completedData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	CompletedAt   string `json:"completed_at"`
}

type receiptOpenedData struct {
	ReceiptID     string `json:"receipt_id"`
	AsnNumber     string `json:"asn_number"`
	AppointmentID string `json:"appointment_id,omitempty"`
	DoorCode      string `json:"door_code,omitempty"`
	OpenedAt      string `json:"opened_at"`
}

type lineReceivedData struct {
	ReceiptID  string `json:"receipt_id"`
	AsnNumber  string `json:"asn_number"`
	LineNo     int    `json:"line_no"`
	SKU        string `json:"sku"`
	Quantity   int64  `json:"quantity"`
	Condition  string `json:"condition"`
	ReceivedAt string `json:"received_at"`
}

type discrepancyData struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expected_qty"`
	ReceivedQty int64  `json:"received_qty"`
	DamagedQty  int64  `json:"damaged_qty"`
}

type receiptClosedData struct {
	ReceiptID     string            `json:"receipt_id"`
	AsnNumber     string            `json:"asn_number"`
	ClosedAt      string            `json:"closed_at"`
	Discrepancies []discrepancyData `json:"discrepancies"`
}

// Encoder implements ports.EventEncoder: each domain event becomes one
// outbox.Message holding the CloudEvents bytes, the Kafka key and the
// content-type header. The CloudEvents `id` is minted HERE, once, and
// persisted with the outbox row, so a relay retry republishes the same id.
type Encoder struct {
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
	// Topic overrides the destination topic (Topic when empty).
	Topic string
}

// NewEncoder returns an Encoder minting random UUID v4 ids.
func NewEncoder() *Encoder { return &Encoder{NewID: uuid.NewString} }

var _ ports.EventEncoder = (*Encoder)(nil)

// row is what the three aggregates' encoders hand to build.
type row struct {
	entity    string
	eventName string
	subject   string
	key       string
	at        time.Time
	data      any
}

// EncodeAsn encodes Asn events in order. An event type it does not publish
// is a programming error and fails the whole call.
func (e *Encoder) EncodeAsn(events ...asn.Event) ([]outbox.Message, error) {
	return encodeAll(e, events, asnRow)
}

// EncodeAppointment encodes DockAppointment events in order.
func (e *Encoder) EncodeAppointment(events ...appointment.Event) ([]outbox.Message, error) {
	return encodeAll(e, events, appointmentRow)
}

// EncodeReceipt encodes Receipt events in order.
func (e *Encoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	return encodeAll(e, events, receiptRow)
}

func encodeAll[T interface{ EventName() string }](e *Encoder, events []T, toRow func(T) (row, error)) ([]outbox.Message, error) {
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	topic := e.Topic
	if topic == "" {
		topic = Topic
	}
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		r, err := toRow(ev)
		if err != nil {
			return nil, err
		}
		msg, err := build(r, newID(), topic)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func build(r row, id, topic string) (outbox.Message, error) {
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        id,
		Entity:    r.entity,
		EventName: r.eventName,
		Subject:   r.subject,
		Time:      r.at,
		Stream:    cloudevents.StreamEvents,
		Version:   schemaVersion,
		Data:      r.data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", r.eventName, err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    id,
		Topic:      topic,
		EventType:  cloudevents.Type(r.entity, r.eventName),
		Subject:    r.subject,
		Key:        []byte(r.key),
		DataSchema: cloudevents.DataSchema(cloudevents.StreamEvents, r.eventName, schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func asnRow(ev asn.Event) (row, error) {
	number := string(ev.AsnNumber())
	r := row{entity: EntityAsn, eventName: ev.EventName(), subject: number, key: number, at: ev.OccurredAt()}
	switch e := ev.(type) {
	case asn.ASNRegistered:
		lines := make([]asnLineData, 0, len(e.Lines))
		for _, l := range e.Lines {
			lines = append(lines, asnLineData{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
		}
		d := asnRegisteredData{AsnNumber: number, SupplierRef: e.SupplierRef, Lines: lines}
		if !e.ExpectedArrival.IsZero() {
			d.ExpectedArrival = ts(e.ExpectedArrival)
		}
		r.data = d
	case asn.ASNCancelled:
		r.data = asnCancelledData{AsnNumber: number, Reason: e.Reason}
	default:
		return row{}, fmt.Errorf("kafka encoder: %s is not a published asn event", ev.EventName())
	}
	return r, nil
}

func appointmentRow(ev appointment.Event) (row, error) {
	id := string(ev.AppointmentID())
	r := row{entity: EntityAppointment, eventName: ev.EventName(), subject: id, key: id, at: ev.OccurredAt()}
	switch e := ev.(type) {
	case appointment.DockAppointmentBooked:
		numbers := make([]string, 0, len(e.AsnNumbers))
		for _, n := range e.AsnNumbers {
			numbers = append(numbers, string(n))
		}
		r.data = bookedData{
			AppointmentID: id, DoorCode: string(e.DoorCode), Carrier: e.Carrier,
			WindowStart: ts(e.Window.Start()), WindowEnd: ts(e.Window.End()), AsnNumbers: numbers,
		}
	case appointment.DockAppointmentCheckedIn:
		r.data = checkedInData{AppointmentID: id, DoorCode: string(e.DoorCode), CheckedInAt: ts(e.CheckedInAt)}
	case appointment.DockAppointmentCancelled:
		r.data = appointmentCancelledData{AppointmentID: id, DoorCode: string(e.DoorCode), Reason: e.Reason}
	case appointment.DockAppointmentCompleted:
		r.data = completedData{AppointmentID: id, DoorCode: string(e.DoorCode), CompletedAt: ts(e.CompletedAt)}
	default:
		return row{}, fmt.Errorf("kafka encoder: %s is not a published dockappointment event", ev.EventName())
	}
	return r, nil
}

func receiptRow(ev receipt.Event) (row, error) {
	id, number := string(ev.ReceiptID()), string(ev.AsnNumber())
	r := row{entity: EntityReceipt, eventName: ev.EventName(), subject: id, key: number, at: ev.OccurredAt()}
	switch e := ev.(type) {
	case receipt.ReceiptOpened:
		r.data = receiptOpenedData{
			ReceiptID: id, AsnNumber: number, AppointmentID: string(e.AppointmentID),
			DoorCode: string(e.DoorCode), OpenedAt: ts(e.At),
		}
	case receipt.ReceiptLineReceived:
		r.data = lineReceivedData{
			ReceiptID: id, AsnNumber: number, LineNo: e.LineNo, SKU: string(e.SKU),
			Quantity: e.Quantity, Condition: string(e.Condition), ReceivedAt: ts(e.At),
		}
	case receipt.ReceiptClosed:
		ds := make([]discrepancyData, 0, len(e.Discrepancies))
		for _, d := range e.Discrepancies {
			ds = append(ds, discrepancyData{
				LineNo: d.LineNo, SKU: string(d.SKU), Kind: string(d.Kind),
				ExpectedQty: d.ExpectedQty, ReceivedQty: d.ReceivedQty, DamagedQty: d.DamagedQty,
			})
		}
		r.data = receiptClosedData{ReceiptID: id, AsnNumber: number, ClosedAt: ts(e.At), Discrepancies: ds}
	default:
		return row{}, fmt.Errorf("kafka encoder: %s is not a published receipt event", ev.EventName())
	}
	return r, nil
}
