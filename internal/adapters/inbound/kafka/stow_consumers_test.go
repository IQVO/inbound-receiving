package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	yardAppointment = "appt-bbbbbbbb-0000-4000-8000-000000000001"
	stowReceiptID   = "rcpt-123e4567-e89b-12d3-a456-426614174000"
)

var stowT0 = time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC)

// stowEncoder is a one-row-per-event encoder that names the event.
type stowEncoder struct{}

func (stowEncoder) EncodeAsn(...asn.Event) ([]outbox.Message, error) { return nil, nil }
func (stowEncoder) EncodeReceipt(events ...receipt.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Message{EventType: e.EventName(), Subject: string(e.ReceiptID())})
	}
	return out, nil
}

type stowClock struct{}

func (stowClock) Now() time.Time { return stowT0.Add(10 * time.Hour) }

type stowWorld struct {
	receipts  *memory.ReceiptRepo
	stows     *memory.StowStore
	processed *memory.ProcessedEventRepo
	outbox    *memory.OutboxRepo
	arrivals  *VisitArrivalConsumer
	cartons   *CartonStowConsumer
	lpn       string
}

func newStowWorld(t *testing.T) *stowWorld {
	t.Helper()
	receipts, processed, ob := memory.NewReceiptRepo(), memory.NewProcessedEventRepo(), memory.NewOutboxRepo()
	stows := memory.NewStowStore(receipts)
	w := usecases.Writer{
		Receipts: receipts, Stows: stows, Outbox: ob, Encoder: stowEncoder{}, Clock: stowClock{},
		UoW: memory.NewUnitOfWork(receipts, stows, processed, ob),
	}
	sw := &stowWorld{receipts: receipts, stows: stows, processed: processed, outbox: ob}
	sw.arrivals = &VisitArrivalConsumer{Arrivals: &usecases.ApplyVisitCheckedIn{Writer: w, ProcessedEvents: processed}, Logger: quiet()}
	sw.cartons = &CartonStowConsumer{Stows: &usecases.ApplyContainerStowed{Writer: w, ProcessedEvents: processed}, Logger: quiet()}
	sw.closedReceipt(t)
	return sw
}

// closedReceipt stores a CLOSED receipt opened from yardAppointment with one
// accepted Good carton.
func (sw *stowWorld) closedReceipt(t *testing.T) {
	t.Helper()
	sscc, err := gs1.NewSSCC("00345678901234567")
	if err != nil {
		t.Fatal(err)
	}
	g, err := gs1.NewGTIN("1000123456789")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := asn.RegisterDraft(asn.Draft{
		Number: "ASN-1", SupplierRef: "ACME", Lines: []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: []asn.CartonInput{{SSCC: sscc.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g.String(), Qty: 20}}}},
	}, stowT0)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := receipt.Open(stowReceiptID, a.Snapshot(), yardAppointment, "", stowT0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(receipt.CartonScan{SSCC: sscc, Condition: receipt.ConditionGood}, stowT0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(stowT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := sw.receipts.Save(context.Background(), r, 0); err != nil {
		t.Fatal(err)
	}
	sw.lpn = sscc.String()
}

// visitCheckedIn is a yard-management VisitCheckedIn carrying MORE than the
// copy needs (the superset of its AsyncAPI).
func visitCheckedIn(t *testing.T, id string, mutate func(map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"visit_id": "visit-5b1f0c3e", "site_id": "WH1", "carrier": "Acme Freight", "carrier_scac": "ACME", "purpose": "LIVE_UNLOAD",
		"appointment_id": yardAppointment, "arrived_at": "2026-11-09T05:00:00Z", "version": 1,
		"trailers": []map[string]any{{"trailer_id": "trl-1", "unit_number": "TR-1042", "carrier_scac": "ACME", "kind": "DRY_VAN", "load_state": "LOADED"}},
	}
	if mutate != nil {
		mutate(data)
	}
	return cloudEvent(t, id, TypeVisitCheckedIn, data)
}

// containerStowed is an inventory-storage ContainerStowed carrying MORE than
// the copy needs.
func containerStowed(t *testing.T, id, lpn, at string) []byte {
	t.Helper()
	return cloudEvent(t, id, TypeContainerStowed, map[string]any{
		"lpn": lpn, "sku": "SKU-1", "qty_eaches": 20, "owner_ref": "seller-a", "site_id": "WH1", "bin": "WH1-STOR-A01", "at": at,
	})
}

func (sw *stowWorld) completed() int {
	n := 0
	for _, m := range sw.outbox.Messages() {
		if m.EventType == "ReceiptStowCompleted" {
			n++
		}
	}
	return n
}

func TestTheStowFactsAreTheProducersAndTheirTopics(t *testing.T) {
	if TypeVisitCheckedIn != "com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn" ||
		TypeContainerStowed != "com.warehouse.wms.inventory-storage.container.ContainerStowed" ||
		YardManagementTopic != "warehouse.yard-management.events" || InventoryTopic != "warehouse.inventory.events" {
		t.Fatalf("types/topics = %s %s %s %s", TypeVisitCheckedIn, TypeContainerStowed, YardManagementTopic, InventoryTopic)
	}
}

func TestTheTwoConsumersCompleteTheFactInEitherOrder(t *testing.T) {
	for name, order := range map[string][]string{"stow then arrival": {"stow", "arrival"}, "arrival then stow": {"arrival", "stow"}} {
		t.Run(name, func(t *testing.T) {
			sw := newStowWorld(t)
			feedTwice(t, sw, order[0]) // a redelivery is a duplicate
			if sw.completed() != 0 {
				t.Fatalf("after %s alone the receipt must not complete: %+v", order[0], sw.outbox.Messages())
			}
			feedTwice(t, sw, order[1])
			if sw.completed() != 1 || !sw.processed.Has(usecases.VisitArrivalConsumer, "v1") || !sw.processed.Has(usecases.CartonStowConsumer, "s1") {
				t.Fatalf("outbox = %+v", sw.outbox.Messages())
			}
		})
	}
}

func feedTwice(t *testing.T, sw *stowWorld, which string) {
	t.Helper()
	for range 2 {
		var err error
		if which == "arrival" {
			err = sw.arrivals.HandleMessage(context.Background(), visitCheckedIn(t, "v1", nil))
		} else {
			err = sw.cartons.HandleMessage(context.Background(), containerStowed(t, "s1", sw.lpn, "2026-11-09T07:00:00Z"))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAWalkInVisitIsIgnoredAndNotClaimed(t *testing.T) {
	sw := newStowWorld(t)
	walkIn := visitCheckedIn(t, "v1", func(m map[string]any) { m["appointment_id"] = nil })
	if err := sw.arrivals.HandleMessage(context.Background(), walkIn); err != nil {
		t.Fatal(err)
	}
	if sw.processed.Has(usecases.VisitArrivalConsumer, "v1") {
		t.Fatal("a walk-in visit has no arrival fact: nothing is claimed")
	}
}

func TestStowConsumersIgnoreOtherTypesAndSkipDeterministicProblems(t *testing.T) {
	arrival := map[string][]byte{
		"not json":            []byte(`nope`),
		"flat legacy":         []byte(`{"event_id":"1","event_type":"VisitCheckedIn","payload":{}}`),
		"visit rejected":      cloudEvent(t, "e1", "com.warehouse.wms.yard-management.yardvisit.VisitRejected", map[string]any{"visit_id": "v", "appointment_id": yardAppointment}),
		"trailer departed":    cloudEvent(t, "e2", "com.warehouse.wms.yard-management.trailer.TrailerDeparted", map[string]any{"trailer_id": "t"}),
		"malformed arrival":   cloudEvent(t, "e3", TypeVisitCheckedIn, map[string]any{"arrived_at": "yesterday"}),
		"no visit":            visitCheckedIn(t, "e4", func(m map[string]any) { delete(m, "visit_id") }),
		"bad appointment":     visitCheckedIn(t, "e5", func(m map[string]any) { m["appointment_id"] = "nope" }),
		"no arrival time":     visitCheckedIn(t, "e6", func(m map[string]any) { delete(m, "arrived_at") }),
		"version zero":        visitCheckedIn(t, "e7", func(m map[string]any) { m["version"] = 0 }),
		"version not numeric": cloudEvent(t, "e8", TypeVisitCheckedIn, map[string]any{"version": "one"}),
	}
	for name, value := range arrival {
		t.Run("arrival/"+name, func(t *testing.T) {
			sw := newStowWorld(t)
			if err := sw.arrivals.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if len(sw.outbox.Messages()) != 0 {
				t.Fatal("a skipped message must not raise anything")
			}
		})
	}
	stow := map[string][]byte{
		"not json":       []byte(`nope`),
		"item stowed":    cloudEvent(t, "e1", "com.warehouse.wms.inventory-storage.stock.ItemStowed", map[string]any{"sku": "SKU-1", "bin_id": "A-1", "quantity": 5}),
		"case broken":    cloudEvent(t, "e2", "com.warehouse.wms.inventory-storage.container.CaseBroken", map[string]any{"lpn": "L"}),
		"malformed stow": cloudEvent(t, "e3", TypeContainerStowed, map[string]any{"lpn": "L", "at": "yesterday"}),
		"no lpn":         containerStowed(t, "e4", "", "2026-11-09T07:00:00Z"),
		"no stow time":   cloudEvent(t, "e5", TypeContainerStowed, map[string]any{"lpn": "L"}),
		"lpn not a text": cloudEvent(t, "e6", TypeContainerStowed, map[string]any{"lpn": 7, "at": "2026-11-09T07:00:00Z"}),
	}
	for name, value := range stow {
		t.Run("stow/"+name, func(t *testing.T) {
			sw := newStowWorld(t)
			if err := sw.cartons.HandleMessage(context.Background(), value); err != nil {
				t.Fatalf("err = %v, want nil (skip)", err)
			}
			if len(sw.outbox.Messages()) != 0 {
				t.Fatal("a skipped message must not raise anything")
			}
		})
	}
}

type failingArrival struct{ err error }

func (f failingArrival) Handle(context.Context, string, usecases.VisitCheckedIn) (usecases.Outcome, error) {
	return "", f.err
}

type failingStow struct{ err error }

func (f failingStow) Handle(context.Context, string, usecases.ContainerStowed) (usecases.Outcome, error) {
	return "", f.err
}

func TestStowConsumersReturnTransientErrorsForRetry(t *testing.T) {
	boom := errors.New("db down")
	a := &VisitArrivalConsumer{Arrivals: failingArrival{boom}, Logger: quiet()}
	if err := a.HandleMessage(context.Background(), visitCheckedIn(t, "v1", nil)); !errors.Is(err, boom) {
		t.Fatalf("arrival: %v", err)
	}
	c := &CartonStowConsumer{Stows: failingStow{boom}, Logger: quiet()}
	if err := c.HandleMessage(context.Background(), containerStowed(t, "s1", "L", "2026-11-09T07:00:00Z")); !errors.Is(err, boom) {
		t.Fatalf("stow: %v", err)
	}
}

func TestStowConsumersRunCommitAfterHandling(t *testing.T) {
	sw := newStowWorld(t)
	for name, run := range map[string]struct {
		queue []kafkago.Message
		start func(ctx context.Context, r Reader) (func() error, func() error)
	}{
		"arrival": {
			[]kafkago.Message{{Offset: 0, Value: visitCheckedIn(t, "v1", nil)}, {Offset: 1, Value: []byte("junk")}},
			func(ctx context.Context, r Reader) (func() error, func() error) {
				sw.arrivals.Reader = r
				return func() error { return sw.arrivals.Run(ctx) }, sw.arrivals.Close
			},
		},
		"stow": {
			[]kafkago.Message{{Offset: 0, Value: containerStowed(t, "s1", sw.lpn, "2026-11-09T07:00:00Z")}, {Offset: 1, Value: []byte("junk")}},
			func(ctx context.Context, r Reader) (func() error, func() error) {
				sw.cartons.Reader = r
				return func() error { return sw.cartons.Run(ctx) }, sw.cartons.Close
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{queue: run.queue}
			ctx, cancel := context.WithCancel(context.Background())
			runFn, closeFn := run.start(ctx, reader)
			done := make(chan error, 1)
			go func() { done <- runFn() }()
			waitFor(t, func() bool { return len(reader.commits()) == 2 })
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v", err)
			}
			if err := closeFn(); err != nil || !reader.closed {
				t.Fatalf("Close = %v closed=%v", err, reader.closed)
			}
		})
	}
	if sw.completed() != 1 {
		t.Fatalf("the two facts must have completed the receipt once: %+v", sw.outbox.Messages())
	}
}

func TestNewStowConsumersDoNotDialAndWaitAtMostHalfASecond(t *testing.T) {
	a := NewVisitArrivalConsumer([]string{"127.0.0.1:1"}, "group-from-env", failingArrival{}, nil)
	c := NewCartonStowConsumer([]string{"127.0.0.1:1"}, "group-from-env", failingStow{}, nil)
	for name, r := range map[string]Reader{"arrival": a.Reader, "stow": c.Reader} {
		kr, ok := r.(*kafkago.Reader)
		if !ok || kr.Config().MaxWait != 500*time.Millisecond || kr.Config().GroupID != "group-from-env" {
			t.Fatalf("%s reader config = %+v", name, kr.Config())
		}
	}
	if a.Logger == nil || c.Logger == nil {
		t.Fatal("a nil logger must default")
	}
	_, _ = a.Close(), c.Close()
}
