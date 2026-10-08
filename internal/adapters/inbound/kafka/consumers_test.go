package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeReader hands out queued messages, then blocks until ctx is done.
type fakeReader struct {
	mu        sync.Mutex
	queue     []kafkago.Message
	committed []int64
	commitErr []error
	fetchErr  error
	closed    bool
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	if r.fetchErr != nil {
		r.mu.Unlock()
		return kafkago.Message{}, r.fetchErr
	}
	if len(r.queue) > 0 {
		m := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.commitErr) > 0 {
		err := r.commitErr[0]
		r.commitErr = r.commitErr[1:]
		return err
	}
	for _, m := range msgs {
		r.committed = append(r.committed, m.Offset)
	}
	return nil
}

func (r *fakeReader) Close() error { r.closed = true; return nil }

func (r *fakeReader) commits() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.committed...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func cloudEvent(t *testing.T, id, typ string, data any) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource("/warehouse/test")
	e.SetType(typ)
	e.SetSubject("x")
	e.SetTime(time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC))
	e.SetDataSchema("urn:warehouse:test:events:X:v1")
	if err := e.SetData("application/json", data); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// world is the real use cases over the memory adapters.
type world struct {
	skus      *memory.KnownSkus
	doors     *memory.DockDoors
	processed *memory.ProcessedEventRepo
	product   *ProductConsumer
	dock      *DockDoorConsumer
}

func newWorld() *world {
	skus, doors, processed := memory.NewKnownSkus(), memory.NewDockDoors(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(skus, doors, processed)
	return &world{
		skus: skus, doors: doors, processed: processed,
		product: &ProductConsumer{Apply: &usecases.ApplyProductRegistered{UoW: uow, ProcessedEvents: processed, Skus: skus}, Logger: quiet()},
		dock: &DockDoorConsumer{
			Registered:    &usecases.ApplyLocationSlotRegistered{UoW: uow, ProcessedEvents: processed, Doors: doors},
			Decommissions: &usecases.ApplyLocationSlotDecommissioned{UoW: uow, ProcessedEvents: processed, Doors: doors},
			Logger:        quiet(),
		},
	}
}

func hasSku(t *testing.T, w *world, sku string) bool {
	t.Helper()
	ok, err := w.skus.Exists(context.Background(), sku)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func hasDoor(t *testing.T, w *world, code string) bool {
	t.Helper()
	ok, err := w.doors.Exists(context.Background(), appointment.DoorCode(code))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func registered(t *testing.T, id, sku string) []byte {
	return cloudEvent(t, id, TypeProductRegistered, map[string]any{"sku": sku, "description": "d", "version": 1})
}

func slot(t *testing.T, id, code, role, flow string) []byte {
	data := map[string]any{"eventName": "LocationSlotRegistered", "locationCode": code, "locationType": "DockDoor", "maxWeightKg": 0}
	if role != "" {
		data["role"] = role
	}
	if flow != "" {
		data["dockFlow"] = flow
	}
	return cloudEvent(t, id, TypeLocationSlotRegistered, data)
}

func decommissioned(t *testing.T, id, code string) []byte {
	return cloudEvent(t, id, TypeLocationSlotDecommissioned, map[string]any{"locationCode": code})
}

func TestProductRegisteredFeedsKnownSkusOnceAndIsIdempotent(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	for range 2 {
		if err := w.product.HandleMessage(ctx, registered(t, "ev-1", "SKU-1")); err != nil {
			t.Fatal(err)
		}
	}
	if !hasSku(t, w, "SKU-1") || !w.processed.Has(usecases.ProductRegistryConsumer, "ev-1") {
		t.Fatal("the SKU must be known and the CloudEvents id claimed")
	}
}

func TestProductConsumerSkipsDeterministicProblems(t *testing.T) {
	cases := map[string][]byte{
		"flat legacy envelope": []byte(`{"event_id":"1","event_type":"ProductRegistered","payload":{"sku":"SKU-1"}}`),
		"not json":             []byte(`not json`),
		"other type":           cloudEvent(t, "ev-2", "com.warehouse.wms.product-master.product.ProductClassified", map[string]any{"sku": "SKU-1"}),
		"short type name":      cloudEvent(t, "ev-3", "ProductRegistered", map[string]any{"sku": "SKU-1"}),
		"malformed data":       cloudEvent(t, "ev-4", TypeProductRegistered, map[string]any{"sku": 7}),
		"invalid sku":          registered(t, "ev-5", "bad sku"),
		"blank sku":            registered(t, "ev-6", ""),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			if err := w.product.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if hasSku(t, w, "SKU-1") || hasSku(t, w, "bad sku") {
				t.Fatal("a skipped message must not change the local copy")
			}
		})
	}
}

func TestDockDoorRegisteredKeepsOnlyInboundAndBothDocks(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	for i, tc := range []struct {
		code, role, flow string
		kept             bool
	}{
		{"D-IN", "Dock", "Inbound", true},
		{"D-BOTH", "Dock", "Both", true},
		{"D-OUT", "Dock", "Outbound", false},
		{"S-1", "", "", false},
		{"S-2", "Storage", "", false},
		{"Y-1", "Yard", "", false},
	} {
		if err := w.dock.HandleMessage(ctx, slot(t, "ev-"+string(rune('a'+i)), tc.code, tc.role, tc.flow)); err != nil {
			t.Fatal(err)
		}
		if got := hasDoor(t, w, tc.code); got != tc.kept {
			t.Fatalf("%s kept = %v, want %v", tc.code, got, tc.kept)
		}
	}
	doors, _ := w.doors.List(ctx)
	if len(doors) != 2 || doors[0].Flow != repository.DockFlowBoth && doors[0].Flow != repository.DockFlowInbound {
		t.Fatalf("doors = %+v", doors)
	}
}

func TestDockDoorDecommissionedRemovesTheDoorAndIsNoopForOthers(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	if err := w.dock.HandleMessage(ctx, slot(t, "ev-1", "D-IN", "Dock", "Inbound")); err != nil {
		t.Fatal(err)
	}
	if err := w.dock.HandleMessage(ctx, decommissioned(t, "ev-2", "S-404")); err != nil {
		t.Fatal(err)
	}
	if !hasDoor(t, w, "D-IN") {
		t.Fatal("another slot's decommission must not remove the door")
	}
	for range 2 {
		if err := w.dock.HandleMessage(ctx, decommissioned(t, "ev-3", "D-IN")); err != nil {
			t.Fatal(err)
		}
	}
	if hasDoor(t, w, "D-IN") || !w.processed.Has(usecases.DockDoorRegistryConsumer, "ev-3") {
		t.Fatal("the door must be gone and the id claimed")
	}
}

func TestDockDoorConsumerSkipsDeterministicProblems(t *testing.T) {
	cases := map[string][]byte{
		"not json":       []byte(`{`),
		"other type":     cloudEvent(t, "ev-1", "com.warehouse.wms.facility-layout.locationslot.LocationSlotMoved", map[string]any{"locationCode": "D"}),
		"malformed data": cloudEvent(t, "ev-2", TypeLocationSlotRegistered, map[string]any{"locationCode": 7}),
		"invalid code":   slot(t, "ev-3", "bad code", "Dock", "Inbound"),
		"invalid decom":  decommissioned(t, "ev-4", "bad code"),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			if err := w.dock.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if doors, _ := w.doors.List(context.Background()); len(doors) != 0 {
				t.Fatalf("doors = %+v", doors)
			}
		})
	}
}

type failingApplier struct {
	mu       sync.Mutex
	calls    []string
	failures int
	err      error
}

func (f *failingApplier) Handle(_ context.Context, id, _ string) (usecases.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.err != nil {
		return "", f.err
	}
	if f.failures > 0 {
		f.failures--
		return "", errors.New("db down")
	}
	return usecases.Applied, nil
}

func TestTransientErrorIsReturnedAndRolledBackNotSkipped(t *testing.T) {
	applier := &failingApplier{err: errors.New("db down")}
	c := &ProductConsumer{Apply: applier, Logger: quiet()}
	if err := c.HandleMessage(context.Background(), registered(t, "ev-1", "SKU-1")); err == nil {
		t.Fatal("a transient failure must be returned so the loop retries")
	}
}

func TestRunRetriesTheSameMessageThenCommitsInOrder(t *testing.T) {
	reader := &fakeReader{queue: []kafkago.Message{
		{Offset: 10, Value: []byte(`{"event_id":"flat"}`)},
		{Offset: 11, Value: registered(t, "ev-1", "SKU-1")},
	}}
	applier := &failingApplier{failures: 2}
	var mu sync.Mutex
	var slept []time.Duration
	c := &ProductConsumer{Reader: reader, Apply: applier, Logger: quiet(), sleep: func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	waitFor(t, func() bool { return len(reader.commits()) == 2 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := reader.commits(); got[0] != 10 || got[1] != 11 {
		t.Fatalf("commits = %v, want [10 11]", got)
	}
	if len(applier.calls) != 3 {
		t.Fatalf("the same message must be retried until it succeeds: %v", applier.calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 2 || slept[0] != DefaultRetryInitial || slept[1] != 2*DefaultRetryInitial {
		t.Fatalf("backoff = %v", slept)
	}
}

func TestRunRetriesAFailedCommit(t *testing.T) {
	reader := &fakeReader{
		queue:     []kafkago.Message{{Offset: 3, Value: []byte(`x`)}},
		commitErr: []error{errors.New("coordinator moved")},
	}
	c := &ProductConsumer{Reader: reader, Apply: &failingApplier{}, Logger: quiet(), sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	waitFor(t, func() bool { return len(reader.commits()) == 1 })
	cancel()
	<-done
}

func TestRunStopsOnFetchErrorAndOnCancelDuringBackoff(t *testing.T) {
	boom := errors.New("broker gone")
	if err := (&ProductConsumer{Reader: &fakeReader{fetchErr: boom}, Apply: &failingApplier{}}).Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run = %v", err)
	}
	reader := &fakeReader{queue: []kafkago.Message{{Offset: 1, Value: registered(t, "ev-1", "SKU-1")}}}
	ctx, cancel := context.WithCancel(context.Background())
	c := &ProductConsumer{Reader: reader, Apply: &failingApplier{err: errors.New("db down")}, Logger: quiet(),
		sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled }}
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(reader.commits()) != 0 {
		t.Fatal("a message that never succeeded must never be committed")
	}
	if err := c.Close(); err != nil || !reader.closed {
		t.Fatal("Close must close the reader")
	}
}

func TestDockDoorRunCommitsAfterHandling(t *testing.T) {
	w := newWorld()
	reader := &fakeReader{queue: []kafkago.Message{{Offset: 7, Value: slot(t, "ev-1", "D-IN", "Dock", "Inbound")}}}
	w.dock.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.dock.Run(ctx) }()
	waitFor(t, func() bool { return len(reader.commits()) == 1 })
	cancel()
	<-done
	if !hasDoor(t, w, "D-IN") {
		t.Fatal("the door must be stored before the offset is committed")
	}
	if err := w.dock.Close(); err != nil || !reader.closed {
		t.Fatal("Close must close the reader")
	}
}

func TestRetryPolicyAndSleep(t *testing.T) {
	p := RetryPolicy{}.withDefaults()
	if p.Initial != DefaultRetryInitial || p.Max != DefaultRetryMax {
		t.Fatalf("defaults = %+v", p)
	}
	if got := p.next(4 * time.Second); got != DefaultRetryMax {
		t.Fatalf("next(4s) = %v, want the cap", got)
	}
	if got := (RetryPolicy{Initial: time.Millisecond, Max: time.Hour}).withDefaults().next(time.Second); got != 2*time.Second {
		t.Fatalf("next(1s) = %v", got)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep on a cancelled ctx = %v", err)
	}
}

func TestConstructorsUseTheGivenGroupAndTopic(t *testing.T) {
	p := NewProductConsumer([]string{"127.0.0.1:1"}, "product-group", &failingApplier{}, nil)
	d := NewDockDoorConsumer([]string{"127.0.0.1:1"}, "dock-group", nil, nil, nil)
	for _, tc := range []struct {
		name, topic, group string
		reader             Reader
	}{{"product", ProductTopic, "product-group", p.Reader}, {"dock", FacilityTopic, "dock-group", d.Reader}} {
		r, ok := tc.reader.(*kafkago.Reader)
		if !ok {
			t.Fatalf("%s reader = %T", tc.name, tc.reader)
		}
		cfg := r.Config()
		if cfg.Topic != tc.topic || cfg.GroupID != tc.group || cfg.CommitInterval != 0 {
			t.Fatalf("%s config = topic %q group %q commitInterval %v", tc.name, cfg.Topic, cfg.GroupID, cfg.CommitInterval)
		}
	}
	_ = p.Close()
	_ = d.Close()
}
