//go:build integration

package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// The whole analytics read side (ADR 0006) against real infrastructure, all
// started with testcontainers (never skip-gated, never a hardcoded broker):
// ASNs, an appointment and receipts are written against the OLTP Postgres
// (both topics' rows in one transaction), the relay drains the outbox to a
// real Kafka (losing one ack, so a row is republished under the SAME id), the
// projector's consumer projects the analytics topic into a SEPARATE
// analytical Postgres, and the reports endpoints answer from it -- while
// legacy, unknown-type, poison and duplicate messages share the topic.

func e2eStartPostgres(t *testing.T, db string) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase(db), tcpostgres.WithUsername("itest"), tcpostgres.WithPassword("itest"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres %s: %v", db, err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func e2eStartKafka(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("inbound-receiving-analytics-e2e"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return brokers
}

func e2eCreateTopic(t *testing.T, broker, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", broker)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// lostAckOnce delivers to the real sink and THEN reports failure once: the
// "published, but the row was not marked" crash window, so the relay
// republishes the first row with the SAME CloudEvents id.
type lostAckOnce struct {
	inner relay.Sink
	mu    sync.Mutex
	fired bool
}

func (f *lostAckOnce) Send(ctx context.Context, msgs ...outbox.Message) error {
	if err := f.inner.Send(ctx, msgs...); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fired {
		f.fired = true
		return errors.New("simulated crash after the broker ack, before the row was marked published")
	}
	return nil
}

// settableClock is the use cases' clock, moved by the test.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settableClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func e2eAt(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

// e2eWrites drives the real use cases over the OLTP database.
type e2eWrites struct {
	t     *testing.T
	w     usecases.Writer
	clock *settableClock
}

func (e e2eWrites) at(when time.Time, what string, run func(ctx context.Context) error) {
	e.t.Helper()
	e.clock.set(when)
	if err := run(context.Background()); err != nil {
		e.t.Fatalf("%s: %v", what, err)
	}
}

func (e e2eWrites) registerAsn(number string, arrival, when time.Time, lines ...usecases.AsnLineInput) {
	e.t.Helper()
	e.at(when, "register "+number, func(ctx context.Context) error {
		_, err := (&usecases.RegisterAsn{Writer: e.w}).Handle(ctx, usecases.RegisterAsnCommand{
			AsnNumber: number, SupplierRef: "ACME", ExpectedArrival: arrival, Lines: lines})
		return err
	})
}

func (e e2eWrites) cancelAsn(number string, when time.Time) {
	e.t.Helper()
	e.at(when, "cancel "+number, func(ctx context.Context) error {
		_, err := (&usecases.CancelAsn{Writer: e.w}).Handle(ctx, usecases.CancelAsnCommand{AsnNumber: number, Reason: "supplier"})
		return err
	})
}

func lineOf(no int, sku string, qty int64) usecases.AsnLineInput {
	return usecases.AsnLineInput{LineNo: no, SKU: sku, ExpectedQty: qty}
}

type e2eReport struct {
	Days    []map[string]any `json:"days"`
	Current map[string]any   `json:"current"`
}

func TestAnalyticsReadSide_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	brokers := e2eStartKafka(t)
	oltpURL := e2eStartPostgres(t, "inbound_receiving_test")
	analyticalURL := e2eStartPostgres(t, "inbound_receiving_analytics")
	if err := postgres.RunMigrations(oltpURL); err != nil {
		t.Fatal(err)
	}
	if err := analyticsstore.RunMigrations(analyticalURL); err != nil {
		t.Fatal(err)
	}
	oltp, err := postgres.NewPool(ctx, oltpURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oltp.Close)

	nonce := time.Now().UnixNano()
	integrationTopic := fmt.Sprintf("warehouse.inbound-receiving.events.e2e-%d", nonce)
	analyticsTopic := fmt.Sprintf("warehouse.inbound-receiving.analytics.e2e-%d", nonce)
	for _, topic := range []string{integrationTopic, analyticsTopic, analyticsTopic + inboundkafka.DLQSuffix} {
		e2eCreateTopic(t, brokers[0], topic)
	}

	// ---- OLTP writes: both topics' rows in ONE transaction per change ----
	ob := postgres.NewOutboxRepo(oltp)
	clock := &settableClock{}
	writes := e2eWrites{t: t, clock: clock, w: usecases.Writer{
		Asns: postgres.NewAsnRepo(oltp), Appointments: postgres.NewAppointmentRepo(oltp), Receipts: postgres.NewReceiptRepo(oltp),
		Outbox: ob, UoW: postgres.NewUnitOfWork(oltp), Clock: clock, IDs: idgen.UUID{},
		Encoder: &outboundkafka.FanoutEncoder{
			Integration: &outboundkafka.Encoder{Topic: integrationTopic},
			Analytics:   &outboundkafka.AnalyticsEncoder{Topic: analyticsTopic},
			NewID:       uuid.NewString,
		},
	}}
	e2eScenario(t, writes)
	if n := countRows(t, oltp, `SELECT count(*) FROM outbox_events WHERE topic = $1`, analyticsTopic); n != 13 {
		t.Fatalf("analytics outbox rows = %d, want 13 (one per event)", n)
	}
	if n := countRows(t, oltp, `SELECT count(*) FROM outbox_events WHERE topic = $1`, integrationTopic); n != 13 {
		t.Fatalf("integration outbox rows = %d, want 13", n)
	}

	// ---- the topic also carries messages the projector must survive ----
	producer := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: analyticsTopic, BatchTimeout: 10 * time.Millisecond, RequiredAcks: kafkago.RequireAll}
	t.Cleanup(func() { _ = producer.Close() })
	legacy := []byte(`{"event_id":"e-1","event_type":"ReceiptClosed","occurred_at":"2026-10-05T08:00:00Z","payload":{"receipt_id":"x"}}`)
	unknown := hand(t, "evt-unknown", "com.warehouse.wms.inbound-receiving.receipt.ReceiptReopened", "rcpt-U", e2eAt(5, 13, 0),
		map[string]any{"receipt_id": "rcpt-U", "asn_number": "ASN-U"})
	poison := hand(t, "evt-poison", "com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived", "rcpt-P", e2eAt(5, 13, 0),
		map[string]any{"receipt_id": "rcpt-P", "asn_number": "ASN-P", "line_no": 1, "sku": "S", "quantity": 0, "condition": "Good"}) // zero units
	if err := producer.WriteMessages(ctx,
		kafkago.Message{Key: []byte("x"), Value: legacy}, kafkago.Message{Key: []byte("ASN-U"), Value: unknown},
		kafkago.Message{Key: []byte("ASN-P"), Value: poison}); err != nil {
		t.Fatal(err)
	}

	// ---- relay -> real Kafka (with one lost ack: a republish under the SAME id) ----
	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := relay.NewRelay(ob, &lostAckOnce{inner: sink}, quiet, relay.WithInterval(50*time.Millisecond))
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-relayDone })

	// ---- the projector: consumer + writer pool over the analytical database ----
	writer, err := analyticsstore.NewPool(ctx, analyticalURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	consumer := inboundkafka.NewAnalyticsConsumer(brokers, analyticsTopic, fmt.Sprintf("inbound-projector-e2e-%d", nonce),
		analyticsstore.NewProjection(writer), quiet)
	consumer.Retry = inboundkafka.RetryPolicy{Initial: 50 * time.Millisecond, Max: 200 * time.Millisecond}
	consumerDone := make(chan struct{})
	go func() { defer close(consumerDone); _ = consumer.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-consumerDone; _ = consumer.Close() })

	// ---- the reports service over a READ-ONLY pool ----
	readOnly, err := analyticsstore.NewReadOnlyPool(ctx, analyticalURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readOnly.Close)
	srv := httptest.NewServer(inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{
		Reader: analyticsstore.NewReader(readOnly), Now: func() time.Time { return e2eAt(6, 12, 0) }}))
	t.Cleanup(srv.Close)
	report := func() e2eReport {
		var out e2eReport
		getJSON(t, srv.URL+"/reports/receiving-performance?from=2026-10-05T00:00:00Z&to=2026-10-07T00:00:00Z", &out)
		return out
	}
	waitFor(t, 90*time.Second, "all thirteen events projected", func() bool {
		var f struct {
			AsOf *time.Time `json:"as_of"`
		}
		getJSON(t, srv.URL+"/reports/freshness", &f)
		return f.AsOf != nil && f.AsOf.Equal(e2eAt(6, 11, 0))
	})

	assertReceivingPerformance(t, report())
	assertOnlyThePoisonWasDeadLettered(t, brokers, analyticsTopic+inboundkafka.DLQSuffix, poison)

	// ---- a redelivered analytics message (same id) changes nothing; a later marker proves it was consumed ----
	var dup []byte
	if err := oltp.QueryRow(ctx, `SELECT value FROM outbox_events WHERE topic = $1 AND event_type LIKE '%ReceiptClosed' ORDER BY id LIMIT 1`, analyticsTopic).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	marker := hand(t, "evt-marker", "com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked", "appt-marker", e2eAt(6, 11, 30),
		map[string]any{"appointment_id": "appt-marker", "door_code": "D1"})
	if err := producer.WriteMessages(ctx, kafkago.Message{Key: []byte("ASN-1"), Value: dup}, kafkago.Message{Key: []byte("appt-marker"), Value: marker}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the marker appointment projected", func() bool {
		apps, _ := report().Days[1]["appointments"].(map[string]any)
		return apps["booked"] == 1.0
	})
	if closed := report().Days[1]["receipts_closed"]; closed != 1.0 {
		t.Errorf("after a redelivered ReceiptClosed day 6 receipts_closed = %v, want still 1 (idempotent on the CloudEvents id)", closed)
	}

	// ---- every outbox row was published (the lost ack was retried, not lost) ----
	waitFor(t, 30*time.Second, "outbox drained", func() bool {
		return countRows(t, oltp, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
}

// e2eScenario is the receiving story the report is asserted against.
//
//	day 5: ASN-1 (10 x SKU-1, 5 x SKU-2) expected day 6 08:00; ASN-2 (4 x SKU-3) expected day 6 09:00;
//	       ASN-3 expected day 6 11:00 and cancelled; one appointment booked for ASN-1 (day 6 08:00-10:00).
//	day 6: the carrier checks in 07:50, ASN-1's receipt opens 08:05, receives 8 good + 1 damaged of line 1
//	       and 7 good of line 2, closes 09:30 (Short + Damaged on line 1, Over on line 2; the appointment
//	       completes); ASN-2 is received as a walk-in from 11:00 and is still open.
func e2eScenario(t *testing.T, w e2eWrites) {
	t.Helper()
	w.registerAsn("ASN-1", e2eAt(6, 8, 0), e2eAt(5, 8, 0), lineOf(1, "SKU-1", 10), lineOf(2, "SKU-2", 5))
	w.registerAsn("ASN-2", e2eAt(6, 9, 0), e2eAt(5, 8, 5), lineOf(1, "SKU-3", 4))
	w.registerAsn("ASN-3", e2eAt(6, 11, 0), e2eAt(5, 8, 10), lineOf(1, "SKU-4", 1))
	w.cancelAsn("ASN-3", e2eAt(5, 9, 0))
	var apptID string
	w.at(e2eAt(5, 9, 30), "book", func(ctx context.Context) error {
		d, err := (&usecases.BookAppointment{Writer: w.w}).Handle(ctx, usecases.BookAppointmentCommand{
			DoorCode: "D1", Carrier: "ACME Freight", WindowStart: e2eAt(6, 8, 0), WindowEnd: e2eAt(6, 10, 0), AsnNumbers: []string{"ASN-1"}})
		if d != nil {
			apptID = string(d.ID())
		}
		return err
	})
	w.at(e2eAt(6, 7, 50), "check in", func(ctx context.Context) error {
		_, err := (&usecases.CheckInAppointment{Writer: w.w}).Handle(ctx, usecases.AppointmentActionCommand{AppointmentID: apptID})
		return err
	})
	var receiptID string
	var version int64
	w.at(e2eAt(6, 8, 5), "open receipt", func(ctx context.Context) error {
		r, err := (&usecases.OpenReceipt{Writer: w.w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1", AppointmentID: apptID})
		if r != nil {
			receiptID, version = string(r.ID()), r.Version()
		}
		return err
	})
	receive := func(when time.Time, lineNo int, qty int64, condition string) {
		w.at(when, fmt.Sprintf("receive line %d", lineNo), func(ctx context.Context) error {
			r, err := (&usecases.ReceiveLine{Writer: w.w}).Handle(ctx, usecases.ReceiveLineCommand{
				ReceiptID: receiptID, LineNo: lineNo, Quantity: qty, Condition: condition, ExpectedVersion: version})
			if r != nil {
				version = r.Version()
			}
			return err
		})
	}
	receive(e2eAt(6, 8, 10), 1, 8, "Good")
	receive(e2eAt(6, 8, 20), 1, 1, "Damaged")
	receive(e2eAt(6, 8, 30), 2, 7, "Good")
	w.at(e2eAt(6, 9, 30), "close receipt", func(ctx context.Context) error {
		_, err := (&usecases.CloseReceipt{Writer: w.w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: receiptID, ExpectedVersion: version})
		return err
	})
	w.at(e2eAt(6, 11, 0), "walk-in receipt", func(ctx context.Context) error {
		_, err := (&usecases.OpenReceipt{Writer: w.w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-2"})
		return err
	})
}

func assertReceivingPerformance(t *testing.T, got e2eReport) {
	t.Helper()
	zeroTiming := map[string]any{"count": 0.0, "p50_seconds": nil, "p95_seconds": nil}
	wantDays := []map[string]any{
		{
			"day": "2026-10-05", "receipts_closed": 0.0, "lines_received": 0.0, "units_good": 0.0, "units_damaged": 0.0,
			"discrepancies": map[string]any{"short": 0.0, "over": 0.0, "damaged": 0.0},
			"receipt_cycle": zeroTiming,
			"appointments":  map[string]any{"booked": 1.0, "checked_in": 0.0, "cancelled": 0.0, "completed": 0.0},
			"dock_dwell":    zeroTiming,
		},
		{
			"day": "2026-10-06", "receipts_closed": 1.0, "lines_received": 3.0, "units_good": 15.0, "units_damaged": 1.0,
			"discrepancies": map[string]any{"short": 1.0, "over": 1.0, "damaged": 1.0},
			"receipt_cycle": map[string]any{"count": 1.0, "p50_seconds": 5100.0, "p95_seconds": 5100.0},
			"appointments":  map[string]any{"booked": 0.0, "checked_in": 1.0, "cancelled": 0.0, "completed": 1.0},
			"dock_dwell":    map[string]any{"count": 1.0, "p50_seconds": 6000.0, "p95_seconds": 6000.0},
		},
	}
	if !reflect.DeepEqual(got.Days, wantDays) {
		t.Errorf("days = %v\nwant %v", got.Days, wantDays)
	}
	wantCurrent := map[string]any{"open_receipts": 1.0, "expected_today": 2.0, "expected_today_started": 2.0}
	if !reflect.DeepEqual(got.Current, wantCurrent) {
		t.Errorf("current = %v\nwant %v", got.Current, wantCurrent)
	}
}

// assertOnlyThePoisonWasDeadLettered reads the DLQ: exactly the poison bytes,
// nothing for the legacy or unknown-type messages.
func assertOnlyThePoisonWasDeadLettered(t *testing.T, brokers []string, dlqTopic string, poison []byte) {
	t.Helper()
	dlq := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: dlqTopic, Partition: 0, MinBytes: 1, MaxBytes: 10e6})
	defer dlq.Close()
	readCtx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	m, err := dlq.ReadMessage(readCtx)
	if err != nil {
		t.Fatalf("nothing reached the DLQ: %v", err)
	}
	if !bytes.Equal(m.Value, poison) {
		t.Errorf("DLQ value differs from the poison message:\n%s", m.Value)
	}
	quietCtx, quietStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer quietStop()
	if extra, err := dlq.ReadMessage(quietCtx); err == nil {
		t.Errorf("a second message reached the DLQ (legacy and unknown types are skipped, not dead-lettered): %s", extra.Value)
	}
}

func hand(t *testing.T, id, typ, subject string, at time.Time, data map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": "/warehouse/inbound-receiving", "type": typ, "subject": subject,
		"datacontenttype": "application/json", "time": at.Format(time.RFC3339), "data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // test helper against a local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
