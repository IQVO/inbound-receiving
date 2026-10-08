package kafka

import (
	"context"
	"errors"
	"log/slog"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// ProductTopic is product-master's integration topic.
const ProductTopic = "warehouse.product-master.events"

// TypeProductRegistered is the only type the product consumer acts on,
// byte-identical to product-master's AsyncAPI. Every other type on the topic
// is ignored.
const TypeProductRegistered = "com.warehouse.wms.product-master.product.ProductRegistered"

// productRegisteredData is the part of product-master's v1 ProductRegistered
// payload this service needs (restated here, never imported).
type productRegisteredData struct {
	SKU string `json:"sku"`
}

// SkuApplier is the use case the product consumer drives
// (usecases.ApplyProductRegistered).
type SkuApplier interface {
	Handle(ctx context.Context, eventID, rawSKU string) (usecases.Outcome, error)
}

// ProductConsumer consumes ProductTopic under a stable consumer group and
// keeps the known_skus local copy (ADR 0003).
type ProductConsumer struct {
	Reader Reader
	Apply  SkuApplier
	Logger *slog.Logger
	Retry  RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewProductConsumer reads ProductTopic from brokers under groupID (from
// PRODUCT_CONSUMER_GROUP, never a literal). It does not dial.
func NewProductConsumer(brokers []string, groupID string, apply SkuApplier, logger *slog.Logger) *ProductConsumer {
	return NewProductConsumerForTopic(brokers, ProductTopic, groupID, apply, logger)
}

// NewProductConsumerForTopic is NewProductConsumer on an explicit topic, so an
// integration test can point the identical logic at a throwaway topic.
func NewProductConsumerForTopic(brokers []string, topic, groupID string, apply SkuApplier, logger *slog.Logger) *ProductConsumer {
	return &ProductConsumer{
		Reader: kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Apply:  apply,
		Logger: defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *ProductConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "product consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *ProductConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure, after the unit of work rolled
// back.
func (c *ProductConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", ProductTopic, "error", err)
		return nil
	}
	if e.Type() != TypeProductRegistered {
		return nil
	}
	var data productRegisteredData
	if err := e.DataAs(&data); err != nil {
		logger.WarnContext(ctx, "skipping a malformed ProductRegistered payload", "event_id", e.ID(), "error", err)
		return nil
	}
	outcome, err := c.Apply.Handle(ctx, e.ID(), data.SKU)
	if errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an invalid ProductRegistered", "event_id", e.ID(), "sku", data.SKU, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "product registered processed", "event_id", e.ID(), "sku", data.SKU, "outcome", string(outcome))
	return nil
}
