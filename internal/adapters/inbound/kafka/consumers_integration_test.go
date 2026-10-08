//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// End to end against real infrastructure (testcontainers only): sibling-shaped
// CloudEvents on real topics are consumed into the local copies in real
// Postgres; invalid and foreign messages are skipped; a redelivered id is
// deduped.

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
		container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("inbound-receiving-consumers-itest"))
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
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// createTopic creates a one-partition topic and waits for its leader.
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

func event(t *testing.T, id, source, typ, subject string, data map[string]any) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(source)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Now().UTC())
	e.SetDataSchema("urn:warehouse:itest:events:X:v1")
	if err := e.SetData("application/json", data); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func publish(t *testing.T, brokers []string, topic string, msgs ...kafkago.Message) {
	t.Helper()
	// A fresh Transport per writer: kafka-go's DefaultTransport is shared by
	// every Writer in the process and caches cluster metadata, so a topic
	// created by this test would look unknown.
	transport := &kafkago.Transport{}
	defer transport.CloseIdleConnections()
	w := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond, Transport: transport}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func runInBackground(t *testing.T, run func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: not reached within 60s", what)
}

func TestProductConsumerEndToEnd(t *testing.T) {
	brokers := startKafka(t)
	pool := startPostgresPool(t)
	topic := fmt.Sprintf("warehouse.product-master.events.itest-%d", time.Now().UnixNano())
	createTopic(t, brokers[0], topic)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	skus := postgres.NewKnownSkus(pool)
	uc := &usecases.ApplyProductRegistered{UoW: postgres.NewUnitOfWork(pool), ProcessedEvents: postgres.NewProcessedEventRepo(pool), Skus: skus}
	consumer := inboundkafka.NewProductConsumerForTopic(brokers, topic, groupFor(topic), uc, logger)
	runInBackground(t, func(ctx context.Context) { _ = consumer.Run(ctx) })
	t.Cleanup(func() { _ = consumer.Close() })

	src := "/warehouse/product-master"
	registered := event(t, "reg-1", src, inboundkafka.TypeProductRegistered, "SKU-9", map[string]any{"sku": "SKU-9", "description": "d", "version": 1})
	publish(t, brokers, topic,
		kafkago.Message{Key: []byte("x"), Value: []byte(`{"event_id":"flat","event_type":"ProductRegistered"}`)},
		kafkago.Message{Key: []byte("SKU-8"), Value: event(t, "cls-1", src, "com.warehouse.wms.product-master.product.ProductClassified", "SKU-8", map[string]any{"sku": "SKU-8"})},
		kafkago.Message{Key: []byte("bad"), Value: event(t, "reg-0", src, inboundkafka.TypeProductRegistered, "bad", map[string]any{"sku": "bad sku"})},
		kafkago.Message{Key: []byte("SKU-9"), Value: registered},
		kafkago.Message{Key: []byte("SKU-9"), Value: registered}, // redelivered id: deduped
		kafkago.Message{Key: []byte("SKU-7"), Value: event(t, "reg-2", src, inboundkafka.TypeProductRegistered, "SKU-7", map[string]any{"sku": "SKU-7", "description": "d", "version": 1})},
	)

	eventually(t, "SKU-7 and SKU-9 known", func() bool {
		a, _ := skus.Exists(context.Background(), "SKU-9")
		b, _ := skus.Exists(context.Background(), "SKU-7")
		return a && b
	})
	for _, skipped := range []string{"SKU-8", "bad sku", "bad"} {
		if ok, err := skus.Exists(context.Background(), skipped); err != nil || ok {
			t.Fatalf("%q must not be known (ok=%v err=%v)", skipped, ok, err)
		}
	}
	var claims int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM processed_events WHERE event_id = 'reg-1'`).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("claims of reg-1 = %d (%v), want 1", claims, err)
	}
}

func TestDockDoorConsumerEndToEnd(t *testing.T) {
	brokers := startKafka(t)
	pool := startPostgresPool(t)
	topic := fmt.Sprintf("warehouse.facility.events.itest-%d", time.Now().UnixNano())
	createTopic(t, brokers[0], topic)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	doors := postgres.NewDockDoors(pool)
	uow, processed := postgres.NewUnitOfWork(pool), postgres.NewProcessedEventRepo(pool)
	consumer := inboundkafka.NewDockDoorConsumerForTopic(brokers, topic, groupFor(topic),
		&usecases.ApplyLocationSlotRegistered{UoW: uow, ProcessedEvents: processed, Doors: doors},
		&usecases.ApplyLocationSlotDecommissioned{UoW: uow, ProcessedEvents: processed, Doors: doors}, logger)
	runInBackground(t, func(ctx context.Context) { _ = consumer.Run(ctx) })
	t.Cleanup(func() { _ = consumer.Close() })

	src := "/warehouse/facility-layout"
	slot := func(id, code, role, flow string) kafkago.Message {
		data := map[string]any{"eventName": "LocationSlotRegistered", "locationCode": code, "locationType": "DockDoor", "maxWeightKg": 0, "maxVolumeM3": 0}
		if role != "" {
			data["role"] = role
		}
		if flow != "" {
			data["dockFlow"] = flow
		}
		return kafkago.Message{Key: []byte(code), Value: event(t, id, src, inboundkafka.TypeLocationSlotRegistered, code, data)}
	}
	retire := func(id, code string) kafkago.Message {
		return kafkago.Message{Key: []byte(code), Value: event(t, id, src, inboundkafka.TypeLocationSlotDecommissioned, code, map[string]any{"locationCode": code})}
	}
	publish(t, brokers, topic,
		slot("s-1", "WH1-DOCK-IN-01", "Dock", "Inbound"),
		slot("s-2", "WH1-DOCK-IO-02", "Dock", "Both"),
		slot("s-3", "WH1-DOCK-OUT-03", "Dock", "Outbound"), // outbound only: ignored
		slot("s-4", "WH1-STOR-01", "", ""),                 // storage slot: ignored
		slot("s-5", "WH1-DOCK-IN-04", "Dock", "Inbound"),
		retire("s-6", "WH1-DOCK-IN-04"),
		retire("s-7", "WH1-STOR-01"), // not a door: no-op
		slot("s-8", "WH1-DOCK-IN-05", "Dock", "Inbound"),
	)

	exists := func(code string) bool {
		ok, err := doors.Exists(context.Background(), appointment.DoorCode(code))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	eventually(t, "the last message applied", func() bool { return exists("WH1-DOCK-IN-05") })
	for code, want := range map[string]bool{
		"WH1-DOCK-IN-01": true, "WH1-DOCK-IO-02": true, "WH1-DOCK-OUT-03": false, "WH1-STOR-01": false, "WH1-DOCK-IN-04": false,
	} {
		if got := exists(code); got != want {
			t.Fatalf("door %s exists = %v, want %v", code, got, want)
		}
	}
}

// groupFor derives a per-run consumer group (never an inline literal at the
// call site).
func groupFor(topic string) string { return "itest-" + topic }
