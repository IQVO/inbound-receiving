//go:build integration

// Gap fix D27 (ADR 0009): the W3C trace context of the transaction that
// wrote an outbox row crosses the outbox to Kafka. Real Postgres and a real
// Kafka broker, both through testcontainers (never KAFKA_BROKERS or
// localhost).
package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// installW3CTracing installs a real SDK tracer provider and the W3C
// trace-context propagator globally (what telemetry.Setup does in
// production), restoring the previous globals when the test ends.
func installW3CTracing(t *testing.T) trace.Tracer {
	t.Helper()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return tp.Tracer("outbox-trace-itest")
}

// startTraceKafka boots a disposable broker for this file's tests (its own
// testcontainer, like the fleet reference) and pre-creates topic so the
// relay's first write does not race topic auto-creation.
func startTraceKafka(t *testing.T, ctx context.Context, topic string) []string {
	t.Helper()
	container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1",
		tckafka.WithClusterID("inbound-receiving-outbox-trace-itest"))
	if err != nil {
		t.Fatalf("start Kafka container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate Kafka container: %v", err)
		}
	})
	brokers, err := container.Brokers(ctx)
	if err != nil {
		t.Fatalf("resolve Kafka brokers: %v", err)
	}
	createTopic(t, brokers[0], topic)
	return brokers
}

func readOne(t *testing.T, ctx context.Context, brokers []string, topic string) kafkago.Message {
	t.Helper()
	reader := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	defer func() { _ = reader.Close() }()
	msg, err := reader.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("read %s: %v", topic, err)
	}
	return msg
}

func headerValue(headers []kafkago.Header, key string) (string, bool) {
	for _, h := range headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// storedTraceparent returns the traceparent stored in outbox_events.headers
// of the only row on topic ("" when absent).
func storedTraceparent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, topic string) string {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT headers FROM outbox_events WHERE topic = $1`, topic).Scan(&raw); err != nil {
		t.Fatalf("read stored headers: %v", err)
	}
	var hs []map[string]string
	if err := json.Unmarshal(raw, &hs); err != nil {
		t.Fatalf("decode stored headers %s: %v", raw, err)
	}
	for _, h := range hs {
		if h["key"] == "traceparent" {
			return h["value"]
		}
	}
	return ""
}

// TestOutboxTrace_TraceparentCrossesTheOutboxToKafka: a use case runs inside
// a request span; the outbox row stores that span's traceparent in
// outbox_events.headers; the relay (a later pass, no span of its own, a
// background context) sends it as a Kafka header; and a consumer extracting
// it with the global propagator lands on the request's trace and span.
func TestOutboxTrace_TraceparentCrossesTheOutboxToKafka(t *testing.T) {
	tracer := installW3CTracing(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	topic := fmt.Sprintf("warehouse.inbound-receiving.events.trace-itest-%d", time.Now().UnixNano())
	brokers := startTraceKafka(t, ctx, topic)
	pool := startPostgresPool(t)

	w := usecases.Writer{
		Asns: postgres.NewAsnRepo(pool), Appointments: postgres.NewAppointmentRepo(pool), Receipts: postgres.NewReceiptRepo(pool),
		Outbox: postgres.NewOutboxRepo(pool), Encoder: &outboundkafka.Encoder{Topic: topic}, UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
	reqCtx, reqSpan := tracer.Start(ctx, "POST /asns", trace.WithSpanKind(trace.SpanKindServer))
	_, err := (&usecases.RegisterAsn{Writer: w}).Handle(reqCtx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-TRACE-1", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}},
	})
	reqSpan.End()
	if err != nil {
		t.Fatalf("register asn: %v", err)
	}
	want := reqSpan.SpanContext()
	wantTraceparent := fmt.Sprintf("00-%s-%s-01", want.TraceID(), want.SpanID())
	if got := storedTraceparent(t, ctx, pool, topic); got != wantTraceparent {
		t.Fatalf("outbox row traceparent = %q, want the request span's %q", got, wantTraceparent)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, slog.New(slog.NewTextHandler(io.Discard, nil)), relay.WithMaxAttempts(20))
	// The relay runs without the request's context: continuity must come
	// from the stored row, not from the caller.
	if n, err := r.RelayOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("relay: n=%d err=%v", n, err)
	}

	msg := readOne(t, ctx, brokers, topic)
	got, ok := headerValue(msg.Headers, "traceparent")
	if !ok || got != wantTraceparent {
		t.Fatalf("Kafka traceparent = %q (present=%v), want %q", got, ok, wantTraceparent)
	}
	if ct, _ := headerValue(msg.Headers, "content-type"); ct == "" {
		t.Fatalf("content-type header lost on the outbox path: %+v", msg.Headers)
	}
	consumerCtx := otel.GetTextMapPropagator().Extract(context.Background(), kafkaHeaderCarrier(msg.Headers))
	parent := trace.SpanContextFromContext(consumerCtx)
	if parent.TraceID() != want.TraceID() || parent.SpanID() != want.SpanID() || !parent.IsRemote() {
		t.Fatalf("consumer would parent on %s/%s (remote=%v), want the request span %s/%s",
			parent.TraceID(), parent.SpanID(), parent.IsRemote(), want.TraceID(), want.SpanID())
	}
}

// TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged pins the other half
// of D27: a row written with no trace context (no active span, or a row that
// predates trace capture) relays with exactly its stored headers, key and
// value, and carries no traceparent a consumer could mis-parent onto.
func TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged(t *testing.T) {
	installW3CTracing(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	topic := fmt.Sprintf("warehouse.inbound-receiving.itest-no-trace-%d", time.Now().UnixNano())
	brokers := startTraceKafka(t, ctx, topic)
	pool := startPostgresPool(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox_events (event_id, topic, event_type, subject, key, dataschema, value)
		VALUES ('evt-untraced', $1, 'com.example.Untraced', 'ASN-UNTRACED', 'ASN-UNTRACED', 'urn:test', '{}')
	`, topic); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, slog.New(slog.NewTextHandler(io.Discard, nil)), relay.WithMaxAttempts(20))
	if n, err := r.RelayOnce(ctx); err != nil || n != 1 {
		t.Fatalf("relay: n=%d err=%v", n, err)
	}

	msg := readOne(t, ctx, brokers, topic)
	if string(msg.Key) != "ASN-UNTRACED" || string(msg.Value) != `{}` {
		t.Fatalf("row relayed changed: key=%q value=%q", msg.Key, msg.Value)
	}
	if len(msg.Headers) != 0 {
		t.Fatalf("a row without stored headers must relay with none, got %+v", msg.Headers)
	}
}

// kafkaHeaderCarrier adapts kafka-go headers to the OTel text-map carrier.
type kafkaHeaderCarrier []kafkago.Header

func (c kafkaHeaderCarrier) Get(key string) string {
	v, _ := headerValue(c, key)
	return v
}

func (c kafkaHeaderCarrier) Set(string, string) {}

func (c kafkaHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for _, h := range c {
		keys = append(keys, h.Key)
	}
	return keys
}
