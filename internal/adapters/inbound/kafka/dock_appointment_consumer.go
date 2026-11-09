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

// The dock-scheduling types the dock-appointment consumer acts on,
// byte-identical to its AsyncAPI (ADR 0019, 0020). Every other type on the
// topic (NoShow, check-in, door closures, ...) is committed past untouched.
// DoorReassigned may not be on the integration topic yet; nothing depends on
// it arriving.
const (
	TypeDockAppointmentBooked         = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked"
	TypeDockAppointmentCancelled      = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled"
	TypeDockAppointmentRescheduled    = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled"
	TypeDockAppointmentDoorReassigned = "com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned"
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

// dockAppointmentRescheduledData is the part of DockAppointmentRescheduled
// the local copy needs; previous_door_code is read for the log only.
type dockAppointmentRescheduledData struct {
	AppointmentID    string    `json:"appointment_id"`
	DoorCode         string    `json:"door_code"`
	PreviousDoorCode string    `json:"previous_door_code"`
	WindowStart      time.Time `json:"window_start"`
	WindowEnd        time.Time `json:"window_end"`
	Version          int64     `json:"version"`
}

// dockAppointmentDoorReassignedData is the part of
// DockAppointmentDoorReassigned the local copy needs.
type dockAppointmentDoorReassignedData struct {
	AppointmentID string `json:"appointment_id"`
	FromDoorCode  string `json:"from_door_code"`
	ToDoorCode    string `json:"to_door_code"`
	Version       int64  `json:"version"`
}

// The use cases the consumer drives (usecases.ApplyDockAppointmentBooked,
// ...Cancelled, ...Rescheduled and ...DoorReassigned).
type (
	DockAppointmentBookedApplier interface {
		Handle(ctx context.Context, eventID string, e usecases.DockAppointmentBooked) (usecases.Outcome, error)
	}
	DockAppointmentCancelledApplier interface {
		Handle(ctx context.Context, eventID, appointmentID string, version int64) (usecases.Outcome, error)
	}
	DockAppointmentRescheduledApplier interface {
		Handle(ctx context.Context, eventID string, e usecases.DockAppointmentRescheduled) (usecases.Outcome, error)
	}
	DockAppointmentDoorReassignedApplier interface {
		Handle(ctx context.Context, eventID string, e usecases.DockAppointmentDoorReassigned) (usecases.Outcome, error)
	}
)

// DockAppointmentAppliers groups the use cases one DockAppointmentConsumer
// drives.
type DockAppointmentAppliers struct {
	Booked         DockAppointmentBookedApplier
	Cancelled      DockAppointmentCancelledApplier
	Rescheduled    DockAppointmentRescheduledApplier
	DoorReassigned DockAppointmentDoorReassignedApplier
}

// DockAppointmentConsumer consumes DockSchedulingTopic under a stable
// consumer group and keeps the appointments local copy (ADR 0019, 0020). It
// always runs, and applying an event publishes nothing: the use cases it
// drives have no outbox.
type DockAppointmentConsumer struct {
	Reader         Reader
	Booked         DockAppointmentBookedApplier
	Cancelled      DockAppointmentCancelledApplier
	Rescheduled    DockAppointmentRescheduledApplier
	DoorReassigned DockAppointmentDoorReassignedApplier
	Logger         *slog.Logger
	Retry          RetryPolicy

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewDockAppointmentConsumer reads DockSchedulingTopic from brokers under
// groupID (from DOCK_APPOINTMENT_CONSUMER_GROUP, never a literal). It does
// not dial.
func NewDockAppointmentConsumer(brokers []string, groupID string, appliers DockAppointmentAppliers, logger *slog.Logger) *DockAppointmentConsumer {
	return NewDockAppointmentConsumerForTopic(brokers, DockSchedulingTopic, groupID, appliers, logger)
}

// NewDockAppointmentConsumerForTopic is NewDockAppointmentConsumer on an
// explicit topic, so an integration test can point the identical logic at a
// throwaway topic.
func NewDockAppointmentConsumerForTopic(brokers []string, topic, groupID string, appliers DockAppointmentAppliers, logger *slog.Logger) *DockAppointmentConsumer {
	return &DockAppointmentConsumer{
		Reader:         kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Booked:         appliers.Booked,
		Cancelled:      appliers.Cancelled,
		Rescheduled:    appliers.Rescheduled,
		DoorReassigned: appliers.DoorReassigned,
		Logger:         defaultLogger(logger),
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
		return applyEvent(ctx, logger, e.ID(), bookedSpec, func(d *dockAppointmentBookedData) error { return e.DataAs(d) }, c.applyBooked)
	case TypeDockAppointmentCancelled:
		return applyEvent(ctx, logger, e.ID(), cancelledSpec, func(d *dockAppointmentCancelledData) error { return e.DataAs(d) }, c.applyCancelled)
	case TypeDockAppointmentRescheduled:
		return applyEvent(ctx, logger, e.ID(), rescheduledSpec, func(d *dockAppointmentRescheduledData) error { return e.DataAs(d) }, c.applyRescheduled)
	case TypeDockAppointmentDoorReassigned:
		return applyEvent(ctx, logger, e.ID(), doorReassignedSpec, func(d *dockAppointmentDoorReassignedData) error { return e.DataAs(d) }, c.applyDoorReassigned)
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
	// An Ignored reschedule or reassignment is normal (an OUTBOUND
	// appointment is never in the copy, an older version is superseded), so
	// it logs at DEBUG.
	rescheduledSpec = eventSpec[dockAppointmentRescheduledData]{
		kind: "DockAppointmentRescheduled", processed: "dock appointment rescheduled processed", quietIgnored: true,
		attrs: func(d dockAppointmentRescheduledData) []any {
			return []any{logAppointmentID, d.AppointmentID, logVersion, d.Version, "door_code", d.DoorCode, "previous_door_code", d.PreviousDoorCode}
		},
	}
	doorReassignedSpec = eventSpec[dockAppointmentDoorReassignedData]{
		kind: "DockAppointmentDoorReassigned", processed: "dock appointment door reassigned processed", quietIgnored: true,
		attrs: func(d dockAppointmentDoorReassignedData) []any {
			return []any{logAppointmentID, d.AppointmentID, logVersion, d.Version, "from_door_code", d.FromDoorCode, "to_door_code", d.ToDoorCode}
		},
	}
)

const (
	logAppointmentID = "appointment_id"
	logVersion       = "version"
)

func (c *DockAppointmentConsumer) applyBooked(ctx context.Context, id string, d dockAppointmentBookedData) (usecases.Outcome, error) {
	return c.Booked.Handle(ctx, id, usecases.DockAppointmentBooked{
		AppointmentID: d.AppointmentID, Direction: d.Direction, DoorCode: d.DoorCode, Carrier: d.Carrier,
		WindowStart: d.WindowStart, WindowEnd: d.WindowEnd, AsnNumbers: d.AsnNumbers, Version: d.Version,
	})
}

func (c *DockAppointmentConsumer) applyCancelled(ctx context.Context, id string, d dockAppointmentCancelledData) (usecases.Outcome, error) {
	return c.Cancelled.Handle(ctx, id, d.AppointmentID, d.Version)
}

func (c *DockAppointmentConsumer) applyRescheduled(ctx context.Context, id string, d dockAppointmentRescheduledData) (usecases.Outcome, error) {
	return c.Rescheduled.Handle(ctx, id, usecases.DockAppointmentRescheduled{
		AppointmentID: d.AppointmentID, DoorCode: d.DoorCode, WindowStart: d.WindowStart, WindowEnd: d.WindowEnd, Version: d.Version,
	})
}

func (c *DockAppointmentConsumer) applyDoorReassigned(ctx context.Context, id string, d dockAppointmentDoorReassignedData) (usecases.Outcome, error) {
	return c.DoorReassigned.Handle(ctx, id, usecases.DockAppointmentDoorReassigned{
		AppointmentID: d.AppointmentID, FromDoorCode: d.FromDoorCode, ToDoorCode: d.ToDoorCode, Version: d.Version,
	})
}
