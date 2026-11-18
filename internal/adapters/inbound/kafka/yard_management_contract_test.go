package kafka

// Pinned-contract tests for yard-management (E1-E4). yard-management's spec
// is AsyncAPI 3.x: channels are named keys with the topic in `address` and a
// channel-level messages map, so the helpers match by address. The golden is
// the spec's own VisitCheckedIn example.

import (
	"context"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

func yardPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "yard-management.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, YardManagementTopic)
	if name == YardManagementTopic {
		t.Logf("channel keyed by topic %q", name)
	} else {
		t.Logf("channel %q carries address %q (AsyncAPI 3.x named channel)", name, YardManagementTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: the pinned yard-management spec documents the integration topic with
// the one type the visit-arrival consumer routes on.
func TestPinnedYardManagementSpecCoversSubscriptions(t *testing.T) {
	_, _, types := yardPinned(t)
	if _, ok := types[TypeVisitCheckedIn]; !ok {
		t.Errorf("routed type %s is not documented on channel for %s", TypeVisitCheckedIn, YardManagementTopic)
	}
}

// E2: the golden VisitCheckedIn decodes and dispatches with the spec's own
// ids and version; the appointment is unknown to this world, so the use
// case's documented outcome is Ignored (the wrapper proves routing).
func TestYardManagementGoldenVisitDecodes(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := yardPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeVisitCheckedIn)

	w := newStowWorld(t)
	rec := &recVisitArrival{VisitCheckedInApplier: w.arrivals.Arrivals}
	w.arrivals.Arrivals = rec
	if err := w.arrivals.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !rec.called {
		t.Fatal("golden was not dispatched to the arrival applier")
	}
	for _, check := range []struct {
		name string
		got  string
		key  string
	}{
		{"visit id", rec.got.VisitID, "visit_id"},
		{"appointment id", rec.got.AppointmentID, "appointment_id"},
	} {
		if check.got != envelopeDataString(t, golden, check.key) {
			t.Errorf("%s = %q, want the golden's %q", check.name, check.got, envelopeDataString(t, golden, check.key))
		}
	}
	if rec.got.Version != 1 {
		t.Errorf("version = %d, want the golden's 1", rec.got.Version)
	}
	if err := w.arrivals.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

// E3: the golden carries the CloudEvents invariants.
func TestYardManagementEnvelopeInvariants(t *testing.T) {
	doc, ch, _ := yardPinned(t)
	assertCEEnvelope(t, pinnedGolden(t, doc, ch, TypeVisitCheckedIn), "yard-management", TypeVisitCheckedIn)
}

// E4: unknown attributes tolerated; documented-but-unrouted yard types are
// skipped and non-CloudEvents bytes never error.
func TestYardManagementUnknownTolerated(t *testing.T) {
	ctx := context.Background()
	doc, ch, types := yardPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeVisitCheckedIn)

	w := newStowWorld(t)
	if err := w.arrivals.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t, golden))); err != nil {
		t.Fatalf("unknown keys broke decode: %v", err)
	}

	skipped := 0
	for typ := range types {
		if typ == TypeVisitCheckedIn {
			continue
		}
		clone := cloneEnvelope(t, golden)
		clone["type"] = typ
		if err := w.arrivals.HandleMessage(ctx, goldenBytes(t, clone)); err != nil {
			t.Fatalf("documented-but-unrouted type %s: %v", typ, err)
		}
		skipped++
	}
	if skipped == 0 {
		t.Fatal("expected documented-but-unrouted types on the channel")
	}
	if err := w.arrivals.HandleMessage(ctx, []byte(`{"event_id":"legacy-flat"}`)); err != nil {
		t.Fatalf("non-CloudEvents must be skipped: %v", err)
	}
}

// recVisitArrival wraps the real applier and records the dispatch (the
// golden appointment is unknown here, so the use case Ignores before any
// claim; the wrapper is the routing proof).
type recVisitArrival struct {
	VisitCheckedInApplier
	got    usecases.VisitCheckedIn
	called bool
}

func (r *recVisitArrival) Handle(ctx context.Context, eventID string, e usecases.VisitCheckedIn) (usecases.Outcome, error) {
	r.called, r.got = true, e
	return r.VisitCheckedInApplier.Handle(ctx, eventID, e)
}
