package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// InventoryTopic is inventory-storage's integration topic.
const InventoryTopic = "warehouse.inventory.events"

// TypeContainerStowed is inventory-storage's container stow fact,
// byte-identical to its AsyncAPI (ADR 0021). It is the only stow fact that
// names a license plate, so it is the only one this context can join to a
// receipt carton; ItemStowed (SKU and bin only) and every other type on the
// topic are committed past untouched.
const TypeContainerStowed = "com.warehouse.wms.inventory-storage.container.ContainerStowed"

// containerStowedData is the part of inventory-storage's snake_case
// ContainerStowed payload the stow copy needs (restated here, never
// imported): the license plate and when it was stowed. Unknown fields (sku,
// qty_eaches, owner_ref, site_id, bin) are ignored.
type containerStowedData struct {
	LPN string    `json:"lpn"`
	At  time.Time `json:"at"`
}

// ContainerStowedApplier is the use case the consumer drives
// (usecases.ApplyContainerStowed).
type ContainerStowedApplier interface {
	Handle(ctx context.Context, eventID string, e usecases.ContainerStowed) (usecases.Outcome, error)
}

// CartonStowConsumer consumes InventoryTopic under a stable consumer group
// and keeps the carton_stows local copy (ADR 0021). It always runs; the use
// case it drives may raise ReceiptStowCompleted through the outbox of this
// context, never anything towards inventory-storage.
type CartonStowConsumer struct {
	Reader Reader
	Stows  ContainerStowedApplier
	Logger *slog.Logger
	Retry  RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewCartonStowConsumer reads InventoryTopic from brokers under groupID (from
// CARTON_STOW_CONSUMER_GROUP, never a literal). It does not dial.
func NewCartonStowConsumer(brokers []string, groupID string, stows ContainerStowedApplier, logger *slog.Logger) *CartonStowConsumer {
	return NewCartonStowConsumerForTopic(brokers, InventoryTopic, groupID, stows, logger)
}

// NewCartonStowConsumerForTopic is NewCartonStowConsumer on an explicit
// topic, so an integration test can point the identical logic at a throwaway
// topic.
func NewCartonStowConsumerForTopic(brokers []string, topic, groupID string, stows ContainerStowedApplier, logger *slog.Logger) *CartonStowConsumer {
	cfg := readerConfig(brokers, topic, groupID)
	cfg.MaxWait = consumerMaxWait
	return &CartonStowConsumer{Reader: kafkago.NewReader(cfg), Stows: stows, Logger: defaultLogger(logger)}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *CartonStowConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "carton stow consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *CartonStowConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure, after the unit of work rolled
// back.
func (c *CartonStowConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", InventoryTopic, "error", err)
		return nil
	}
	if e.Type() != TypeContainerStowed {
		return nil
	}
	return applyEvent(ctx, logger, e.ID(), containerStowedSpec, func(d *containerStowedData) error { return e.DataAs(d) }, c.apply)
}

// Ignored is the normal outcome of a container moved to another bin, so it
// logs at DEBUG.
var containerStowedSpec = eventSpec[containerStowedData]{
	kind: "ContainerStowed", processed: "container stowed processed", quietIgnored: true,
	attrs: func(d containerStowedData) []any { return []any{"lpn", d.LPN, "at", d.At} },
}

func (c *CartonStowConsumer) apply(ctx context.Context, id string, d containerStowedData) (usecases.Outcome, error) {
	return c.Stows.Handle(ctx, id, usecases.ContainerStowed{LPN: d.LPN, StowedAt: d.At})
}
