package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaultsToPermissiveWithNoConsumers(t *testing.T) {
	cfg, err := loadConfig(envOf(nil))
	if err != nil || cfg.productMode != usecases.ModePermissive || cfg.doorMode != usecases.ModePermissive || cfg.consumersWant {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestLoadConfigKafkaModes(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{
		"PRODUCT_MODE": "kafka", "PRODUCT_CONSUMER_GROUP": "g1", "DOCK_DOOR_MODE": "kafka", "DOCK_DOOR_CONSUMER_GROUP": "g2",
		"KAFKA_BROKERS": "a:9092,b:9092",
	}))
	if err != nil || cfg.productMode != usecases.ModeKafka || cfg.doorMode != usecases.ModeKafka || !cfg.consumersWant ||
		cfg.productGroup != "g1" || cfg.doorGroup != "g2" || len(cfg.kafkaBrokers) != 2 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestLoadConfigRefusesBadCombinations(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown product mode":       {"PRODUCT_MODE": "strict"},
		"unknown door mode":          {"DOCK_DOOR_MODE": "strict"},
		"product kafka no group":     {"PRODUCT_MODE": "kafka", "KAFKA_BROKERS": "a:9092"},
		"door kafka no group":        {"DOCK_DOOR_MODE": "kafka", "KAFKA_BROKERS": "a:9092"},
		"kafka mode with no brokers": {"PRODUCT_MODE": "kafka", "PRODUCT_CONSUMER_GROUP": "g"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(envOf(env)); err == nil {
				t.Fatal("want a boot error")
			}
		})
	}
}

func TestParsePublisherMode(t *testing.T) {
	for raw, want := range map[string]string{"": "log", "log": "log", "kafka": "kafka"} {
		got, err := parsePublisherMode(raw)
		if err != nil || got != want {
			t.Errorf("parsePublisherMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := parsePublisherMode("rabbit"); err == nil {
		t.Error("an unknown EVENT_PUBLISHER must be a config error")
	}
}

func TestParseRelayInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"": time.Second, "250ms": 250 * time.Millisecond, "3s": 3 * time.Second, "2": 2 * time.Second,
		"0.5": 500 * time.Millisecond, "abc": time.Second, "-1s": time.Second, "0": time.Second,
	}
	for raw, want := range cases {
		if got := parseRelayInterval(raw, quietLogger()); got != want {
			t.Errorf("parseRelayInterval(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	cases := map[string]time.Duration{"": DefaultShutdownDrainDelay, "0": 0, "2s": 2 * time.Second, "x": DefaultShutdownDrainDelay, "-1s": DefaultShutdownDrainDelay}
	for raw, want := range cases {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(quietLogger()); got != want {
			t.Errorf("shutdownDrainDelay(%q) = %v, want %v", raw, got, want)
		}
	}
}

type countingStore struct{ drains chan struct{} }

func (s countingStore) Drain(ctx context.Context, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	select {
	case s.drains <- struct{}{}:
	default:
	}
	return 0, ctx.Err()
}

var _ outboxrelay.Store = countingStore{}

func TestStartOutboxRelayDefaultsToTheLogSinkAndStops(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	store := countingStore{drains: make(chan struct{}, 1)}
	w, stop, err := startOutboxRelay(store, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay: %v", err)
	}
	select {
	case <-store.drains:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never drained")
	}
	stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not stop")
	}
}

// A broker that is down at boot must not crash or block startup.
func TestStartOutboxRelayKafkaModeDoesNotDialAtBoot(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "kafka")
	t.Setenv("KAFKA_BROKERS", "127.0.0.1:1")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	start := time.Now()
	w, stop, err := startOutboxRelay(countingStore{drains: make(chan struct{}, 1)}, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay with an unreachable broker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("startup took %v; it must not wait on Kafka", elapsed)
	}
	stop()
	await(quietLogger(), w)
}

func TestStartOutboxRelayConfigErrors(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "kafka")
	t.Setenv("KAFKA_BROKERS", "")
	if _, _, err := startOutboxRelay(countingStore{}, quietLogger()); err == nil {
		t.Fatal("EVENT_PUBLISHER=kafka needs KAFKA_BROKERS")
	}
	t.Setenv("EVENT_PUBLISHER", "nope")
	if _, _, err := startOutboxRelay(countingStore{}, quietLogger()); err == nil {
		t.Fatal("want an error for an unknown EVENT_PUBLISHER")
	}
}

func TestStartConsumersPermissiveStartsNothing(t *testing.T) {
	ad, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if got := startConsumers(ad, config{productMode: usecases.ModePermissive, doorMode: usecases.ModePermissive}, quietLogger()); len(got) != 0 {
		t.Fatalf("workers = %d, want none", len(got))
	}
}

func TestStartConsumersKafkaStartsBothWithoutDialingAndStops(t *testing.T) {
	ad, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	cfg := config{
		productMode: usecases.ModeKafka, productGroup: "product-group", doorMode: usecases.ModeKafka, doorGroup: "door-group",
		kafkaBrokers: []string{"127.0.0.1:1"}, consumersWant: true,
	}
	start := time.Now()
	workers := startConsumers(ad, cfg, quietLogger())
	if len(workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(workers))
	}
	if time.Since(start) > time.Second {
		t.Fatal("starting the consumers must not dial Kafka")
	}
	for _, w := range workers {
		w.stop()
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not stop", w.name)
		}
	}
}

func memoryAdapters(t *testing.T) adapters {
	t.Helper()
	ad, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if err != nil {
		t.Fatalf("buildAdapters: %v", err)
	}
	t.Cleanup(closeFn)
	return ad
}

func TestBuildAdaptersInMemoryWiresEveryPort(t *testing.T) {
	ad := memoryAdapters(t)
	ports := []any{ad.asns, ad.appointments, ad.receipts, ad.outbox, ad.outboxStore, ad.processed, ad.skus, ad.doors, ad.idempotency, ad.uow}
	for i, p := range ports {
		if p == nil {
			t.Fatalf("port #%d not wired: %+v", i, ad)
		}
	}
}

func TestBuildServerWiresEveryUseCase(t *testing.T) {
	s := buildServer(memoryAdapters(t), config{productMode: usecases.ModePermissive, doorMode: usecases.ModePermissive}, &inboundhttp.Readiness{}, nil)
	useCases := []any{
		s.RegisterAsn, s.CancelAsn, s.GetAsn, s.ListAsns, s.BookAppointment, s.CheckInAppointment, s.CancelAppointment,
		s.GetAppointment, s.ListAppointments, s.OpenReceipt, s.ReceiveLine, s.CloseReceipt, s.GetReceipt, s.ListReceipts,
		s.ListDocks, s.Idempotency,
	}
	for i, uc := range useCases {
		if uc == nil {
			t.Fatalf("use case #%d not wired", i)
		}
	}
}

func TestCompositionRootServesTheAPI(t *testing.T) {
	s := buildServer(memoryAdapters(t), config{productMode: usecases.ModePermissive, doorMode: usecases.ModePermissive}, &inboundhttp.Readiness{}, nil)
	srv := httptest.NewServer(inboundhttp.NewRouter(s))
	defer srv.Close()
	body := `{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":3}]}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/asns", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "boot-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /asns through the composition root = %d, want 201", resp.StatusCode)
	}
}

func TestMigrationsDatabaseURL(t *testing.T) {
	const pooled, direct = "postgres://u@pgbouncer:6432/db", "postgres://u@postgres:5432/db"
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	if got := migrationsDatabaseURL(pooled); got != pooled {
		t.Errorf("unset: got %q", got)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", direct)
	if got := migrationsDatabaseURL(pooled); got != direct {
		t.Errorf("set: got %q", got)
	}
}

func TestNewLoggerAndGetenv(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "nonsense"} {
		if newLogger(lvl) == nil {
			t.Fatalf("newLogger(%q) = nil", lvl)
		}
	}
	t.Setenv("IR_TEST_KEY", "")
	if getenv("IR_TEST_KEY", "fallback") != "fallback" {
		t.Fatal("getenv fallback")
	}
	t.Setenv("IR_TEST_KEY", "v")
	if getenv("IR_TEST_KEY", "fallback") != "v" {
		t.Fatal("getenv value")
	}
}
