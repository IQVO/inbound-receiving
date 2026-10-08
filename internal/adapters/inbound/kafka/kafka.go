// Package kafka holds this service's INBOUND Kafka consumers (ADR 0003): the
// product consumer (product-master's ProductRegistered into the known_skus
// local copy) and the dock-door consumer (facility-layout's
// LocationSlotRegistered/LocationSlotDecommissioned into dock_doors). Each
// decodes CloudEvents 1.0 only via internal/adapters/kafka/cloudevents and
// dispatches on the FULL `type` string.
//
// Delivery guarantee: AT-LEAST-ONCE with an ATOMIC effect. Each message is
// handled inside ONE unit of work (processed-event claim + effect commit or
// roll back together, see the consumer use cases) and its
// offset is committed only AFTER handling returned nil (FetchMessage +
// CommitMessages). Handling returns a non-nil error ONLY for transient
// failures; the run loop then retries the SAME message with capped
// exponential backoff and never commits past it. Deterministic problems (not
// a CloudEvent, unknown type, malformed payload, invalid classification) are
// logged at WARN and committed past.
package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// Reader is the subset of *kafkago.Reader a consumer needs, so unit tests
// never need a broker. FetchMessage/CommitMessages (not ReadMessage, which
// auto-commits before the message is handled).
type Reader interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// readerConfig builds the reader configuration. GroupID always comes from
// the caller (an env var at the composition root, never a literal here).
// CommitInterval stays UNSET so CommitMessages commits synchronously, after
// the message was handled.
func readerConfig(brokers []string, topic, groupID string) kafkago.ReaderConfig {
	return kafkago.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	}
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// RetryPolicy is the capped exponential backoff applied while the SAME
// message keeps failing with a transient error. Zero fields fall back to
// DefaultRetryInitial / DefaultRetryMax.
type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

// Default backoff: 200ms, 400ms, 800ms ... capped at 5s.
const (
	DefaultRetryInitial = 200 * time.Millisecond
	DefaultRetryMax     = 5 * time.Second
)

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.Initial <= 0 {
		p.Initial = DefaultRetryInitial
	}
	if p.Max <= 0 {
		p.Max = DefaultRetryMax
	}
	return p
}

// next doubles d, capped at p.Max.
func (p RetryPolicy) next(d time.Duration) time.Duration {
	d *= 2
	if d > p.Max {
		return p.Max
	}
	return d
}

// sleepFunc waits d or until ctx is done (returning ctx.Err()); a field so
// tests can record the backoff without sleeping.
type sleepFunc func(ctx context.Context, d time.Duration) error

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// consumeLoop is the at-least-once run loop.
type consumeLoop struct {
	reader Reader
	handle func(ctx context.Context, msg kafkago.Message) error
	logger *slog.Logger
	name   string
	retry  RetryPolicy
	sleep  sleepFunc
}

// run fetches one message at a time and does not fetch the next until the
// current one was handled AND its offset committed. It returns only when ctx
// is cancelled or FetchMessage itself fails.
func (l *consumeLoop) run(ctx context.Context) error {
	policy := l.retry.withDefaults()
	sleep := l.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for {
		msg, err := l.reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		if err := l.retryUntilOK(ctx, policy, sleep, "handling", msg, func() error { return l.handle(ctx, msg) }); err != nil {
			return err
		}
		// A failed commit is retried too: the work is already durable, and a
		// redelivery after a crash is skipped by the processed-event claim.
		if err := l.retryUntilOK(ctx, policy, sleep, "offset commit", msg, func() error {
			return l.reader.CommitMessages(ctx, msg)
		}); err != nil {
			return err
		}
	}
}

// retryUntilOK runs op until it returns nil, sleeping with capped
// exponential backoff between attempts, and gives up only when ctx is
// cancelled. It never skips: a message that keeps failing transiently blocks
// its partition (logged at ERROR every attempt) rather than being dropped.
func (l *consumeLoop) retryUntilOK(ctx context.Context, p RetryPolicy, sleep sleepFunc, what string, msg kafkago.Message, op func() error) error {
	delay := p.Initial
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.logger.ErrorContext(ctx, l.name+" "+what+" failed; retrying the same message",
			"error", err, "partition", msg.Partition, "offset", msg.Offset, "attempt", attempt, "retry_in", delay)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = p.next(delay)
	}
}
