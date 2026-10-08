package kafka

import (
	"context"
	"errors"
	"strconv"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// DLQSuffix is appended to a consumed topic to name its dead-letter topic
// (warehouse.inbound-receiving.analytics.dlq for the projector).
const DLQSuffix = ".dlq"

// DeadLetterWriter is the subset of *kafkago.Writer a dead-lettering
// consumer needs, so unit tests never need a broker.
type DeadLetterWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// dlqBatchTimeout keeps a lone synchronous dead-letter write from waiting
// out kafka-go's 1s default.
const dlqBatchTimeout = 10 * time.Millisecond

// newDLQWriter builds the dead-letter writer for dlqTopic with the fleet's
// sync-writer settings (the same as the outbox RelaySink): the Hash balancer
// on the key (one aggregate's poison stays ordered on one partition),
// RequireAll acks (a dead-letter the broker did not store must not let the
// consumer commit past the message), AllowAutoTopicCreation (the DLQ is only
// written on the rare poison path) and a short BatchTimeout. It does not dial.
func newDLQWriter(brokers []string, dlqTopic string) DeadLetterWriter {
	return &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers...),
		Topic:                  dlqTopic,
		Balancer:               &kafkago.Hash{},
		RequiredAcks:           kafkago.RequireAll,
		BatchTimeout:           dlqBatchTimeout,
		AllowAutoTopicCreation: true,
	}
}

// deadLetterMessage is msg's raw key/value/headers plus the x-dlq-* error
// context: source topic/partition/offset, the error and when it failed.
func deadLetterMessage(msg kafkago.Message, cause error, failedAt time.Time) kafkago.Message {
	headers := append([]kafkago.Header{}, msg.Headers...)
	headers = append(headers,
		kafkago.Header{Key: "x-dlq-source-topic", Value: []byte(msg.Topic)},
		kafkago.Header{Key: "x-dlq-source-partition", Value: []byte(strconv.Itoa(msg.Partition))},
		kafkago.Header{Key: "x-dlq-source-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		kafkago.Header{Key: "x-dlq-error", Value: []byte(cause.Error())},
		kafkago.Header{Key: "x-dlq-failed-at", Value: []byte(failedAt.UTC().Format(time.RFC3339))},
	)
	return kafkago.Message{Key: msg.Key, Value: msg.Value, Headers: headers}
}

// Bounded retry while an auto-created DLQ topic has no leader yet.
const (
	dlqTopicReadyAttempts = 40
	dlqTopicReadyBackoff  = 250 * time.Millisecond
)

// writeDLQ publishes msg, retrying (bounded) while the auto-created DLQ topic
// has no leader yet. Any other error, or exhausting the budget, is returned.
func writeDLQ(ctx context.Context, w DeadLetterWriter, msg kafkago.Message) error {
	var err error
	for attempt := 0; attempt < dlqTopicReadyAttempts; attempt++ {
		if err = w.WriteMessages(ctx, msg); err == nil || !isTopicNotReady(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(dlqTopicReadyBackoff):
		}
	}
	return err
}

// isTopicNotReady reports whether err only says the topic (or its leader)
// does not exist yet: the transient state right after auto-creation.
func isTopicNotReady(err error) bool {
	var werrs kafkago.WriteErrors
	if errors.As(err, &werrs) {
		for _, e := range werrs {
			if e != nil && !isTopicNotReady(e) {
				return false
			}
		}
		return werrs.Count() > 0
	}
	return errors.Is(err, kafkago.UnknownTopicOrPartition) || errors.Is(err, kafkago.LeaderNotAvailable)
}
