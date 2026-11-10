package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// YardManagementTopic is yard-management's integration topic.
const YardManagementTopic = "warehouse.yard-management.events"

// TypeVisitCheckedIn is yard-management's gate arrival, byte-identical to its
// AsyncAPI (ADR 0021). Every other type on the topic (VisitRejected,
// TrailerDocked, TrailerDeparted, ...) is committed past untouched.
const TypeVisitCheckedIn = "com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn"

// consumerMaxWait bounds how long a dock-to-stock reader waits for a fetch to
// fill, so a quiet topic (or one that does not exist yet) never holds up
// shutdown for the library's 10 s default.
const consumerMaxWait = 500 * time.Millisecond

// visitCheckedInData is the part of yard-management's snake_case
// VisitCheckedIn payload the arrival copy needs (restated here, never
// imported). appointment_id is null for a walk-in visit. Unknown fields
// (site_id, carrier, trailers, ...) are ignored.
type visitCheckedInData struct {
	VisitID       string    `json:"visit_id"`
	AppointmentID string    `json:"appointment_id"`
	ArrivedAt     time.Time `json:"arrived_at"`
	Version       int64     `json:"version"`
}

// VisitCheckedInApplier is the use case the consumer drives
// (usecases.ApplyVisitCheckedIn).
type VisitCheckedInApplier interface {
	Handle(ctx context.Context, eventID string, e usecases.VisitCheckedIn) (usecases.Outcome, error)
}

// VisitArrivalConsumer consumes YardManagementTopic under a stable consumer
// group and keeps the visit_arrivals local copy (ADR 0021). It always runs;
// the use case it drives may raise ReceiptStowCompleted through the outbox of
// this context, never anything towards yard-management.
type VisitArrivalConsumer struct {
	Reader   Reader
	Arrivals VisitCheckedInApplier
	Logger   *slog.Logger
	Retry    RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewVisitArrivalConsumer reads YardManagementTopic from brokers under
// groupID (from VISIT_ARRIVAL_CONSUMER_GROUP, never a literal). It does not
// dial, and a topic that does not exist yet is retried by the reader like on
// every other consumer.
func NewVisitArrivalConsumer(brokers []string, groupID string, arrivals VisitCheckedInApplier, logger *slog.Logger) *VisitArrivalConsumer {
	return NewVisitArrivalConsumerForTopic(brokers, YardManagementTopic, groupID, arrivals, logger)
}

// NewVisitArrivalConsumerForTopic is NewVisitArrivalConsumer on an explicit
// topic, so an integration test can point the identical logic at a throwaway
// topic.
func NewVisitArrivalConsumerForTopic(brokers []string, topic, groupID string, arrivals VisitCheckedInApplier, logger *slog.Logger) *VisitArrivalConsumer {
	cfg := readerConfig(brokers, topic, groupID)
	cfg.MaxWait = consumerMaxWait
	return &VisitArrivalConsumer{Reader: kafkago.NewReader(cfg), Arrivals: arrivals, Logger: defaultLogger(logger)}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *VisitArrivalConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "visit arrival consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *VisitArrivalConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure, after the unit of work rolled
// back.
func (c *VisitArrivalConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", YardManagementTopic, "error", err)
		return nil
	}
	if e.Type() != TypeVisitCheckedIn {
		return nil
	}
	return applyEvent(ctx, logger, e.ID(), visitCheckedInSpec, func(d *visitCheckedInData) error { return e.DataAs(d) }, c.apply)
}

// A walk-in visit (no appointment) is Ignored by design, so that outcome logs
// at DEBUG.
var visitCheckedInSpec = eventSpec[visitCheckedInData]{
	kind: "VisitCheckedIn", processed: "visit checked in processed", quietIgnored: true,
	attrs: func(d visitCheckedInData) []any {
		return []any{"visit_id", d.VisitID, logAppointmentID, d.AppointmentID, logVersion, d.Version}
	},
}

func (c *VisitArrivalConsumer) apply(ctx context.Context, id string, d visitCheckedInData) (usecases.Outcome, error) {
	return c.Arrivals.Handle(ctx, id, usecases.VisitCheckedIn{
		VisitID: d.VisitID, AppointmentID: d.AppointmentID, ArrivedAt: d.ArrivedAt, Version: d.Version,
	})
}
