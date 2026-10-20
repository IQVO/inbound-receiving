package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestNewSweeperReadsTheHousekeepingEnv: HOUSEKEEPING_INTERVAL reaches the
// sweeper (0 disables it, so Run returns at once without touching the nil
// pool), and startSweeper is a no-op when no sweeper was built (gap fix D24).
func TestNewSweeperReadsTheHousekeepingEnv(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(k string) string {
		if k == "HOUSEKEEPING_INTERVAL" {
			return "0"
		}
		return ""
	}
	done := make(chan error, 1)
	go func() { done <- newSweeper(nil, env, logger).Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disabled sweeper Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HOUSEKEEPING_INTERVAL=0 did not disable the sweeper")
	}
	if w := startSweeper(nil, logger); w != nil {
		t.Fatal("startSweeper(nil) must not start a worker")
	}
}
