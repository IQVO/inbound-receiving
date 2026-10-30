package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMigrationsDatabaseURLFallback proves the fallback wiring run() applies
// before calling buildDeps: getenv("MIGRATIONS_DATABASE_URL", databaseURL) must
// return MIGRATIONS_DATABASE_URL's own value when it is set, and databaseURL
// itself (DATABASE_URL) when it is unset. Mirrors cmd/mcp's behaviour (a DIRECT
// Postgres DSN for the session-scoped migration advisory lock where
// DATABASE_URL points at PgBouncer).
func TestMigrationsDatabaseURLFallback(t *testing.T) {
	const databaseURL = "postgres://u:p@pgbouncer.example:6432/inbound_receiving?sslmode=disable"

	t.Run("falls back to DATABASE_URL when MIGRATIONS_DATABASE_URL is unset", func(t *testing.T) {
		t.Setenv("MIGRATIONS_DATABASE_URL", "")
		os.Unsetenv("MIGRATIONS_DATABASE_URL")

		if got := getenv("MIGRATIONS_DATABASE_URL", databaseURL); got != databaseURL {
			t.Fatalf("getenv fallback = %q, want the DATABASE_URL value %q", got, databaseURL)
		}
	})

	t.Run("uses MIGRATIONS_DATABASE_URL when set, not DATABASE_URL", func(t *testing.T) {
		const direct = "postgres://u:p@postgres-postgresql.example:5432/inbound_receiving?sslmode=disable"
		t.Setenv("MIGRATIONS_DATABASE_URL", direct)

		if got := getenv("MIGRATIONS_DATABASE_URL", databaseURL); got != direct {
			t.Fatalf("getenv = %q, want the direct MIGRATIONS_DATABASE_URL value %q (must NOT silently keep using DATABASE_URL/PgBouncer)", got, direct)
		}
	})
}

// TestBuildDeps_UsesMigrationsDatabaseURLNotDatabaseURLForMigrations proves
// buildDeps itself, not just the env read above, threads migrationsDatabaseURL
// into the migration step. A schemeless migrations URL is rejected by the
// migrator at once with a parse error, and bootretry logs the cause of the
// first failed attempt before it backs off; had buildDeps run migrations
// against the unreachable databaseURL instead, that logged cause would be a
// dial failure naming 127.0.0.1:1. (The returned error cannot tell them apart:
// a context that ends the retries wraps only ctx.Err().)
func TestBuildDeps_UsesMigrationsDatabaseURLNotDatabaseURLForMigrations(t *testing.T) {
	const (
		bogusMigrationsURL = "not-a-valid-connection-string"
		unreachableAppURL  = "postgres://u:p@127.0.0.1:1/inbound_receiving?sslmode=disable&connect_timeout=1"
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, _, err := buildDeps(ctx, logger, unreachableAppURL, bogusMigrationsURL); err == nil {
		t.Fatal("a malformed MIGRATIONS_DATABASE_URL must fail boot")
	}
	got := logs.String()
	if !strings.Contains(got, "parse scheme") {
		t.Fatalf("logged cause = %q, want the migrator's parse error for MIGRATIONS_DATABASE_URL", got)
	}
	if strings.Contains(got, "127.0.0.1:1") {
		t.Fatalf("logged cause = %q: migrations ran against DATABASE_URL instead of MIGRATIONS_DATABASE_URL", got)
	}
}
