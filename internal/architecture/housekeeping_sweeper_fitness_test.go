package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sweeperObligations maps every composition root that opens a Postgres
// writer pool to the sweeper constructor it must call (gap fix D24, ADR
// 0010). The reports binary is read-only and has nothing to sweep; MCP holds
// no database.
var sweeperObligations = map[string]string{
	"cmd/api/main.go":               "postgres.NewSweeper(",
	"cmd/inbound-projector/main.go": "analyticsstore.NewSweeper(",
}

// sweeperViolation reports why src (the composition root at path) breaks
// the obligation, "" when it is met. Pure so the sensor test can feed bad
// fixtures.
func sweeperViolation(path, src, constructor string) string {
	if strings.Contains(src, constructor) {
		return ""
	}
	return path + ": opens a writer pool but never calls " + constructor +
		" — its append-only tables (idempotency_keys, outbox_events, processed_events / analytics_processed_events) grow without bound (ADR 0010)"
}

// TestCompositionRootsWireTheHousekeepingSweeper: the binaries that own a
// writer pool run the housekeeping sweeper, so no deployment grows its
// dedupe, idempotency or published-outbox tables forever.
func TestCompositionRootsWireTheHousekeepingSweeper(t *testing.T) {
	for rel, constructor := range sweeperObligations {
		src, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if v := sweeperViolation(rel, string(src), constructor); v != "" {
			t.Error(v)
		}
	}
}

// TestHousekeepingSweeperSensorFailsOnBadFixtures proves the sensor bites.
func TestHousekeepingSweeperSensorFailsOnBadFixtures(t *testing.T) {
	bad := "package main\n\nfunc run() { pool := postgres.NewPool(ctx, url); _ = pool }\n"
	if sweeperViolation("cmd/api/main.go", bad, "postgres.NewSweeper(") == "" {
		t.Fatal("sensor accepted a composition root without a sweeper")
	}
	good := bad + "\nvar _ = postgres.NewSweeper(pool)\n"
	if v := sweeperViolation("cmd/api/main.go", good, "postgres.NewSweeper("); v != "" {
		t.Fatalf("sensor rejected a wired sweeper: %s", v)
	}
}
