package main

import (
	"context"
	"errors"
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
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// validEnv is the smallest environment the binary boots with; tests delete or
// overwrite single keys of it.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u@db:5432/x", "KAFKA_BROKERS": "a:9092,b:9092",
		"PRODUCT_CONSUMER_GROUP": "g1", "DOCK_DOOR_CONSUMER_GROUP": "g2",
	}
}

func withEnv(overrides map[string]string) map[string]string {
	env := validEnv()
	for k, v := range overrides {
		if v == "-" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return env
}

func TestLoadConfigAcceptsTheMinimalRealEnvironment(t *testing.T) {
	cfg, err := loadConfig(envOf(validEnv()))
	if err != nil || cfg.productGroup != "g1" || cfg.doorGroup != "g2" || len(cfg.kafkaBrokers) != 2 || cfg.kafkaBrokers[1] != "b:9092" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	// The explicit values the chart renders are accepted too.
	env := withEnv(map[string]string{"PRODUCT_MODE": "kafka", "DOCK_DOOR_MODE": "kafka", "EVENT_PUBLISHER": "kafka"})
	if _, err := loadConfig(envOf(env)); err != nil {
		t.Fatalf("explicit kafka modes: %v", err)
	}
}

// Every refused environment is a boot error whose message names the variable
// (ADR 0011), raised by loadConfig before anything is opened or bound.
func TestLoadConfigRefusesWithAnErrorNamingTheVariable(t *testing.T) {
	cases := []struct {
		name     string
		override map[string]string
		wantVar  string
	}{
		{"no DATABASE_URL", map[string]string{"DATABASE_URL": "-"}, "DATABASE_URL"},
		{"no KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": "-"}, "KAFKA_BROKERS"},
		{"blank KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": " , "}, "KAFKA_BROKERS"},
		{"no PRODUCT_CONSUMER_GROUP", map[string]string{"PRODUCT_CONSUMER_GROUP": "-"}, "PRODUCT_CONSUMER_GROUP"},
		{"no DOCK_DOOR_CONSUMER_GROUP", map[string]string{"DOCK_DOOR_CONSUMER_GROUP": "-"}, "DOCK_DOOR_CONSUMER_GROUP"},
		{"PRODUCT_MODE=permissive", map[string]string{"PRODUCT_MODE": "permissive"}, "PRODUCT_MODE=permissive is not supported"},
		{"PRODUCT_MODE=off", map[string]string{"PRODUCT_MODE": "off"}, "PRODUCT_MODE=off is not supported"},
		{"DOCK_DOOR_MODE=permissive", map[string]string{"DOCK_DOOR_MODE": "permissive"}, "DOCK_DOOR_MODE=permissive is not supported"},
		{"DOCK_DOOR_MODE=off", map[string]string{"DOCK_DOOR_MODE": "off"}, "DOCK_DOOR_MODE=off is not supported"},
		{"unknown PRODUCT_MODE", map[string]string{"PRODUCT_MODE": "strict"}, "PRODUCT_MODE"},
		{"unknown DOCK_DOOR_MODE", map[string]string{"DOCK_DOOR_MODE": "strict"}, "DOCK_DOOR_MODE"},
		{"EVENT_PUBLISHER=log", map[string]string{"EVENT_PUBLISHER": "log"}, "EVENT_PUBLISHER=log is not supported"},
		{"unknown EVENT_PUBLISHER", map[string]string{"EVENT_PUBLISHER": "rabbit"}, "EVENT_PUBLISHER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(envOf(withEnv(tc.override)))
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want a boot error containing %q", err, tc.wantVar)
			}
		})
	}
}

func TestLoadConfigPermissiveMessageNamesRealKafka(t *testing.T) {
	_, err := loadConfig(envOf(withEnv(map[string]string{"PRODUCT_MODE": "permissive"})))
	if err == nil || err.Error() != "PRODUCT_MODE=permissive is not supported: this context runs on real Kafka" {
		t.Fatalf("err = %v", err)
	}
}

// A completely empty environment fails on the first mandatory variable, and
// buildAdapters refuses an empty DATABASE_URL by itself (no in-memory branch).
func TestNoDatabaseURLIsABootErrorEverywhere(t *testing.T) {
	if _, err := loadConfig(envOf(nil)); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("loadConfig(empty) = %v, want errMissingDatabaseURL", err)
	}
	_, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("buildAdapters(\"\") = %v, want errMissingDatabaseURL", err)
	}
	closeFn()
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

func (s countingStore) Drain(ctx context.Context, _, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	select {
	case s.drains <- struct{}{}:
	default:
	}
	return 0, ctx.Err()
}

var _ outboxrelay.Store = countingStore{}

// The relay is always on and always publishes to Kafka: with a down broker it
// starts without dialing, drains the store, and stops.
func TestStartOutboxRelayStartsOverKafkaWithoutDialingAtBoot(t *testing.T) {
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	store := countingStore{drains: make(chan struct{}, 1)}
	start := time.Now()
	w, stop, err := startOutboxRelay(store, []string{"127.0.0.1:1"}, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay with an unreachable broker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("startup took %v; it must not wait on Kafka", elapsed)
	}
	select {
	case <-store.drains:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never drained")
	}
	stop()
	await(quietLogger(), w)
	select {
	case <-w.done:
	default:
		t.Fatal("the relay did not stop")
	}
}

func TestStartOutboxRelayWithoutBrokersIsABootError(t *testing.T) {
	if _, _, err := startOutboxRelay(countingStore{}, nil, quietLogger()); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("err = %v, want errMissingBrokers", err)
	}
}

func TestStartConsumersAlwaysStartsBothWithoutDialingAndStops(t *testing.T) {
	cfg := config{productGroup: "product-group", doorGroup: "door-group", kafkaBrokers: []string{"127.0.0.1:1"}}
	start := time.Now()
	workers := startConsumers(testDoubleAdapters(t), cfg, quietLogger())
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

func TestBuildServerWiresEveryUseCase(t *testing.T) {
	s := buildServer(testDoubleAdapters(t), &inboundhttp.Readiness{}, nil, nil)
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

// The composition root enforces the SKU local copy: the runtime is `kafka`
// mode only, so an ASN line for a SKU nobody registered is a 422, and a
// registered one is a 201.
func TestCompositionRootEnforcesTheKnownSkuCopy(t *testing.T) {
	ad := testDoubleAdapters(t)
	s := buildServer(ad, &inboundhttp.Readiness{}, nil, nil)
	srv := httptest.NewServer(inboundhttp.NewRouter(s))
	defer srv.Close()
	body := `{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":3}]}`
	post := func(key string) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/asns", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("boot-1"); got != http.StatusUnprocessableEntity {
		t.Fatalf("POST /asns with an unknown SKU = %d, want 422", got)
	}
	if err := ad.skus.Upsert(context.Background(), "SKU-1"); err != nil {
		t.Fatal(err)
	}
	if got := post("boot-2"); got != http.StatusCreated {
		t.Fatalf("POST /asns with a registered SKU = %d, want 201", got)
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
