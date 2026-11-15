package kafka

// Pinned-contract tests for inventory-storage (E1-E4). The consumer routes
// on exactly one of the nineteen documented types; the golden is
// inventory-storage's own ContainerStowed example, driven through the real
// consumer and use case.

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

func inventoryPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "inventory-storage.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, InventoryTopic)
	if name != InventoryTopic {
		t.Fatalf("channel key %q, want the topic constant %q", name, InventoryTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: the pinned inventory-storage spec documents the inventory topic with
// the one type the carton-stow consumer routes on.
func TestPinnedInventoryStorageSpecCoversSubscriptions(t *testing.T) {
	_, _, types := inventoryPinned(t)
	if _, ok := types[TypeContainerStowed]; !ok {
		t.Errorf("routed type %s is not documented on channel %s", TypeContainerStowed, InventoryTopic)
	}
}

// E2: the golden ContainerStowed decodes and dispatches with the spec's own
// lpn and stow time; the container is unknown to this world's receipts, so
// the use case's documented outcome is Ignored (the wrapper proves routing).
func TestInventoryStorageGoldenStowDecodes(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := inventoryPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeContainerStowed)

	w := newStowWorld(t)
	rec := &recContainerStow{ContainerStowedApplier: w.cartons.Stows}
	w.cartons.Stows = rec
	if err := w.cartons.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !rec.called {
		t.Fatal("golden was not dispatched to the stow applier")
	}
	if rec.got.LPN != envelopeDataString(t, golden, "lpn") {
		t.Errorf("lpn = %q, want the golden's %q", rec.got.LPN, envelopeDataString(t, golden, "lpn"))
	}
	wantAt, _ := time.Parse(time.RFC3339, envelopeDataString(t, golden, "at"))
	if !rec.got.StowedAt.Equal(wantAt) {
		t.Errorf("stowedAt = %v, want the golden's %v", rec.got.StowedAt, wantAt)
	}
	if err := w.cartons.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rec.calls != 2 { // dedupe is the store's claim: the consumer itself stays at-least-once
		t.Fatalf("replay dispatch count = %d, want 2 (at-least-once redelivery reaches the use case)", rec.calls)
	}
}

// E3: the golden carries the CloudEvents invariants.
func TestInventoryStorageEnvelopeInvariants(t *testing.T) {
	doc, ch, _ := inventoryPinned(t)
	assertCEEnvelope(t, pinnedGolden(t, doc, ch, TypeContainerStowed), "inventory-storage", TypeContainerStowed)
}

// E4: unknown attributes tolerated; the eighteen documented-but-unrouted
// types are skipped and non-CloudEvents bytes never error.
func TestInventoryStorageUnknownTolerated(t *testing.T) {
	ctx := context.Background()
	doc, ch, types := inventoryPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeContainerStowed)

	w := newStowWorld(t)
	if err := w.cartons.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t, golden))); err != nil {
		t.Fatalf("unknown keys broke decode: %v", err)
	}

	skipped := 0
	for typ := range types {
		if typ == TypeContainerStowed {
			continue
		}
		clone := cloneEnvelope(t, golden)
		clone["type"] = typ
		if err := w.cartons.HandleMessage(ctx, goldenBytes(t, clone)); err != nil {
			t.Fatalf("documented-but-unrouted type %s: %v", typ, err)
		}
		skipped++
	}
	if skipped == 0 {
		t.Fatal("expected documented-but-unrouted types on the channel")
	}
	if err := w.cartons.HandleMessage(ctx, []byte(`not json`)); err != nil {
		t.Fatalf("non-CloudEvents must be skipped: %v", err)
	}
}

// recContainerStow wraps the real applier and records the dispatch (the
// golden container is unknown here, so the use case Ignores before any
// claim; the wrapper is the routing proof).
type recContainerStow struct {
	ContainerStowedApplier
	got    usecases.ContainerStowed
	called bool
	calls  int
}

func (r *recContainerStow) Handle(ctx context.Context, eventID string, e usecases.ContainerStowed) (usecases.Outcome, error) {
	r.called, r.got, r.calls = true, e, r.calls+1
	return r.ContainerStowedApplier.Handle(ctx, eventID, e)
}
