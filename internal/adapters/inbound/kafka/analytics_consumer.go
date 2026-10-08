package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// skipWarnEvery bounds the WARN lines for skipped (non-CloudEvents) messages:
// the first skip is logged, then at most one line per interval carrying the
// number suppressed since.
const skipWarnEvery = time.Minute

// The `entity` segment of the type of each published aggregate.
const (
	entityAsn         = "asn"
	entityAppointment = "dockappointment"
	entityReceipt     = "receipt"
)

func entityOf(k report.Kind) string {
	switch {
	case k.IsAsn():
		return entityAsn
	case k.IsAppointment():
		return entityAppointment
	default:
		return entityReceipt
	}
}

// analyticsKinds maps the FULL CloudEvents type of the nine published events
// to their report.Kind. Every other type on the topic is acknowledged and
// ignored.
var analyticsKinds = func() map[string]report.Kind {
	m := make(map[string]report.Kind, len(report.Kinds()))
	for _, k := range report.Kinds() {
		m[cloudevents.Type(entityOf(k), string(k))] = k
	}
	return m
}()

// analyticsData is the union of the analytics payload fields the projection
// reads (hand-mirrored from apis/asyncapi.yaml, never imported from the
// domain). Pointers distinguish "absent" from zero.
type analyticsData struct {
	AsnNumber       string     `json:"asn_number"`
	ExpectedArrival *time.Time `json:"expected_arrival"`
	AppointmentID   string     `json:"appointment_id"`
	ReceiptID       string     `json:"receipt_id"`
	LineNo          int        `json:"line_no"`
	SKU             string     `json:"sku"`
	Quantity        int64      `json:"quantity"`
	Condition       string     `json:"condition"`
	Discrepancies   []struct {
		LineNo int    `json:"line_no"`
		SKU    string `json:"sku"`
		Kind   string `json:"kind"`
	} `json:"discrepancies"`
}

// AnalyticsConsumer is the projector's consumer: it reads the analytics topic
// under a FIXED, env-supplied consumer group (at-least-once, offsets
// committed only after success -- never a full-replay cache) and folds each
// event into the analytical model through report.Projection.
//
// Outcomes per message (ADR 0006 section 5):
//   - not a CloudEvents 1.0 message: skipped and committed past, with a
//     rate-limited WARN;
//   - a valid CloudEvent of any other type: ignored, committed past;
//   - a known type with an unusable payload, or one the store
//     deterministically rejects: dead-lettered at once, committed past;
//   - a transient failure (database down, timeout): the SAME message is
//     retried with capped exponential backoff and the offset is NOT committed;
//     it is never dead-lettered.
type AnalyticsConsumer struct {
	Reader     Reader
	Projection report.Projection
	DLQ        DeadLetterWriter
	// DLQTopic is recorded in the poison log line.
	DLQTopic string
	Logger   *slog.Logger
	Retry    RetryPolicy

	// Now is the clock of the skip-warning sampler and the x-dlq-failed-at
	// header (time.Now when nil).
	Now func() time.Time

	skipsOnce sync.Once
	skips     *skipSampler
	sleep     sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewAnalyticsConsumer constructs the consumer for topic under groupID (an
// env-configured value from the composition root, never a literal). Its
// dead-letter writer targets topic + DLQSuffix. Nothing dials Kafka here.
func NewAnalyticsConsumer(brokers []string, topic, groupID string, projection report.Projection, logger *slog.Logger) *AnalyticsConsumer {
	return &AnalyticsConsumer{
		Reader:     kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Projection: projection,
		DLQ:        newDLQWriter(brokers, topic+DLQSuffix),
		DLQTopic:   topic + DLQSuffix,
		Logger:     defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails.
func (c *AnalyticsConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: c.HandleMessage,
		logger: defaultLogger(c.Logger),
		name:   "analytics consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *AnalyticsConsumer) Close() error {
	var err error
	if c.Reader != nil {
		err = c.Reader.Close()
	}
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage processes one message; see the type comment for outcomes. A
// non-nil error is always transient (the loop retries the same message).
func (c *AnalyticsConsumer) HandleMessage(ctx context.Context, msg kafkago.Message) error {
	evt, err := cloudevents.Decode(msg.Value)
	if err != nil {
		c.skipped(ctx, msg, err)
		return nil
	}
	kind, known := analyticsKinds[evt.Type()]
	if !known {
		defaultLogger(c.Logger).DebugContext(ctx, "analytics: ignoring event of another type", "type", evt.Type(), "event_id", evt.ID())
		return nil
	}
	pe, err := toReportEvent(kind, evt)
	if err != nil {
		return c.deadLetter(ctx, msg, err)
	}
	if _, err := c.Projection.Apply(ctx, pe); err != nil {
		if errors.Is(err, report.ErrRejected) {
			return c.deadLetter(ctx, msg, err)
		}
		return fmt.Errorf("analytics: project %s %s: %w", kind, evt.ID(), err)
	}
	return nil
}

// toReportEvent validates a decoded CloudEvent of a known kind and maps it to
// a report.Event. Every error is deterministic (the same bytes can never
// pass).
func toReportEvent(kind report.Kind, evt ce.Event) (report.Event, error) {
	var d analyticsData
	if err := evt.DataAs(&d); err != nil {
		return report.Event{}, fmt.Errorf("decode %s data: %w", kind, err)
	}
	e := report.Event{Kind: kind, EventID: evt.ID(), At: evt.Time().UTC(), AsnNumber: d.AsnNumber}
	var subjectKey string
	switch {
	case kind.IsAsn():
		subjectKey = d.AsnNumber
		if kind == report.KindAsnRegistered && d.ExpectedArrival != nil {
			x := d.ExpectedArrival.UTC()
			e.ExpectedArrival = &x
		}
	case kind.IsAppointment():
		e.AppointmentID, subjectKey = d.AppointmentID, d.AppointmentID
	default:
		e.ReceiptID, subjectKey = d.ReceiptID, d.ReceiptID
		applyReceiptDetail(&e, d)
	}
	if subjectKey != evt.Subject() {
		return report.Event{}, fmt.Errorf("%s %s: aggregate id %q does not match subject %q", kind, evt.ID(), subjectKey, evt.Subject())
	}
	if err := e.Validate(); err != nil {
		return report.Event{}, err
	}
	return e, nil
}

func applyReceiptDetail(e *report.Event, d analyticsData) {
	switch e.Kind {
	case report.KindReceiptLineReceived:
		e.Line = &report.LineReceived{LineNo: d.LineNo, SKU: d.SKU, Quantity: d.Quantity, Condition: report.Condition(d.Condition)}
	case report.KindReceiptClosed:
		e.Discrepancies = make([]report.Discrepancy, 0, len(d.Discrepancies))
		for _, x := range d.Discrepancies {
			e.Discrepancies = append(e.Discrepancies, report.Discrepancy{LineNo: x.LineNo, SKU: x.SKU, Kind: report.DiscrepancyKind(x.Kind)})
		}
	}
}

func (c *AnalyticsConsumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// skipped logs a skipped non-CloudEvents message, sampled.
func (c *AnalyticsConsumer) skipped(ctx context.Context, msg kafkago.Message, cause error) {
	c.skipsOnce.Do(func() { c.skips = &skipSampler{every: skipWarnEvery, now: c.now} })
	if emit, suppressed := c.skips.allow(); emit {
		defaultLogger(c.Logger).WarnContext(ctx, "analytics: skipping message that is not a CloudEvents 1.0 event (further skips are counted, not logged, until the next interval)",
			"error", cause, "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset, "suppressed_since_last_warning", suppressed)
	}
}

// deadLetter publishes the raw, unmodified message to the DLQ with error
// context in headers, then lets the loop commit past it. A DLQ write failure
// is returned (transient: the loop retries the message) so a poison message
// is never lost.
func (c *AnalyticsConsumer) deadLetter(ctx context.Context, msg kafkago.Message, cause error) error {
	defaultLogger(c.Logger).ErrorContext(ctx, "analytics: dead-lettering a message that can never be projected",
		"error", cause, "dlq_topic", c.DLQTopic, "partition", msg.Partition, "offset", msg.Offset)
	if err := writeDLQ(ctx, c.DLQ, deadLetterMessage(msg, cause, c.now())); err != nil {
		return fmt.Errorf("analytics: publish to dead-letter topic %s: %w", c.DLQTopic, err)
	}
	return nil
}

// skipSampler lets the first call through, then at most one per interval,
// reporting how many were suppressed in between.
type skipSampler struct {
	mu         sync.Mutex
	every      time.Duration
	now        func() time.Time
	last       time.Time
	suppressed int64
}

func (s *skipSampler) allow() (emit bool, suppressed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last.IsZero() || now.Sub(s.last) >= s.every {
		suppressed, s.suppressed, s.last = s.suppressed, 0, now
		return true, suppressed
	}
	s.suppressed++
	return false, 0
}
