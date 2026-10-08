package kafka

import (
	"context"
	"errors"
	"log/slog"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// FacilityTopic is facility-layout's integration topic.
const FacilityTopic = "warehouse.facility.events"

// The only facility-layout types the dock-door consumer acts on,
// byte-identical to its AsyncAPI.
const (
	TypeLocationSlotRegistered     = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	TypeLocationSlotDecommissioned = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
)

// locationSlotData mirrors the part of facility-layout's camelCase
// LocationSlotRegistered / LocationSlotDecommissioned payloads this service
// needs. An absent role arrives as "".
type locationSlotData struct {
	LocationCode string `json:"locationCode"`
	Role         string `json:"role"`
	DockFlow     string `json:"dockFlow"`
}

// DoorRegistrar and DoorDecommissioner are the use cases the dock-door
// consumer drives.
type (
	DoorRegistrar interface {
		Handle(ctx context.Context, eventID string, slot usecases.LocationSlot) (usecases.Outcome, error)
	}
	DoorDecommissioner interface {
		Handle(ctx context.Context, eventID, locationCode string) (usecases.Outcome, error)
	}
)

// DockDoorConsumer consumes FacilityTopic under a stable consumer group and
// keeps the dock_doors local copy (ADR 0003).
type DockDoorConsumer struct {
	Reader        Reader
	Registered    DoorRegistrar
	Decommissions DoorDecommissioner
	Logger        *slog.Logger
	Retry         RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewDockDoorConsumer reads FacilityTopic from brokers under groupID (from
// DOCK_DOOR_CONSUMER_GROUP, never a literal). It does not dial.
func NewDockDoorConsumer(brokers []string, groupID string, reg DoorRegistrar, dec DoorDecommissioner, logger *slog.Logger) *DockDoorConsumer {
	return NewDockDoorConsumerForTopic(brokers, FacilityTopic, groupID, reg, dec, logger)
}

// NewDockDoorConsumerForTopic is NewDockDoorConsumer on an explicit topic.
func NewDockDoorConsumerForTopic(brokers []string, topic, groupID string, reg DoorRegistrar, dec DoorDecommissioner, logger *slog.Logger) *DockDoorConsumer {
	return &DockDoorConsumer{
		Reader:        kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Registered:    reg,
		Decommissions: dec,
		Logger:        defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails.
func (c *DockDoorConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "dock door consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *DockDoorConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure.
func (c *DockDoorConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", FacilityTopic, "error", err)
		return nil
	}
	if e.Type() != TypeLocationSlotRegistered && e.Type() != TypeLocationSlotDecommissioned {
		return nil
	}
	var data locationSlotData
	if err := e.DataAs(&data); err != nil {
		logger.WarnContext(ctx, "skipping a malformed location slot payload", "event_id", e.ID(), "type", e.Type(), "error", err)
		return nil
	}
	var outcome usecases.Outcome
	if e.Type() == TypeLocationSlotRegistered {
		outcome, err = c.Registered.Handle(ctx, e.ID(), usecases.LocationSlot{LocationCode: data.LocationCode, Role: data.Role, DockFlow: data.DockFlow})
	} else {
		outcome, err = c.Decommissions.Handle(ctx, e.ID(), data.LocationCode)
	}
	if errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an invalid location slot event", "event_id", e.ID(), "type", e.Type(), "location_code", data.LocationCode, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "location slot event processed", "event_id", e.ID(), "type", e.Type(), "location_code", data.LocationCode, "outcome", string(outcome))
	return nil
}
