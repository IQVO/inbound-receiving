package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// The two product-master types the packaging consumer acts on, byte-identical
// to product-master's AsyncAPI. Every other type on ProductTopic is ignored
// (ProductRegistered belongs to the product consumer, ProductPackLevelMeasured
// carries no fact receiving needs).
const (
	TypePackLevelDeclared = "com.warehouse.wms.product-master.product.ProductPackLevelDeclared"
	TypePackLevelRetired  = "com.warehouse.wms.product-master.product.ProductPackLevelRetired"
)

// packLevelDeclaredData is the part of product-master's ProductPackLevelDeclared
// payload the PackagingView needs (restated here, never imported; snake_case as
// product-master ships it).
type packLevelDeclaredData struct {
	SKU           string    `json:"sku"`
	Level         string    `json:"level"`
	GTIN          string    `json:"gtin"`
	ChildGTIN     string    `json:"child_gtin"`
	QtyOfChild    int64     `json:"qty_of_child"`
	UnitsPerPack  int64     `json:"units_per_pack"`
	EffectiveFrom time.Time `json:"effective_from"`
	Version       int64     `json:"version"`
}

// packLevelRetiredData is the part of ProductPackLevelRetired this service needs.
type packLevelRetiredData struct {
	GTIN        string    `json:"gtin"`
	EffectiveTo time.Time `json:"effective_to"`
	Version     int64     `json:"version"`
}

// PackLevelDeclaredApplier is the use case the consumer drives for a declared
// level (usecases.ApplyPackLevelDeclared).
type PackLevelDeclaredApplier interface {
	Handle(ctx context.Context, eventID string, d usecases.PackLevelDeclared) (usecases.Outcome, error)
}

// PackLevelRetiredApplier is the use case the consumer drives for a retired
// level (usecases.ApplyPackLevelRetired).
type PackLevelRetiredApplier interface {
	Handle(ctx context.Context, eventID, gtin string, effectiveTo time.Time, version int64) (usecases.Outcome, error)
}

// PackagingConsumer consumes ProductTopic under its OWN stable consumer group
// (a new group replays the topic from the start, so the PackagingView is
// complete whenever the service first runs) and keeps the PackagingView local
// projection (contracts R8). It never calls product-master.
type PackagingConsumer struct {
	Reader   Reader
	Declared PackLevelDeclaredApplier
	Retired  PackLevelRetiredApplier
	Logger   *slog.Logger
	Retry    RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewPackagingConsumer reads ProductTopic from brokers under groupID (from
// PACKAGING_CONSUMER_GROUP, never a literal). It does not dial.
func NewPackagingConsumer(brokers []string, groupID string, declared PackLevelDeclaredApplier, retired PackLevelRetiredApplier, logger *slog.Logger) *PackagingConsumer {
	return NewPackagingConsumerForTopic(brokers, ProductTopic, groupID, declared, retired, logger)
}

// NewPackagingConsumerForTopic is NewPackagingConsumer on an explicit topic, so
// an integration test can point the identical logic at a throwaway topic.
func NewPackagingConsumerForTopic(brokers []string, topic, groupID string, declared PackLevelDeclaredApplier, retired PackLevelRetiredApplier, logger *slog.Logger) *PackagingConsumer {
	return &PackagingConsumer{
		Reader:   kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Declared: declared,
		Retired:  retired,
		Logger:   defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *PackagingConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "packaging consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *PackagingConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure, after the unit of work rolled
// back.
func (c *PackagingConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", ProductTopic, "error", err)
		return nil
	}
	switch e.Type() {
	case TypePackLevelDeclared:
		return c.declared(ctx, logger, e.ID(), func(d *packLevelDeclaredData) error { return e.DataAs(d) })
	case TypePackLevelRetired:
		return c.retired(ctx, logger, e.ID(), func(d *packLevelRetiredData) error { return e.DataAs(d) })
	default:
		return nil
	}
}

func (c *PackagingConsumer) declared(ctx context.Context, logger *slog.Logger, id string, decode func(*packLevelDeclaredData) error) error {
	var data packLevelDeclaredData
	if err := decode(&data); err != nil {
		logger.WarnContext(ctx, "skipping a malformed ProductPackLevelDeclared payload", "event_id", id, "error", err)
		return nil
	}
	outcome, err := c.Declared.Handle(ctx, id, usecases.PackLevelDeclared{
		SKU: data.SKU, Level: data.Level, GTIN: data.GTIN, ChildGTIN: data.ChildGTIN, QtyOfChild: data.QtyOfChild,
		UnitsPerPack: data.UnitsPerPack, EffectiveFrom: data.EffectiveFrom, Version: data.Version,
	})
	if errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an invalid ProductPackLevelDeclared", "event_id", id, "gtin", data.GTIN, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "pack level declared processed", "event_id", id, "sku", data.SKU, "level", data.Level, "outcome", string(outcome))
	return nil
}

func (c *PackagingConsumer) retired(ctx context.Context, logger *slog.Logger, id string, decode func(*packLevelRetiredData) error) error {
	var data packLevelRetiredData
	if err := decode(&data); err != nil {
		logger.WarnContext(ctx, "skipping a malformed ProductPackLevelRetired payload", "event_id", id, "error", err)
		return nil
	}
	outcome, err := c.Retired.Handle(ctx, id, data.GTIN, data.EffectiveTo, data.Version)
	if errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an invalid ProductPackLevelRetired", "event_id", id, "gtin", data.GTIN, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "pack level retired processed", "event_id", id, "gtin", data.GTIN, "outcome", string(outcome))
	return nil
}
