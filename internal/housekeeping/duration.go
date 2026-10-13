// Package housekeeping reads the housekeeping sweeper knobs (gap fix D24,
// ADR 0010) shared by cmd/api and cmd/inbound-projector.
package housekeeping

import (
	"log/slog"
	"strconv"
	"time"
)

// Environment variable names of the sweeper knobs (the fleet convention).
const (
	EnvInterval                 = "HOUSEKEEPING_INTERVAL"
	EnvIdempotencyKeyTTL        = "IDEMPOTENCY_KEY_TTL"
	EnvOutboxRetention          = "OUTBOX_RETENTION"
	EnvProcessedEventsRetention = "PROCESSED_EVENTS_RETENTION"
)

// Duration reads key via env as a Go duration ("720h") or plain seconds
// ("90"). Unset returns fallback; an explicit zero is preserved because it
// disables that part of the sweep; a negative or malformed value logs a
// WARN and returns fallback, so a typo never silently disables the sweep.
func Duration(env func(string) string, key string, fallback time.Duration, logger *slog.Logger) time.Duration {
	raw := env(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d < 0 {
		logger.Warn("invalid housekeeping duration; using the default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
