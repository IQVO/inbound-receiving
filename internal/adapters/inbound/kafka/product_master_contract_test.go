package kafka

// Pinned-contract tests for product-master (E1-E4): two consumers (product
// registry + packaging view) on one topic, all three routed types covered by
// the spec's own examples.

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func productPinned(t *testing.T) (map[string]any, map[string]any, map[string]string) {
	t.Helper()
	doc := loadPinnedSpec(t, "product-master.asyncapi.yaml")
	name, ch := pinnedChannelFor(t, doc, ProductTopic)
	if name != ProductTopic {
		t.Fatalf("channel key %q, want the topic constant %q", name, ProductTopic)
	}
	return doc, ch, pinnedChannelTypes(t, doc, ch)
}

// E1: the pinned product-master spec documents the topic with every type the
// two product-master consumers route on.
func TestPinnedProductMasterSpecCoversSubscriptions(t *testing.T) {
	_, _, types := productPinned(t)
	for _, typ := range []string{TypeProductRegistered, TypePackLevelDeclared, TypePackLevelRetired} {
		if _, ok := types[typ]; !ok {
			t.Errorf("routed type %s is not documented on channel %s", typ, ProductTopic)
		}
	}
}

// E2: ProductRegistered's golden feeds the known-SKU copy and claims its id;
// the pack-level goldens drive the packaging view (declare then retire, the
// golden's own version ordering 3 -> 4).
func TestProductMasterGoldenEventsDecode(t *testing.T) {
	ctx := context.Background()
	doc, ch, _ := productPinned(t)
	registeredEnv := pinnedGolden(t, doc, ch, TypeProductRegistered)

	t.Run("registered feeds known skus once", func(t *testing.T) {
		w := newWorld()
		assertRoutedAndClaimed(t, func(b []byte) error { return w.product.HandleMessage(ctx, b) },
			func(id string) bool { return w.processed.Has(usecases.ProductRegistryConsumer, id) },
			registeredEnv["id"].(string), registeredEnv)
		if !hasSku(t, w, envelopeDataString(t, registeredEnv, "sku")) {
			t.Fatal("the golden's SKU is not in the known-SKU copy")
		}
	})

	t.Run("pack levels declare and retire", func(t *testing.T) {
		w := newPackagingWorld()
		gtin := productGoldenGTIN(t, doc, ch, TypePackLevelDeclared)
		assertPackApplied(t, ctx, w, pinnedGolden(t, doc, ch, TypePackLevelDeclared))
		level, err := w.view.ByGTIN(ctx, gtin)
		if err != nil || level.Version != 3 {
			t.Fatalf("declared level = %+v (%v), want version 3", level, err)
		}
		assertPackApplied(t, ctx, w, pinnedGolden(t, doc, ch, TypePackLevelRetired))
		level, err = w.view.ByGTIN(ctx, gtin)
		if err != nil {
			t.Fatalf("retired level read: %v", err)
		}
		if !level.EffectiveTo.After(time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)) {
			t.Errorf("retired level EffectiveTo = %v, want the golden's 2026-10-06T21:01Z", level.EffectiveTo)
		}
	})
}

// E3: all three goldens carry the CloudEvents invariants.
func TestProductMasterEnvelopeInvariants(t *testing.T) {
	doc, ch, types := productPinned(t)
	for _, typ := range []string{TypeProductRegistered, TypePackLevelDeclared, TypePackLevelRetired} {
		t.Run(types[typ], func(t *testing.T) {
			assertCEEnvelope(t, pinnedGolden(t, doc, ch, typ), "product-master", typ)
		})
	}
}

// E4: unknown attributes tolerated on both consumers; a documented-but-
// unrouted product type and non-CloudEvents bytes are skipped.
func TestProductMasterUnknownTolerated(t *testing.T) {
	ctx := context.Background()
	doc, ch, types := productPinned(t)
	registeredEnv := pinnedGolden(t, doc, ch, TypeProductRegistered)

	w := newWorld()
	if err := w.product.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t, registeredEnv))); err != nil {
		t.Fatalf("unknown keys broke decode: %v", err)
	}
	if !hasSku(t, w, envelopeDataString(t, registeredEnv, "sku")) {
		t.Error("the perturbed golden was not applied")
	}

	pw := newPackagingWorld()
	if err := pw.consumer.HandleMessage(ctx, goldenBytes(t, envelopeWithUnknowns(t,
		pinnedGolden(t, doc, ch, TypePackLevelDeclared)))); err != nil {
		t.Fatalf("unknown keys broke the packaging decode: %v", err)
	}

	clone := cloneEnvelope(t, registeredEnv)
	clone["type"] = "com.warehouse.wms.product-master.product.ProductClassified" // documented, unrouted
	for typ := range types {
		if typ != TypeProductRegistered && typ != TypePackLevelDeclared && typ != TypePackLevelRetired {
			clone["type"] = typ
			break
		}
	}
	if err := w.product.HandleMessage(ctx, goldenBytes(t, clone)); err != nil {
		t.Fatalf("documented-but-unrouted type: %v", err)
	}
	if err := w.product.HandleMessage(ctx, []byte(`{"event_id":"legacy-flat"}`)); err != nil {
		t.Fatalf("non-CloudEvents must be skipped: %v", err)
	}
}

func assertPackApplied(t *testing.T, ctx context.Context, w *packagingWorld, env map[string]any) {
	t.Helper()
	if err := w.consumer.HandleMessage(ctx, goldenBytes(t, env)); err != nil {
		t.Fatalf("HandleMessage(%s): %v", env["type"], err)
	}
	if !w.processed.Has(usecases.PackagingViewConsumer, env["id"].(string)) {
		t.Fatalf("%s id not claimed", env["type"])
	}
}

func productGoldenGTIN(t *testing.T, doc, ch map[string]any, typ string) gs1.GTIN {
	t.Helper()
	data, _ := pinnedGolden(t, doc, ch, typ)["data"].(map[string]any)
	gtin, err := gs1.ParseGTIN(data["gtin"].(string))
	if err != nil {
		t.Fatalf("golden gtin: %v", err)
	}
	return gtin
}
