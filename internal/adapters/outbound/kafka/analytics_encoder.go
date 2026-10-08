package kafka

import (
	"github.com/google/uuid"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// AnalyticsTopic is this service's analytics topic: the data product's own
// stream, consumed only by cmd/inbound-projector (ADR 0006). Its DLQ is
// AnalyticsTopic + ".dlq".
const AnalyticsTopic = "warehouse.inbound-receiving.analytics"

// AnalyticsEncoder encodes the same nine events as Encoder onto
// AnalyticsTopic, with dataschema
// urn:warehouse:inbound-receiving:analytics:<EventName>:v1. The payloads equal
// the integration payloads (ADR 0006 section 2): the row functions are shared,
// so an event published on one stream can never be missing from the other.
// Alone it mints its own ids (golden tests); production uses FanoutEncoder so
// both messages of one occurrence share one id.
type AnalyticsEncoder struct {
	// NewID mints a CloudEvents id (uuid.NewString when nil).
	NewID func() string
	// Topic overrides AnalyticsTopic (integration tests use a unique one).
	Topic string
}

var _ ports.EventEncoder = (*AnalyticsEncoder)(nil)

// EncodeAsn encodes Asn events in order.
func (e *AnalyticsEncoder) EncodeAsn(events ...asn.Event) ([]outbox.Message, error) {
	return encodeStream(e.NewID, e.Topic, AnalyticsTopic, cloudevents.StreamAnalytics, events, asnRow)
}

// EncodeAppointment encodes DockAppointment events in order.
func (e *AnalyticsEncoder) EncodeAppointment(events ...appointment.Event) ([]outbox.Message, error) {
	return encodeStream(e.NewID, e.Topic, AnalyticsTopic, cloudevents.StreamAnalytics, events, appointmentRow)
}

// EncodeReceipt encodes Receipt events in order.
func (e *AnalyticsEncoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	return encodeStream(e.NewID, e.Topic, AnalyticsTopic, cloudevents.StreamAnalytics, events, receiptRow)
}

// FanoutEncoder is the encoder the use cases get: every domain event becomes
// TWO outbox messages, the integration one first and the analytics one
// second, carrying the SAME CloudEvents id (minted once here, persisted with
// both rows) and the same `type`. Both rows are inserted by the use case's
// single UnitOfWork, so they commit or roll back together with the aggregate;
// a relay retry republishes each row's persisted bytes. outbox_events'
// identity is (event_id, topic), so the shared id is legal.
type FanoutEncoder struct {
	Integration *Encoder
	Analytics   *AnalyticsEncoder
	// NewID mints the shared CloudEvents id (uuid.NewString when nil).
	NewID func() string
}

// NewFanoutEncoder returns a FanoutEncoder over the production topics.
func NewFanoutEncoder() *FanoutEncoder {
	return &FanoutEncoder{Integration: &Encoder{}, Analytics: &AnalyticsEncoder{}, NewID: uuid.NewString}
}

var _ ports.EventEncoder = (*FanoutEncoder)(nil)

// EncodeAsn encodes Asn events: integration message then analytics message
// per event. It fails, and returns no messages, on an event type the streams
// do not publish.
func (f *FanoutEncoder) EncodeAsn(events ...asn.Event) ([]outbox.Message, error) {
	return fanout(f, events, asnRow)
}

// EncodeAppointment encodes DockAppointment events, two messages per event.
func (f *FanoutEncoder) EncodeAppointment(events ...appointment.Event) ([]outbox.Message, error) {
	return fanout(f, events, appointmentRow)
}

// EncodeReceipt encodes Receipt events, two messages per event.
func (f *FanoutEncoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	return fanout(f, events, receiptRow)
}

func fanout[T any](f *FanoutEncoder, events []T, toRow func(T) (row, error)) ([]outbox.Message, error) {
	newID := f.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	integration, analytics := f.Integration, f.Analytics
	if integration == nil {
		integration = &Encoder{}
	}
	if analytics == nil {
		analytics = &AnalyticsEncoder{}
	}
	integrationTopic, analyticsTopic := integration.Topic, analytics.Topic
	if integrationTopic == "" {
		integrationTopic = Topic
	}
	if analyticsTopic == "" {
		analyticsTopic = AnalyticsTopic
	}
	out := make([]outbox.Message, 0, 2*len(events))
	for _, ev := range events {
		r, err := toRow(ev)
		if err != nil {
			return nil, err
		}
		id := newID()
		in, err := build(r, id, integrationTopic, cloudevents.StreamEvents)
		if err != nil {
			return nil, err
		}
		an, err := build(r, id, analyticsTopic, cloudevents.StreamAnalytics)
		if err != nil {
			return nil, err
		}
		out = append(out, in, an)
	}
	return out, nil
}
