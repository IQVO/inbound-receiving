package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// DockSchedulingTopic is dock-scheduling's integration topic.
const DockSchedulingTopic = "warehouse.dock-scheduling.events"

// The only dock-scheduling types the dock-appointment consumer acts on,
// byte-identical to its AsyncAPI (ADR 0019). Every other type on the topic
// (Rescheduled, NoShow, door closures, ...) is committed past untouched.
const (
	TypeDockAppointmentBooked    = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked"
	TypeDockAppointmentCancelled = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled"
)

// dockAppointmentBookedData is the part of dock-scheduling's snake_case
// DockAppointmentBooked payload the local copy needs (restated here, never
// imported). Unknown fields are ignored.
type dockAppointmentBookedData struct {
	AppointmentID string    `json:"appointment_id"`
	Direction     string    `json:"direction"`
	DoorCode      string    `json:"door_code"`
	Carrier       string    `json:"carrier"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
	AsnNumbers    []string  `json:"asn_numbers"`
	Version       int64     `json:"version"`
}

// dockAppointmentCancelledData is the part of DockAppointmentCancelled the
// local copy needs.
type dockAppointmentCancelledData struct {
	AppointmentID string `json:"appointment_id"`
	Version       int64  `json:"version"`
}

// DockAppointmentBookedApplier and DockAppointmentCancelledApplier are the
// use cases the consumer drives (usecases.ApplyDockAppointmentBooked and
// usecases.ApplyDockAppointmentCancelled).
type (
	DockAppointmentBookedApplier interface {
		Handle(ctx context.Context, eventID string, e usecases.DockAppointmentBooked) (usecases.Outcome, error)
	}
	DockAppointmentCancelledApplier interface {
		Handle(ctx context.Context, eventID, appointmentID string, version int64) (usecases.Outcome, error)
	}
)

// DockAppointmentConsumer consumes DockSchedulingTopic under a stable
// consumer group and keeps the appointments local copy (ADR 0019). It always
// runs, and applying an event publishes nothing: the use cases it drives have
// no outbox.
type DockAppointmentConsumer struct {
	Reader    Reader
	Booked    DockAppointmentBookedApplier
	Cancelled DockAppointmentCancelledApplier
	Logger    *slog.Logger
	Retry     RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewDockAppointmentConsumer reads DockSchedulingTopic from brokers under
// groupID (from DOCK_APPOINTMENT_CONSUMER_GROUP, never a literal). It does
// not dial.
func NewDockAppointmentConsumer(brokers []string, groupID string, booked DockAppointmentBookedApplier, cancelled DockAppointmentCancelledApplier, logger *slog.Logger) *DockAppointmentConsumer {
	return NewDockAppointmentConsumerForTopic(brokers, DockSchedulingTopic, groupID, booked, cancelled, logger)
}

// NewDockAppointmentConsumerForTopic is NewDockAppointmentConsumer on an
// explicit topic, so an integration test can point the identical logic at a
// throwaway topic.
func NewDockAppointmentConsumerForTopic(brokers []string, topic, groupID string, booked DockAppointmentBookedApplier, cancelled DockAppointmentCancelledApplier, logger *slog.Logger) *DockAppointmentConsumer {
	return &DockAppointmentConsumer{
		Reader:    kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Booked:    booked,
		Cancelled: cancelled,
		Logger:    defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *DockAppointmentConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   "dock appointment consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader.
func (c *DockAppointmentConsumer) Close() error { return c.Reader.Close() }

// HandleMessage returns nil for everything deterministic (logged) and a
// non-nil error ONLY for a transient failure, after the unit of work rolled
// back.
func (c *DockAppointmentConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", DockSchedulingTopic, "error", err)
		return nil
	}
	switch e.Type() {
	case TypeDockAppointmentBooked:
		return c.booked(ctx, logger, e.ID(), func(d *dockAppointmentBookedData) error { return e.DataAs(d) })
	case TypeDockAppointmentCancelled:
		return c.cancelled(ctx, logger, e.ID(), func(d *dockAppointmentCancelledData) error { return e.DataAs(d) })
	default:
		return nil
	}
}

var (
	bookedSpec = eventSpec[dockAppointmentBookedData]{
		kind: "DockAppointmentBooked", processed: "dock appointment booked processed",
		attrs: func(d dockAppointmentBookedData) []any {
			return []any{logAppointmentID, d.AppointmentID, logVersion, d.Version}
		},
	}
	cancelledSpec = eventSpec[dockAppointmentCancelledData]{
		kind: "DockAppointmentCancelled", processed: "dock appointment cancelled processed",
		attrs: func(d dockAppointmentCancelledData) []any {
			return []any{logAppointmentID, d.AppointmentID, logVersion, d.Version}
		},
	}
)

const (
	logAppointmentID = "appointment_id"
	logVersion       = "version"
)

func (c *DockAppointmentConsumer) booked(ctx context.Context, logger *slog.Logger, id string, decode func(*dockAppointmentBookedData) error) error {
	return applyEvent(ctx, logger, id, bookedSpec, decode, c.applyBooked)
}

func (c *DockAppointmentConsumer) applyBooked(ctx context.Context, id string, d dockAppointmentBookedData) (usecases.Outcome, error) {
	return c.Booked.Handle(ctx, id, usecases.DockAppointmentBooked{
		AppointmentID: d.AppointmentID, Direction: d.Direction, DoorCode: d.DoorCode, Carrier: d.Carrier,
		WindowStart: d.WindowStart, WindowEnd: d.WindowEnd, AsnNumbers: d.AsnNumbers, Version: d.Version,
	})
}

func (c *DockAppointmentConsumer) cancelled(ctx context.Context, logger *slog.Logger, id string, decode func(*dockAppointmentCancelledData) error) error {
	return applyEvent(ctx, logger, id, cancelledSpec, decode, c.applyCancelled)
}

func (c *DockAppointmentConsumer) applyCancelled(ctx context.Context, id string, d dockAppointmentCancelledData) (usecases.Outcome, error) {
	return c.Cancelled.Handle(ctx, id, d.AppointmentID, d.Version)
}
