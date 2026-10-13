package main

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestAnalyticsSweeperStopsPromptly: with HOUSEKEEPING_INTERVAL=0 the
// analytics sweeper never touches the (nil) pool and the stop function
// returns at once (gap fix D24).
func TestAnalyticsSweeperStopsPromptly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(k string) string {
		if k == "HOUSEKEEPING_INTERVAL" {
			return "0"
		}
		return ""
	}
	stop := startAnalyticsSweeper(nil, env, logger)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping a disabled analytics sweeper blocked")
	}
}
