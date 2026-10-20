package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig_Defaults(t *testing.T) {
	c, err := loadConfig(env(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "k1:9092, k2:9092,,"}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		analyticsURL: "postgres://u@h/db", brokers: []string{"k1:9092", "k2:9092"},
		group: "inbound-receiving-analytics", adminAddr: ":8091", logLevel: "info",
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestLoadConfig_EnvironmentOverridesEverything(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "kafka:9092",
		"ANALYTICS_CONSUMER_GROUP": "g-from-env", "ADMIN_ADDR": ":9999", "LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.group != "g-from-env" || c.adminAddr != ":9999" || c.logLevel != "debug" || !reflect.DeepEqual(c.brokers, []string{"kafka:9092"}) {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadConfig_RequiresTheAnalyticalDatabaseAndKafka(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{"KAFKA_BROKERS": "k:9092"})); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("err = %v", err)
	}
	for _, brokers := range []string{"", " , "} {
		if _, err := loadConfig(env(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": brokers})); !errors.Is(err, errMissingBrokers) {
			t.Fatalf("brokers %q: err = %v", brokers, err)
		}
	}
}

func TestRun_FailsFastWithoutTheAnalyticalDatabase(t *testing.T) {
	t.Setenv("ANALYTICS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", "k:9092")
	if err := run(); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("run = %v", err)
	}
}

func adminGet(mux http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminMux_HealthzIsLivenessAndReadyzFlipsOnShutdown(t *testing.T) {
	up := func(context.Context) error { return nil }
	gate := inboundhttp.NewReadiness(time.Nanosecond, time.Second, inboundhttp.Dependency{Name: "analytics-postgres", Check: up})
	mux := newAdminMux(gate)
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/readyz"); rec.Code != 200 || rec.Body.String() != `{"status":"ready"}` {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}
	gate.SetNotReady()
	if rec := adminGet(mux, "/readyz"); rec.Code != 503 || rec.Body.String() != `{"status":"not_ready"}` {
		t.Fatalf("readyz during shutdown = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz must stay up during shutdown, got %d", rec.Code)
	}
}

// /readyz is real: a dead Kafka broker or a dead analytical database is NOT
// ready, and the body names the failing dependency; /healthz is unaffected.
func TestAdminMux_ReadyzReflectsTheDependencies(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }
	dead := []string{"127.0.0.1:1"}

	mux := newAdminMux(newReadiness(up, dead))
	rec := adminGet(mux, "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"failing":["kafka"]`) {
		t.Fatalf("readyz with Kafka down = %d %s, want 503 naming kafka", rec.Code, rec.Body)
	}
	rec = adminGet(newAdminMux(newReadiness(down, dead)), "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "analytics-postgres") || !strings.Contains(rec.Body.String(), "kafka") {
		t.Fatalf("readyz with everything down = %d %s", rec.Code, rec.Body)
	}
	if rec := adminGet(mux, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz = %d, liveness must not depend on dependencies", rec.Code)
	}
}

func TestNewLogger_LevelMapping(t *testing.T) {
	for level, enabledDebug := range map[string]bool{"debug": true, "info": false, "": false, "nonsense": false} {
		if got := newLogger(level).Enabled(context.Background(), -4); got != enabledDebug {
			t.Errorf("level %q: debug enabled = %v, want %v", level, got, enabledDebug)
		}
	}
	if newLogger("warn").Enabled(context.Background(), 0) || !newLogger("warning").Enabled(context.Background(), 4) || newLogger("error").Enabled(context.Background(), 4) {
		t.Error("warn/error level mapping is wrong")
	}
}
