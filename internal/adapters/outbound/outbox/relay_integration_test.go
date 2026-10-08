//go:build integration

package outbox_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// The relay is proven end to end against real infrastructure, both started
// with testcontainers (never skip-gated, never a hardcoded broker address):
// use cases write aggregates + outbox rows in real Postgres, the relay drains
// them to a real broker on a unique topic, and the CloudEvents are read back.

var (
	kafkaOnce       sync.Once
	sharedBrokers   []string
	sharedContainer testcontainers.Container
	startKafkaErr   error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedContainer != nil {
		if err := testcontainers.TerminateContainer(sharedContainer); err != nil {
			fmt.Fprintf(os.Stderr, "terminate kafka container: %v\n", err)
		}
	}
	os.Exit(code)
}

func startKafka(t *testing.T) []string {
	t.Helper()
	kafkaOnce.Do(func() {
		ctx := context.Background()
		container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("inbound-receiving-relay-itest"))
		if err != nil {
			startKafkaErr = err
			return
		}
		sharedContainer = container
		sharedBrokers, startKafkaErr = container.Brokers(ctx)
	})
	if startKafkaErr != nil {
		t.Fatalf("start kafka container: %v", startKafkaErr)
	}
	return sharedBrokers
}

func startPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_test"),
		tcpostgres.WithUsername("inbound_receiving_test"),
		tcpostgres.WithPassword("inbound_receiving_test"),
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
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	p, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// createTopic creates a one-partition topic and waits for its leader
// (CreateTopics returns before the broker is done).
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

// step is one expected message: the Kafka key is the asn_number for ASN and
// receipt events and the appointment id for appointment events, while the
// CloudEvents subject is always the aggregate instance id.
type step struct {
	eventType, key, subject string
}

func TestRelayRealPostgresAndKafkaPublishesCloudEventsWithTheRightTypeAndKey(t *testing.T) {
	ctx := context.Background()
	brokers := startKafka(t)
	pool := startPostgresPool(t)
	topic := fmt.Sprintf("warehouse.inbound-receiving.events.itest-%d", time.Now().UnixNano())
	createTopic(t, brokers[0], topic)

	w := usecases.Writer{
		Asns: postgres.NewAsnRepo(pool), Appointments: postgres.NewAppointmentRepo(pool), Receipts: postgres.NewReceiptRepo(pool),
		Outbox: postgres.NewOutboxRepo(pool), Encoder: &outboundkafka.Encoder{Topic: topic}, UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: idgen.UUID{},
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-1", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 10}},
	})
	must(err)
	start := time.Now().UTC().Add(2 * time.Hour)
	appt, err := (&usecases.BookAppointment{Writer: w}).Handle(ctx, usecases.BookAppointmentCommand{
		DoorCode: "DOOR-1", Carrier: "ACME Freight", WindowStart: start, WindowEnd: start.Add(time.Hour), AsnNumbers: []string{"ASN-1"},
	})
	must(err)
	rcpt, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	must(err)
	_, err = (&usecases.ReceiveLine{Writer: w}).Handle(ctx, usecases.ReceiveLineCommand{ReceiptID: string(rcpt.ID()), LineNo: 1, Quantity: 8, Condition: "Good"})
	must(err)
	_, err = (&usecases.CloseReceipt{Writer: w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(rcpt.ID())})
	must(err)

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, slog.New(slog.NewTextHandler(io.Discard, nil)), relay.WithInterval(100*time.Millisecond))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })

	reader := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	t.Cleanup(func() { _ = reader.Close() })
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	prefix := "com.warehouse.wms.inbound-receiving."
	want := []step{
		{prefix + "asn.ASNRegistered", "ASN-1", "ASN-1"},
		{prefix + "dockappointment.DockAppointmentBooked", string(appt.ID()), string(appt.ID())},
		{prefix + "receipt.ReceiptOpened", "ASN-1", string(rcpt.ID())},
		{prefix + "receipt.ReceiptLineReceived", "ASN-1", string(rcpt.ID())},
		{prefix + "receipt.ReceiptClosed", "ASN-1", string(rcpt.ID())},
	}
	for i, s := range want {
		msg, err := reader.ReadMessage(readCtx)
		if err != nil {
			t.Fatalf("read message %d: %v", i, err)
		}
		assertCloudEvent(t, msg, s)
	}
	waitAllPublished(t, pool)
}

// assertCloudEvent checks the key, the content-type header and the decoded
// CloudEvents attributes of one consumed message.
func assertCloudEvent(t *testing.T, msg kafkago.Message, want step) {
	t.Helper()
	if string(msg.Key) != want.key {
		t.Errorf("%s: key = %q, want %q", want.eventType, msg.Key, want.key)
	}
	if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || string(msg.Headers[0].Value) != cloudevents.MediaType {
		t.Errorf("%s: headers = %+v", want.eventType, msg.Headers)
	}
	e, err := cloudevents.Decode(msg.Value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Type() != want.eventType || e.Subject() != want.subject || e.Source() != "/warehouse/inbound-receiving" {
		t.Fatalf("event = type %q subject %q source %q, want type %q subject %q", e.Type(), e.Subject(), e.Source(), want.eventType, want.subject)
	}
}

func waitAllPublished(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var unpublished int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
			t.Fatal(err)
		}
		if unpublished == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d outbox rows still unpublished", unpublished)
}
