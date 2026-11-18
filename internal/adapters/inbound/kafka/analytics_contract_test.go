package kafka

// Pinned-contract SELF-PIN tests: this repo's own apis/asyncapi.yaml pin
// covers the analytics topic the projector consumes, whose consumer is the
// fleet's one dead-lettering reader (ADR 0006 section 5) — E4 covers the DLQ
// policy. The analytics twins reuse the INTEGRATION event type with a
// `urn:warehouse:inbound-receiving:analytics:<Name>:vN` dataschema.

import (
	"context"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// analyticsProjected are the kinds the projection folds (report.Kinds minus
// the four retired dockappointment legacy kinds, which stay routed but are no
// longer documented on the channel — ADR 0019/0022).
// analyticsIgnored are documented on the channel but not projected.
var (
	analyticsProjected = []string{
		"ASNRegistered", "ASNCancelled", "AsnLateForArrival",
		"ReceiptOpened", "ReceiptLineReceived", "ReceiptClosed",
		"ReceiptStowCompleted", "ReceivingAdviceIssued",
	}
	analyticsIgnored = []string{
		"ReceiptCartonReceived", "CartonDiscrepancyRaised", "ReceivingAdviceOverdue", "ReceivingPolicyDeclared",
	}
)

func analyticsPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "inbound-receiving.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, outboundkafka.AnalyticsTopic)
	if name != outboundkafka.AnalyticsTopic {
		t.Fatalf("channel key %q, want the analytics topic constant %q", name, outboundkafka.AnalyticsTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: every ACTIVE projected kind's type is documented on the analytics
// channel of the pinned self-spec; the retired legacy kinds stay routed but
// undocumented; the documented-but-ignored kinds are not routed (exactly
// what analyticsKinds and the channel each say).
func TestPinnedAnalyticsSpecCoversSubscriptions(t *testing.T) {
	_, _, types := analyticsPinned(t)
	for _, k := range report.Kinds() {
		typ := cloudevents.Type(entityOf(k), string(k))
		_, documented := types[typ]
		_, routed := analyticsKinds[typ]
		if !routed {
			t.Errorf("kind %s is not routed by the consumer", k)
		}
		if k.IsAppointment() && documented {
			t.Errorf("retired legacy kind %s is still documented on the channel", typ)
		}
		if !k.IsAppointment() && !documented {
			t.Errorf("active kind %s (%s) is not documented on the pinned channel", k, typ)
		}
	}
	for _, name := range analyticsIgnored {
		typ := cloudevents.Type(entityOfKindName(name), name)
		if _, ok := analyticsKinds[typ]; ok {
			t.Errorf("documented-but-ignored kind %s must not be routed", name)
		}
		if _, ok := types[typ]; !ok {
			t.Errorf("ignored kind %s is not documented on the channel", name)
		}
	}
}

// countingProjection records exactly-once routing per message.
type countingProjection struct {
	mu     sync.Mutex
	events []report.Event
	errs   []error
}

func (p *countingProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return false, err
		}
	}
	p.events = append(p.events, e)
	return true, nil
}

func (p *countingProjection) applied() []report.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]report.Event(nil), p.events...)
}

func analyticsGolden(t *testing.T, doc, ch map[string]any, name string) map[string]any {
	t.Helper()
	return pinnedGolden(t, doc, ch, cloudevents.Type(entityOfKindName(name), name))
}

func entityOfKindName(name string) string {
	for _, k := range report.Kinds() {
		if string(k) == name {
			return entityOf(k)
		}
	}
	if name == "ReceivingPolicyDeclared" {
		return "policy" // ADR 0026: the one ignored type of another entity
	}
	return entityReceipt
}

// E2: each projected kind's golden decodes through the real consumer into
// exactly one projection carrying the envelope's own id; the ignored kinds
// project nothing; the retired legacy kinds still project (ADR 0022).
func TestAnalyticsGoldenEventsDecode(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := analyticsPinned(t)
	proj := &countingProjection{}
	h := newAnHarness(proj)
	feed := func(env map[string]any) error {
		return h.consumer.HandleMessage(ctx, kafkago.Message{Topic: outboundkafka.AnalyticsTopic, Value: goldenBytes(t, env)})
	}

	for _, name := range analyticsProjected {
		golden := analyticsGolden(t, doc, ch, name)
		if err := feed(golden); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		applied := proj.applied()
		if len(applied) == 0 || applied[len(applied)-1].Kind != report.Kind(name) {
			t.Fatalf("%s did not project (last=%v)", name, applied)
		}
		if got := applied[len(applied)-1].EventID; got != golden["id"].(string) {
			t.Errorf("%s projected id %q, want the envelope's %q (the store's dedupe key)", name, got, golden["id"])
		}
	}
	if got := len(proj.applied()); got != len(analyticsProjected) {
		t.Fatalf("projection count = %d, want %d (exactly-once routing)", got, len(analyticsProjected))
	}

	for _, name := range analyticsIgnored {
		if err := feed(analyticsGolden(t, doc, ch, name)); err != nil {
			t.Fatalf("ignored kind %s: %v", name, err)
		}
	}
	if got := len(proj.applied()); got != len(analyticsProjected) {
		t.Fatalf("ignored kinds were projected: %d -> %d", len(analyticsProjected), got)
	}

	legacy := anApptEvent(t, "legacy-appt-golden-1", "DockAppointmentBooked", anAppt,
		time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC), map[string]any{})
	if err := jsonFeed(h.consumer, ctx, legacy); err != nil {
		t.Fatalf("legacy appointment event: %v", err)
	}
	applied := proj.applied()
	if len(applied) != len(analyticsProjected)+1 || applied[len(applied)-1].Kind != report.KindAppointmentBooked {
		t.Fatalf("legacy kind did not project: %d events, last=%v", len(applied), applied)
	}
}

func jsonFeed(c *AnalyticsConsumer, ctx context.Context, raw []byte) error {
	return c.HandleMessage(ctx, kafkago.Message{Topic: outboundkafka.AnalyticsTopic, Value: raw})
}

// E3: every documented analytics golden carries the invariants; the
// dataschema names the INTEGRATION event (analytics twins reuse the type).
func TestAnalyticsEnvelopeInvariants(t *testing.T) {
	doc, ch, types := analyticsPinned(t)
	for typ := range types {
		t.Run(typ, func(t *testing.T) {
			assertCEEnvelope(t, pinnedGolden(t, doc, ch, typ), "inbound-receiving", typ)
		})
	}
}

// E4: the analytics consumer's own documented policy — unknown data keys and
// extensions tolerated; unknown types ignored (no DLQ); non-CloudEvents
// skipped (no DLQ); a known type whose subject is not the aggregate id is
// dead-lettered; a store ErrRejected is dead-lettered; any other store error
// is transient (returned, never dead-lettered).
func TestAnalyticsUnknownAndDLQPolicy(t *testing.T) {
	doc, ch, _ := analyticsPinned(t)
	golden := analyticsGolden(t, doc, ch, "ASNRegistered")

	cases := []struct {
		name       string
		mutate     func(map[string]any) []byte
		projErr    error
		wantDLQ    int
		wantErr    bool
		wantNoProj bool
	}{
		{
			name:   "unknown keys tolerated",
			mutate: func(env map[string]any) []byte { return goldenBytes(t, envelopeWithUnknowns(t, env)) },
		},
		{
			name: "unknown type ignored without dlq",
			mutate: func(env map[string]any) []byte {
				clone := cloneEnvelope(t, env)
				clone["type"] = cloudevents.Type(entityAsn, "NeverSeenByProjector")
				return goldenBytes(t, clone)
			},
			wantNoProj: true,
		},
		{
			name:       "non cloudevents skipped without dlq",
			mutate:     func(map[string]any) []byte { return []byte(`{"event_id":"legacy-flat"}`) },
			wantNoProj: true,
		},
		{
			name: "subject mismatch deadletters",
			mutate: func(env map[string]any) []byte {
				clone := cloneEnvelope(t, env)
				clone["subject"] = "not-the-asn"
				return goldenBytes(t, clone)
			},
			wantDLQ: 1, wantNoProj: true,
		},
		{
			name: "store rejection deadletters", projErr: report.ErrRejected, wantDLQ: 1,
			mutate: func(env map[string]any) []byte { return goldenBytes(t, env) },
		},
		{
			name: "transient store error is returned not dlqd", projErr: context.DeadlineExceeded,
			wantErr: true, wantNoProj: true,
			mutate: func(env map[string]any) []byte { return goldenBytes(t, env) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertAnalyticsPolicy(t, tc.wantDLQ, tc.wantErr, tc.wantNoProj, tc.projErr, tc.mutate(golden))
		})
	}
}

func assertAnalyticsPolicy(t *testing.T, wantDLQ int, wantErr, wantNoProj bool, projErr error, value []byte) {
	t.Helper()
	proj := &countingProjection{}
	if projErr != nil {
		proj.errs = []error{projErr}
	}
	h := newAnHarness(proj)
	err := jsonFeed(h.consumer, context.Background(), value)
	if (err != nil) != wantErr {
		t.Fatalf("HandleMessage error = %v, wantErr %v", err, wantErr)
	}
	if got := len(h.dlq.written()); got != wantDLQ {
		t.Errorf("dlq = %d, want %d", got, wantDLQ)
	}
	if wantNoProj && len(proj.applied()) != 0 {
		t.Errorf("applied = %d, want 0", len(proj.applied()))
	}
	if wantDLQ > 0 {
		assertDLQHeaders(t, h.dlq.written()[0])
	}
}

func assertDLQHeaders(t *testing.T, msg kafkago.Message) {
	t.Helper()
	if got := dlqHeader(msg, "x-dlq-source-topic"); got != outboundkafka.AnalyticsTopic {
		t.Errorf("x-dlq-source-topic = %q, want %q", got, outboundkafka.AnalyticsTopic)
	}
	if dlqHeader(msg, "x-dlq-error") == "" {
		t.Error("dead-lettered message carries no x-dlq-error header")
	}
}

func dlqHeader(msg kafkago.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
