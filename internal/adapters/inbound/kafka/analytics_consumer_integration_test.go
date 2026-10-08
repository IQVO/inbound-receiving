//go:build integration

package kafka_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/kafka" // the broker comes from startKafka (testcontainers), shared per package
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// The projector's consumer against real infrastructure (testcontainers only):
// the real AnalyticsEncoder's bytes on a real topic land in a real analytical
// Postgres; garbage is skipped, poison is dead-lettered byte for byte, a
// redelivered id is applied once, and the offsets are committed (a second
// consumer in the same group resumes after them).

func startAnalyticsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_analytics"),
		tcpostgres.WithUsername("analytics"),
		tcpostgres.WithPassword("analytics"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatalf("analytics migrations: %v", err)
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

const (
	itRcpt = "rcpt-123e4567-e89b-12d3-a456-426614174000"
	itAsn  = "ASN-1001"
)

func analyticsMessage(t *testing.T, topic, id string, ev receipt.Event) kafkago.Message {
	t.Helper()
	msgs, err := (&outboundkafka.AnalyticsEncoder{NewID: func() string { return id }, Topic: topic}).EncodeReceipt(ev)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("encode: %d msgs, %v", len(msgs), err)
	}
	return kafkago.Message{Key: msgs[0].Key, Value: msgs[0].Value}
}

// totals sums the receipt-related counts of the days around at.
func totals(t *testing.T, r *analyticsstore.Reader, at time.Time) (closed, lines int) {
	t.Helper()
	days, err := r.PerformanceDays(context.Background(), report.Range{From: at.Add(-48 * time.Hour), To: at.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		closed += d.ReceiptsClosed
		lines += d.LinesReceived
	}
	return closed, lines
}

func TestAnalyticsConsumer_RealKafkaAndPostgres(t *testing.T) {
	brokers := startKafka(t)
	pool := startAnalyticsPool(t)
	stamp := time.Now().UnixNano()
	topic := fmt.Sprintf("warehouse.inbound-receiving.analytics.itest-%d", stamp)
	group := fmt.Sprintf("inbound-receiving-analytics-itest-%d", stamp)
	createTopic(t, brokers[0], topic)
	createTopic(t, brokers[0], topic+inboundkafka.DLQSuffix)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := analyticsstore.NewReader(pool)
	at := time.Now().UTC().Truncate(time.Second)
	header := func(d time.Duration) receipt.Header {
		return receipt.Header{ID: itRcpt, Asn: itAsn, At: at.Add(d)}
	}

	// start runs one group member; the returned stop is idempotent.
	start := func() (stop func()) {
		c := inboundkafka.NewAnalyticsConsumer(brokers, topic, group, analyticsstore.NewProjection(pool), logger)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = c.Run(ctx) }()
		var once sync.Once
		stop = func() { once.Do(func() { cancel(); <-done; _ = c.Close() }) }
		t.Cleanup(stop)
		return stop
	}
	stopFirst := start()

	opened := analyticsMessage(t, topic, "evt-open", receipt.ReceiptOpened{Header: header(0)})
	closed := analyticsMessage(t, topic, "evt-close", receipt.ReceiptClosed{Header: header(time.Minute), Discrepancies: []receipt.Discrepancy{}})
	poison := kafkago.Message{Key: closed.Key, Value: bytes.Replace(closed.Value, []byte(`"subject":"`+itRcpt+`"`), []byte(`"subject":"rcpt-other"`), 1)}
	poison.Value = bytes.Replace(poison.Value, []byte(`"evt-close"`), []byte(`"evt-poison"`), 1)
	publish(t, brokers, topic,
		kafkago.Message{Key: []byte("x"), Value: []byte(`{"event_id":"flat","event_type":"ReceiptClosed"}`)}, // not a CloudEvent: skipped
		opened,
		poison,
		opened, // redelivered id: applied once
		closed, // proves everything before it was consumed
	)

	eventually(t, "the closing event to be projected", func() bool { c, _ := totals(t, reader, at); return c == 1 })
	var applied int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM analytics_processed_events`).Scan(&applied); err != nil || applied != 2 {
		t.Fatalf("applied ids = %d (%v), want 2 (the duplicate and the poison are not applied)", applied, err)
	}
	cur, err := reader.Current(context.Background(), report.TodayWindow(at))
	if err != nil || cur.OpenReceipts != 0 {
		t.Fatalf("current = %+v, %v; the receipt is closed", cur, err)
	}

	assertRawDeadLetter(t, readRaw(t, brokers, topic+inboundkafka.DLQSuffix, 1)[0], poison, topic)

	// Offsets were committed: a second member of the same group, started
	// after the first one left, resumes after them. Were they not, it would
	// re-read the poison and dead-letter it a second time.
	stopFirst()
	start()
	line := analyticsMessage(t, topic, "evt-line", receipt.ReceiptLineReceived{Header: header(30 * time.Second), LineNo: 1, SKU: "SKU-1", Quantity: 3, Condition: receipt.ConditionGood})
	publish(t, brokers, topic, line)
	eventually(t, "the line to be projected", func() bool { _, l := totals(t, reader, at); return l == 1 })
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM analytics_processed_events`).Scan(&applied); err != nil || applied != 3 {
		t.Fatalf("applied ids = %d (%v), want 3", applied, err)
	}
	if n := countRaw(t, brokers, topic+inboundkafka.DLQSuffix, 3*time.Second); n != 1 {
		t.Fatalf("dlq messages = %d, want still 1 (the second member re-read committed offsets)", n)
	}
}

// countRaw counts the messages of partition 0 of topic, reading until idle.
func countRaw(t *testing.T, brokers []string, topic string, idle time.Duration) int {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	defer r.Close()
	n := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), idle)
		_, err := r.ReadMessage(ctx)
		cancel()
		if err != nil {
			return n
		}
		n++
	}
}

// assertRawDeadLetter checks a DLQ message carries the poison's raw bytes and
// key and the x-dlq-* context of its source offset (2).
func assertRawDeadLetter(t *testing.T, got, poison kafkago.Message, topic string) {
	t.Helper()
	if !bytes.Equal(got.Value, poison.Value) || string(got.Key) != itAsn {
		t.Fatalf("dlq message = %s, want the raw poison bytes", got.Value)
	}
	headers := map[string]string{}
	for _, h := range got.Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["x-dlq-source-topic"] != topic || headers["x-dlq-error"] == "" || headers["x-dlq-source-offset"] != "2" {
		t.Fatalf("dlq headers = %v", headers)
	}
}

// readRaw reads n raw messages from partition 0 of topic, from the start.
func readRaw(t *testing.T, brokers []string, topic string, n int) []kafkago.Message {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out := make([]kafkago.Message, 0, n)
	for len(out) < n {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read %d of %d from %s: %v", len(out), n, topic, err)
		}
		out = append(out, m)
	}
	return out
}
