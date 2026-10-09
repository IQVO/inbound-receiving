//go:build integration

// Integration tests for the outbound Kafka publisher against a REAL broker
// (testcontainers, never a hardcoded address, never a skip gate): real
// domain events raised by the real aggregates flow through the production
// Encoder (CloudEvents 1.0) and RelaySink (kafka-go writer, RequireAll
// acks) to a unique throwaway topic, and are consumed back and asserted
// envelope-by-envelope: specversion 1.0, the fleet type namespace
// com.warehouse.<sub>.<ctx>.<entity>.<Event>, id, source, subject and the
// pinned payload. Per this repo's .claude rules the CloudEvents envelope is
// mandatory — this suite is what proves the publisher keeps the contract.
package kafka_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// One Kafka container serves the whole package (booting one per test would
// dominate the run). Every test publishes to its own timestamp-suffixed
// topic so tests never share a log.
var (
	kafkaOnce     sync.Once
	sharedBrokers []string
	kafkaBox      testcontainers.Container
	startErr      error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if kafkaBox != nil {
		if err := testcontainers.TerminateContainer(kafkaBox); err != nil {
			fmt.Fprintf(os.Stderr, "terminate kafka container: %v\n", err)
		}
	}
	os.Exit(code)
}

// brokers boots the shared confluent-local container on first use.
func brokers(t *testing.T) []string {
	t.Helper()
	kafkaOnce.Do(func() {
		ctx := context.Background()
		container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1",
			tckafka.WithClusterID("inbound-receiving-kafka-itest"))
		if err != nil {
			startErr = err
			return
		}
		kafkaBox = container
		sharedBrokers, startErr = container.Brokers(ctx)
	})
	if startErr != nil {
		t.Fatalf("start kafka container: %v", startErr)
	}
	return sharedBrokers
}

// createTopic creates a one-partition topic and waits for its partition
// leader (CreateTopics returns before the broker finished creating it).
func createTopic(t *testing.T, broker, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", broker)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// TestKafkaPublisherCloudEventsContract publishes one event of each
// aggregate stream through the production Encoder + RelaySink and asserts
// every consumed message carries the CloudEvents 1.0 envelope and payload
// the fleet contract pins.
func TestKafkaPublisherCloudEventsContract(t *testing.T) {
	ctx := context.Background()
	bs := brokers(t)
	topic := fmt.Sprintf("warehouse.inbound-receiving.events.itest-%d", time.Now().UnixNano())
	createTopic(t, bs[0], topic)

	// Real domain events from the real aggregates, at a fixed instant.
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	registered, asnEvents, err := asn.Register("ASN-K1", "ACME", at.Add(24*time.Hour),
		[]asn.LineInput{{LineNo: 1, SKU: "SKU-K", ExpectedQty: 10}}, at)
	if err != nil {
		t.Fatalf("register asn: %v", err)
	}
	booked, apptEvents, err := appointment.Book(
		"appt-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f", "DOOR-1", "ACME Freight",
		at.Add(2*time.Hour), at.Add(3*time.Hour), []string{"ASN-K1"}, at)
	if err != nil {
		t.Fatalf("book appointment: %v", err)
	}
	opened, rcptEvents, err := receipt.Open(
		"rcpt-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f", registered.Snapshot(),
		"", "", at)
	if err != nil {
		t.Fatalf("open receipt: %v", err)
	}

	encoder := &kafka.Encoder{Topic: topic}
	asnMsgs, err := encoder.EncodeAsn(asnEvents...)
	if err != nil {
		t.Fatalf("encode asn events: %v", err)
	}
	apptMsgs, err := encoder.EncodeAppointment(apptEvents...)
	if err != nil {
		t.Fatalf("encode appointment events: %v", err)
	}
	rcptMsgs, err := encoder.EncodeReceipt(rcptEvents...)
	if err != nil {
		t.Fatalf("encode receipt events: %v", err)
	}

	sink := kafka.NewRelaySink(bs)
	t.Cleanup(func() { _ = sink.Close() })
	all := append(append(asnMsgs, apptMsgs...), rcptMsgs...)
	if err := sink.Send(ctx, all...); err != nil {
		t.Fatalf("publish: %v", err)
	}

	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: bs, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20,
	})
	t.Cleanup(func() { _ = reader.Close() })

	want := []struct {
		eventType string
		key       string
		subject   string
		payload   map[string]any
	}{
		{
			eventType: cloudevents.Type(kafka.EntityAsn, "ASNRegistered"),
			key:       "ASN-K1", subject: "ASN-K1",
			payload: map[string]any{
				"asn_number": "ASN-K1", "supplier_ref": "ACME",
				"expected_arrival": at.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
				"lines": []any{map[string]any{
					"line_no": 1.0, "sku": "SKU-K", "expected_qty": 10.0,
				}},
			},
		},
		{
			eventType: cloudevents.Type(kafka.EntityAppointment, "DockAppointmentBooked"),
			key:       string(booked.ID()), subject: string(booked.ID()),
			payload: map[string]any{
				"appointment_id": string(booked.ID()), "door_code": "DOOR-1",
				"carrier": "ACME Freight", "asn_numbers": []any{"ASN-K1"},
			},
		},
		{
			eventType: cloudevents.Type(kafka.EntityReceipt, "ReceiptOpened"),
			key:       "ASN-K1", subject: string(opened.ID()),
			payload: map[string]any{
				"receipt_id": string(opened.ID()), "asn_number": "ASN-K1",
			},
		},
	}
	for i, w := range want {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read message %d: %v", i, err)
		}
		assertEnvelope(t, msg, w.eventType, w.key, w.subject, w.payload)
	}
}

// assertEnvelope checks one consumed message against the mandatory
// CloudEvents 1.0 contract: the content-type header, specversion, id,
// source, type, subject and the decoded payload.
func assertEnvelope(t *testing.T, msg kafkago.Message, wantType, wantKey, wantSubject string, wantPayload map[string]any) {
	t.Helper()
	if string(msg.Key) != wantKey {
		t.Errorf("%s: key = %q, want %q", wantType, msg.Key, wantKey)
	}
	if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" ||
		string(msg.Headers[0].Value) != cloudevents.MediaType {
		t.Errorf("%s: headers = %+v, want one content-type %s", wantType, msg.Headers, cloudevents.MediaType)
	}
	e, err := cloudevents.Decode(msg.Value)
	if err != nil {
		t.Fatalf("%s: not a CloudEvents 1.0 event: %v", wantType, err)
	}
	if e.SpecVersion() != cloudevents.SpecVersion {
		t.Errorf("%s: specversion = %q, want %q", wantType, e.SpecVersion(), cloudevents.SpecVersion)
	}
	if e.ID() == "" {
		t.Errorf("%s: CloudEvents id is empty", wantType)
	}
	if e.Source() != cloudevents.Source {
		t.Errorf("%s: source = %q, want %q", wantType, e.Source(), cloudevents.Source)
	}
	if e.Type() != wantType {
		t.Errorf("%s: type = %q, want %q", wantType, e.Type(), wantType)
	}
	if e.Subject() != wantSubject {
		t.Errorf("%s: subject = %q, want %q", wantType, e.Subject(), wantSubject)
	}
	if e.DataSchema() == "" {
		t.Errorf("%s: dataschema is empty", wantType)
	}

	var data map[string]any
	if err := json.Unmarshal(e.Data(), &data); err != nil {
		t.Fatalf("%s: payload is not JSON: %v", wantType, err)
	}
	for field, want := range wantPayload {
		got, ok := data[field]
		if !ok {
			t.Errorf("%s: payload field %q missing (payload %v)", wantType, field, data)
			continue
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: payload %q = %s, want %s", wantType, field, gotJSON, wantJSON)
		}
	}
}
