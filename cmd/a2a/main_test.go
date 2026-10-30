package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An unreachable database is a boot error after the bounded retries, never a
// silent fallback.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// There is no in-memory runtime: an empty DATABASE_URL is a boot error from
// both loadBootConfig (before anything opens) and buildDeps.
func TestNoDatabaseURLIsABootError(t *testing.T) {
	if _, err := loadBootConfig(func(string) string { return "" }); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("loadBootConfig(empty) = %v, want errMissingDatabaseURL", err)
	}
	if _, _, err := buildDeps(context.Background(), quietLogger(), "", ""); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("buildDeps(\"\") = %v, want errMissingDatabaseURL", err)
	}
}

// depsOver wires exactly the three read use cases the skills need.
func TestDepsOver_WiresTheReadUseCasesOnly(t *testing.T) {
	deps := depsOver(memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo())
	if deps.GetAsn == nil || deps.ListReceipts == nil || deps.ListAppointments == nil {
		t.Fatalf("deps = %+v, want all three read use cases", deps)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("IR_A2A_TEST_SET", "v")
	t.Setenv("IR_A2A_TEST_EMPTY", "")
	if got := getenv("IR_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("IR_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
