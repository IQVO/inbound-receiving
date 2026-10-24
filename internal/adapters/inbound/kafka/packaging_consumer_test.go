package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	caseGTIN = "20001234567851"
	eachGTIN = "10001234567892"
)

type packagingWorld struct {
	view      *memory.PackagingView
	processed *memory.ProcessedEventRepo
	consumer  *PackagingConsumer
}

func newPackagingWorld() *packagingWorld {
	view, processed := memory.NewPackagingView(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(view, processed)
	return &packagingWorld{
		view: view, processed: processed,
		consumer: &PackagingConsumer{
			Declared: &usecases.ApplyPackLevelDeclared{UoW: uow, ProcessedEvents: processed, Packaging: view},
			Retired:  &usecases.ApplyPackLevelRetired{UoW: uow, ProcessedEvents: processed, Packaging: view},
			Logger:   quiet(),
		},
	}
}

func declaredEvent(t *testing.T, id string, version int) []byte {
	return cloudEvent(t, id, TypePackLevelDeclared, map[string]any{
		"sku": "SKU-1", "level": "CASE", "gtin": caseGTIN, "child_gtin": eachGTIN, "qty_of_child": 12, "units_per_pack": 12,
		"is_orderable": true, "is_despatch_unit": true, "effective_from": "2026-10-01T00:00:00Z", "version": version,
	})
}

func retiredEvent(t *testing.T, id string, version int) []byte {
	return cloudEvent(t, id, TypePackLevelRetired, map[string]any{
		"sku": "SKU-1", "level": "CASE", "gtin": caseGTIN, "effective_to": "2026-10-20T00:00:00Z", "reason": "superseded", "version": version,
	})
}

func (w *packagingWorld) level(t *testing.T) (repository.PackLevel, error) {
	t.Helper()
	g, err := gs1.ParseGTIN(caseGTIN)
	if err != nil {
		t.Fatal(err)
	}
	return w.view.ByGTIN(context.Background(), g)
}

func TestPackagingConsumerFeedsAndRetiresTheView(t *testing.T) {
	w, ctx := newPackagingWorld(), context.Background()
	for range 2 {
		if err := w.consumer.HandleMessage(ctx, declaredEvent(t, "ev-1", 3)); err != nil {
			t.Fatal(err)
		}
	}
	l, err := w.level(t)
	if err != nil || l.SKU != "SKU-1" || l.Level != shared.PackCase || l.UnitsPerPack != 12 || l.ChildGTIN.String() != eachGTIN ||
		!l.EffectiveFrom.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !w.processed.Has(usecases.PackagingViewConsumer, "ev-1") {
		t.Fatalf("level = %+v, %v", l, err)
	}
	if err := w.consumer.HandleMessage(ctx, retiredEvent(t, "ev-2", 4)); err != nil {
		t.Fatal(err)
	}
	if l, _ = w.level(t); !l.EffectiveTo.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) || l.Version != 4 {
		t.Fatalf("retired level = %+v", l)
	}
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	if _, err := w.view.ActiveLevel(ctx, "SKU-1", shared.PackCase, at); !errors.Is(err, repository.ErrPackLevelNotFound) {
		t.Fatalf("a retired level is no longer the active one: %v", err)
	}
}

func TestPackagingConsumerSkipsDeterministicProblems(t *testing.T) {
	cases := map[string][]byte{
		"flat legacy envelope": []byte(`{"event_id":"1","event_type":"ProductPackLevelDeclared","payload":{}}`),
		"not json":             []byte(`nope`),
		"product registered":   cloudEvent(t, "ev-2", TypeProductRegistered, map[string]any{"sku": "SKU-1"}),
		"measured":             cloudEvent(t, "ev-3", "com.warehouse.wms.product-master.product.ProductPackLevelMeasured", map[string]any{"gtin": caseGTIN}),
		"malformed declared":   cloudEvent(t, "ev-4", TypePackLevelDeclared, map[string]any{"units_per_pack": "twelve"}),
		"bad effective_from":   cloudEvent(t, "ev-5", TypePackLevelDeclared, map[string]any{"sku": "SKU-1", "level": "CASE", "effective_from": "yesterday"}),
		"invalid declared":     cloudEvent(t, "ev-6", TypePackLevelDeclared, map[string]any{"sku": "SKU-1", "level": "BOX", "gtin": caseGTIN, "units_per_pack": 12, "effective_from": "2026-10-01T00:00:00Z", "version": 1}),
		"malformed retired":    cloudEvent(t, "ev-7", TypePackLevelRetired, map[string]any{"effective_to": 5}),
		"invalid retired":      cloudEvent(t, "ev-8", TypePackLevelRetired, map[string]any{"gtin": "123", "effective_to": "2026-10-20T00:00:00Z", "version": 1}),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			w := newPackagingWorld()
			if err := w.consumer.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if _, err := w.level(t); !errors.Is(err, repository.ErrPackLevelNotFound) {
				t.Fatal("a skipped message must not change the view")
			}
		})
	}
}

type failingDeclared struct{ err error }

func (f failingDeclared) Handle(context.Context, string, usecases.PackLevelDeclared) (usecases.Outcome, error) {
	return "", f.err
}

type failingRetired struct{ err error }

func (f failingRetired) Handle(context.Context, string, string, time.Time, int64) (usecases.Outcome, error) {
	return "", f.err
}

func TestPackagingConsumerReturnsTransientErrorsForRetry(t *testing.T) {
	boom := errors.New("db down")
	c := &PackagingConsumer{Declared: failingDeclared{boom}, Retired: failingRetired{boom}, Logger: quiet()}
	if err := c.HandleMessage(context.Background(), declaredEvent(t, "ev-1", 1)); !errors.Is(err, boom) {
		t.Fatalf("declared: %v", err)
	}
	if err := c.HandleMessage(context.Background(), retiredEvent(t, "ev-2", 1)); !errors.Is(err, boom) {
		t.Fatalf("retired: %v", err)
	}
}

func TestPackagingConsumerRunCommitsAfterHandling(t *testing.T) {
	w := newPackagingWorld()
	reader := &fakeReader{queue: []kafkago.Message{
		{Offset: 0, Value: declaredEvent(t, "ev-1", 1)}, {Offset: 1, Value: []byte("junk")}, {Offset: 2, Value: retiredEvent(t, "ev-2", 2)},
	}}
	w.consumer.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.consumer.Run(ctx) }()
	waitFor(t, func() bool { return len(reader.commits()) == 3 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if err := w.consumer.Close(); err != nil || !reader.closed {
		t.Fatalf("Close = %v closed=%v", err, reader.closed)
	}
}

func TestNewPackagingConsumerDoesNotDial(t *testing.T) {
	c := NewPackagingConsumer([]string{"127.0.0.1:1"}, "group-from-env", nil, nil, nil)
	if c.Reader == nil || c.Logger == nil {
		t.Fatalf("consumer = %+v", c)
	}
	_ = c.Close()
}
