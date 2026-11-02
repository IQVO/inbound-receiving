package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// config is the validated, env-derived part of the wiring.
type config struct {
	productGroup   string
	doorGroup      string
	packagingGroup string
	kafkaBrokers   []string
}

// Boot errors for the variables the context cannot run without. Each message
// names the variable (ADR 0011).
var (
	errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	errMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// loadConfig validates the environment before anything is opened or bound:
// DATABASE_URL, KAFKA_BROKERS and both consumer groups are mandatory, the
// consumer modes may only be `kafka` (the default), and EVENT_PUBLISHER may
// only be `kafka`. Every error names the variable (ADR 0011).
func loadConfig(env func(string) string) (config, error) {
	var cfg config
	if env("DATABASE_URL") == "" {
		return config{}, errMissingDatabaseURL
	}
	cfg.kafkaBrokers = splitBrokers(env("KAFKA_BROKERS"))
	if len(cfg.kafkaBrokers) == 0 {
		return config{}, errMissingBrokers
	}
	if err := parseConsumerMode("PRODUCT_MODE", env("PRODUCT_MODE")); err != nil {
		return config{}, err
	}
	if err := parseConsumerMode("DOCK_DOOR_MODE", env("DOCK_DOOR_MODE")); err != nil {
		return config{}, err
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return config{}, err
	}
	if cfg.productGroup = env("PRODUCT_CONSUMER_GROUP"); cfg.productGroup == "" {
		return config{}, errors.New("PRODUCT_CONSUMER_GROUP is required: the product consumer always runs on real Kafka")
	}
	if cfg.doorGroup = env("DOCK_DOOR_CONSUMER_GROUP"); cfg.doorGroup == "" {
		return config{}, errors.New("DOCK_DOOR_CONSUMER_GROUP is required: the dock-door consumer always runs on real Kafka")
	}
	if cfg.packagingGroup = env("PACKAGING_CONSUMER_GROUP"); cfg.packagingGroup == "" {
		return config{}, errors.New("PACKAGING_CONSUMER_GROUP is required: the packaging consumer (PackagingView) always runs on real Kafka")
	}
	return cfg, nil
}

// splitBrokers splits KAFKA_BROKERS on commas, trimming blanks.
func splitBrokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// parseConsumerMode accepts the only runtime mode of a local-copy consumer:
// "" (the default) or "kafka". The names stay (docs and infra set them) but
// `permissive` and `off` are refused: the consumers always run.
func parseConsumerMode(name, raw string) error {
	switch raw {
	case "", string(usecases.ModeKafka):
		return nil
	case string(usecases.ModePermissive), "off":
		return fmt.Errorf("%s=%s is not supported: this context runs on real Kafka", name, raw)
	default:
		return fmt.Errorf("%s: unknown mode %q (want kafka)", name, raw)
	}
}

// parsePublisherMode validates EVENT_PUBLISHER: unset or "kafka". The log
// sink is gone (ADR 0011); the variable stays because the chart and
// warehouse-infra set it.
func parsePublisherMode(raw string) error {
	switch raw {
	case "", "kafka":
		return nil
	case "log":
		return errors.New("EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka")
	default:
		return fmt.Errorf("EVENT_PUBLISHER: unknown value %q (want kafka)", raw)
	}
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// Env names of the CartonOverLimit limits (contracts R9, ADR 0014).
const (
	envCartonMaxGrossWeightG  = "CARTON_MAX_GROSS_WEIGHT_G"
	envCartonMaxLongestSideMm = "CARTON_MAX_LONGEST_SIDE_MM"
)

// cartonLimits reads the two carton handling limits. Unset or 0 = unchecked;
// a negative or unparsable value logs a warning and is unchecked, so a typo
// never rejects or flags goods by accident.
func cartonLimits(env func(string) string, logger *slog.Logger) receipt.CartonLimits {
	return receipt.CartonLimits{
		MaxGrossWeightG:  limitFromEnv(env, envCartonMaxGrossWeightG, logger),
		MaxLongestSideMm: limitFromEnv(env, envCartonMaxLongestSideMm, logger),
	}
}

func limitFromEnv(env func(string) string, name string, logger *slog.Logger) int64 {
	raw := env(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		logger.Warn("invalid "+name+"; the limit is unchecked", "value", raw)
		return 0
	}
	return n
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS (gap fix D12): how many times
// an outbox row may fail before the relay excludes it from claims. Unset =
// the fleet default (20); 0 = unlimited; negative or unparsable values log
// a warning and use the default, so a typo never silently removes the cap.
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
