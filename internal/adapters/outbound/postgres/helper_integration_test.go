//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
)

// One Postgres container serves the whole package (booting one per test
// would dominate the run); every test gets its OWN database inside it, so
// tests may assert on whole-table contents.
var (
	pgOnce      sync.Once
	pgContainer testcontainers.Container
	pgAdminURL  string
	pgStartErr  error
	dbCounter   atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgContainer != nil {
		if err := testcontainers.TerminateContainer(pgContainer); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}
	os.Exit(code)
}

func startContainer() {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("inbound_test"),
		tcpostgres.WithPassword("inbound_test"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		pgStartErr = err
		return
	}
	pgContainer = container
	pgAdminURL, pgStartErr = container.ConnectionString(ctx, "sslmode=disable")
}

// databaseURL returns the admin URL with its database name replaced.
func databaseURL(t *testing.T, name string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(pgAdminURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
}

// startPostgresPool boots a real Postgres via testcontainers (never a
// skip-gated external database), creates a fresh database, applies the
// embedded migrations (twice: boot runs them on every start) and returns an
// open pool.
func startPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pgOnce.Do(startContainer)
	if pgStartErr != nil {
		t.Fatalf("start postgres container: %v", pgStartErr)
	}
	name := fmt.Sprintf("itest_%d", dbCounter.Add(1))
	admin, err := pgx.Connect(ctx, pgAdminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	url := databaseURL(t, name)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
