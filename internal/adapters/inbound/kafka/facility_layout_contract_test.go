package kafka

// Pinned-contract tests for facility-layout (E1-E4). LocationSlotRegistered
// has a spec example (the golden); LocationSlotDecommissioned has NONE, so
// its E2 fixture is DERIVED from the payload schema's required keys and the
// type/dataschema consts (derived; producer example backfill pending).

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// recDoorRegistrar wraps the real registrar and records the dispatch (the
// golden slot is a Yard spot, which the use case legitimately Ignores before
// claiming, so the claim alone cannot prove routing).
type recDoorRegistrar struct {
	DoorRegistrar
	gotID  string
	got    usecases.LocationSlot
	called bool
}

func (r *recDoorRegistrar) Handle(ctx context.Context, eventID string, slot usecases.LocationSlot) (usecases.Outcome, error) {
	r.called, r.gotID, r.got = true, eventID, slot
	return r.DoorRegistrar.Handle(ctx, eventID, slot)
}

type recStringEvent struct {
	StringEventUseCase
	gotID, got string
	called     bool
}

func (r *recStringEvent) Handle(ctx context.Context, eventID, s string) (usecases.Outcome, error) {
	r.called, r.gotID, r.got = true, eventID, s
	return r.StringEventUseCase.Handle(ctx, eventID, s)
}

func facilityPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "facility-layout.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, FacilityTopic)
	if name != FacilityTopic {
		t.Fatalf("channel key %q, want the topic constant %q", name, FacilityTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: the pinned facility-layout spec documents the facility topic with both
// types the dock-door consumer routes on.
func TestPinnedFacilityLayoutSpecCoversSubscriptions(t *testing.T) {
	_, _, types := facilityPinned(t)
	for _, typ := range []string{TypeLocationSlotRegistered, TypeLocationSlotDecommissioned} {
		if _, ok := types[typ]; !ok {
			t.Errorf("routed type %s is not documented on channel %s", typ, FacilityTopic)
		}
	}
}

// E2: the golden LocationSlotRegistered (a Yard spot) decodes, dispatches to
// the registrar with the spec's own field values, and is Ignored by design
// (only role Dock / flow Inbound|Both feeds the dock-door copy).
func TestFacilityLayoutGoldenSlotDecodes(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := facilityPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeLocationSlotRegistered)

	w := newWorld()
	reg := &recDoorRegistrar{DoorRegistrar: w.dock.Registered}
	w.dock.Registered = reg
	if err := w.dock.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !reg.called {
		t.Fatal("golden was not dispatched to the registrar")
	}
	if reg.got.LocationCode != envelopeDataString(t, golden, "locationCode") ||
		reg.got.Role != envelopeDataString(t, golden, "role") {
		t.Errorf("dispatched slot = %+v, want the golden's locationCode/role", reg.got)
	}
	if hasDoor(t, w, envelopeDataString(t, golden, "locationCode")) {
		t.Error("a non-dock slot must not enter the dock-door copy")
	}

	// Replay with the same id: deterministic nil, still exactly one door set.
	if err := w.dock.HandleMessage(ctx, goldenBytes(t, golden)); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

// E2 (derived fixture): LocationSlotDecommissioned has no spec example; the
// envelope is derived from the schema consts and required data keys and must
// dispatch to the decommission use case with the payload's location code.
func TestFacilityLayoutDerivedDecommissionDecodes(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := facilityPinned(t)
	env := derivedDecommission(t, doc, ch)

	w := newWorld()
	dec := &recStringEvent{StringEventUseCase: w.dock.Decommissions}
	w.dock.Decommissions = dec
	if err := w.dock.HandleMessage(ctx, goldenBytes(t, env)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !dec.called || dec.got != envelopeDataString(t, env, "locationCode") {
		t.Fatalf("derived decommission not dispatched: called=%v code=%q", dec.called, dec.got)
	}
	if !w.processed.Has(usecases.DockDoorRegistryConsumer, env["id"].(string)) {
		t.Error("the decommission's CloudEvents id was not claimed")
	}
}

// E3: the golden and the derived envelope carry the CloudEvents invariants,
// including facility-layout's own dataschema consts.
func TestFacilityLayoutEnvelopeInvariants(t *testing.T) {
	doc, ch, _ := facilityPinned(t)
	assertCEEnvelope(t, pinnedGolden(t, doc, ch, TypeLocationSlotRegistered), "facility-layout", TypeLocationSlotRegistered)
	assertCEEnvelope(t, derivedDecommission(t, doc, ch), "facility-layout", TypeLocationSlotDecommissioned)
}

// E4: unknown attributes tolerated; a documented-but-unrouted facility type
// and non-CloudEvents bytes are skipped.
func TestFacilityLayoutUnknownTolerated(t *testing.T) {
	ctx := context.Background()
	doc, ch, types := facilityPinned(t)
	golden := pinnedGolden(t, doc, ch, TypeLocationSlotRegistered)

	w := newWorld()
	reg := &recDoorRegistrar{DoorRegistrar: w.dock.Registered}
	w.dock.Registered = reg
	if err := w.dock.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t, golden))); err != nil {
		t.Fatalf("unknown keys broke decode: %v", err)
	}
	if !reg.called {
		t.Error("the perturbed golden was not dispatched")
	}

	for typ := range types { // any documented type this consumer does not route on
		if typ != TypeLocationSlotRegistered && typ != TypeLocationSlotDecommissioned {
			clone := cloneEnvelope(t, golden)
			clone["type"] = typ
			if err := w.dock.HandleMessage(ctx, goldenBytes(t, clone)); err != nil {
				t.Fatalf("documented-but-unrouted type %s: %v", typ, err)
			}
			break
		}
	}
	if err := w.dock.HandleMessage(ctx, []byte(`{"event_id":"legacy-flat"}`)); err != nil {
		t.Fatalf("non-CloudEvents must be skipped: %v", err)
	}
}

// derivedDecommission builds the derived fixture: envelope consts read from
// the pinned schema (never invented), data from the schema's required keys
// (eventDataBase + locationCode), a fresh id.
func derivedDecommission(t *testing.T, doc, ch map[string]any) map[string]any {
	t.Helper()
	schemas := componentMap(t, doc, "schemas")
	msg := pinnedChannelMessages(t, doc, ch)["locationSlotDecommissioned"].(map[string]any)
	typ := constIn(t, msg["payload"], schemas, "com.warehouse.")
	if typ != TypeLocationSlotDecommissioned {
		t.Fatalf("schema const type = %q, want the routed constant", typ)
	}
	return map[string]any{
		"specversion": "1.0", "id": "derived-facility-decommission-1",
		"source": "/warehouse/facility-layout", "type": typ,
		"subject": "WH1-DOCK-IN-09", "time": "2026-11-05T10:00:00Z",
		"datacontenttype": "application/json",
		"dataschema":      "urn:warehouse:facility-layout:events:LocationSlotDecommissioned:v1",
		"data": map[string]any{
			"eventName": "LocationSlotDecommissioned", "eventType": typ,
			"occurredAt": time.Now().UTC().Format(time.RFC3339), "locationCode": "WH1-DOCK-IN-09",
		},
	}
}
