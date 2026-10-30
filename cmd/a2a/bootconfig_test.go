package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

const testAgentURL = "http://inbound-receiving-a2a.warehouse-systems.svc.cluster.local:8093/"

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func completeEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":  "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		"A2A_AGENT_URL": testAgentURL,
	}
}

func TestLoadBootConfigAcceptsACompleteEnvironment(t *testing.T) {
	cfg, err := loadBootConfig(envOf(completeEnv()))
	if err != nil || cfg.databaseURL == "" || cfg.agentURL != testAgentURL {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

// Each missing or rejected value is a boot error that names its variable.
func TestLoadBootConfigRefusesEachMissingOrRejectedValue(t *testing.T) {
	cases := []struct {
		name, variable, value, want string
	}{
		{"database unset", "DATABASE_URL", "", "DATABASE_URL is required: this context runs on real Postgres"},
		{"agent url unset", "A2A_AGENT_URL", "", "A2A_AGENT_URL is required: the Agent Card must advertise"},
		{"agent url blank", "A2A_AGENT_URL", "   ", "A2A_AGENT_URL is required"},
		{"agent url relative", "A2A_AGENT_URL", "/a2a", `A2A_AGENT_URL="/a2a" is not an absolute http(s) URL`},
		{"agent url schemeless", "A2A_AGENT_URL", "inbound-receiving-a2a:8093", "is not an absolute http(s) URL"},
		{"agent url wrong scheme", "A2A_AGENT_URL", "ftp://host/", "is not an absolute http(s) URL"},
		{"agent url without host", "A2A_AGENT_URL", "http://", "is not an absolute http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			env[tc.variable] = tc.value
			_, err := loadBootConfig(envOf(env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s=%q: %v, want a boot error containing %q", tc.variable, tc.value, err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.variable) {
				t.Fatalf("the error does not name %s: %v", tc.variable, err)
			}
		})
	}
}

// Every problem is reported at once, so an operator fixes the environment in
// one pass.
func TestLoadBootConfigReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadBootConfig(envOf(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") || !strings.Contains(err.Error(), "A2A_AGENT_URL is required") {
		t.Fatalf("err = %v, want both variables named", err)
	}
}

// The binary reads no Kafka and no dock-door setting: neither is validated
// (nor required) here, unlike cmd/api.
func TestLoadBootConfigNeedsNoKafkaOrConsumerSettings(t *testing.T) {
	env := completeEnv()
	env["DOCK_DOOR_MODE"] = "permissive"
	if _, err := loadBootConfig(envOf(env)); err != nil {
		t.Fatalf("the A2A agent has no dock-door mode: %v", err)
	}
}

// run fails fast on a bad configuration: before telemetry, any connection and
// before the listener binds.
func TestRunFailsFastBeforeBindingTheListener(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("A2A_AGENT_URL", "not-a-url")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	t.Setenv("A2A_ADDR", addr)

	start := time.Now()
	if err := run(); err == nil || !strings.Contains(err.Error(), "A2A_AGENT_URL") {
		t.Fatalf("run = %v, want a boot error naming A2A_AGENT_URL", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a configuration error must not wait on any dependency")
	}
	if c, derr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(t.Context(), "tcp", addr); derr == nil {
		_ = c.Close()
		t.Fatal("the listener was bound although the configuration is invalid")
	}
}

// There is no in-memory runtime: run() without DATABASE_URL is a boot error
// naming it.
func TestRunRefusesToBootWithoutDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("A2A_AGENT_URL", testAgentURL)
	if err := run(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run() without DATABASE_URL = %v, want a boot error naming DATABASE_URL", err)
	}
}
