package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const (
	anTypePre = "com.warehouse.wms.inbound-receiving."
	anAppt    = "appt-123e4567-e89b-12d3-a456-426614174000"
	anAppt2   = "appt-223e4567-e89b-12d3-a456-426614174000"
	anRcpt    = "rcpt-1"
)

var anAt = time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)

// anEncode encodes ev exactly as the OLTP service writes it to the analytics
// topic (the real AnalyticsEncoder), under the given id.
func anEncode(t *testing.T, id string, encode func(e *outboundkafka.AnalyticsEncoder) ([]outbox.Message, error)) []byte {
	t.Helper()
	msgs, err := encode(&outboundkafka.AnalyticsEncoder{NewID: func() string { return id }})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("encode: %d msgs, %v", len(msgs), err)
	}
	return msgs[0].Value
}

func anRcptHeader(at time.Time) receipt.Header {
	return receipt.Header{ID: anRcpt, Asn: "ASN-1", At: at}
}

func anApptHeader(at time.Time) appointment.Header { return appointment.Header{ID: anAppt, At: at} }

func anRegistered(t *testing.T, id string, arrival time.Time) []byte {
	t.Helper()
	_, events, err := asn.Register("ASN-1", "ACME", arrival, []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 40}}, anAt)
	if err != nil {
		t.Fatal(err)
	}
	return anEncode(t, id, func(e *outboundkafka.AnalyticsEncoder) ([]outbox.Message, error) { return e.EncodeAsn(events[0]) })
}

func anRcptEvent(t *testing.T, id string, ev receipt.Event) []byte {
	t.Helper()
	return anEncode(t, id, func(e *outboundkafka.AnalyticsEncoder) ([]outbox.Message, error) { return e.EncodeReceipt(ev) })
}

func anApptEvent(t *testing.T, id string, ev appointment.Event) []byte {
	t.Helper()
	return anEncode(t, id, func(e *outboundkafka.AnalyticsEncoder) ([]outbox.Message, error) { return e.EncodeAppointment(ev) })
}

// anWire builds a hand-made CloudEvent (payload variants the real encoder
// never produces).
func anWire(t *testing.T, id, typ, subject string, at time.Time, data any) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource("/warehouse/inbound-receiving")
	e.SetType(typ)
	e.SetSubject(subject)
	if !at.IsZero() {
		e.SetTime(at)
	}
	if err := e.SetData("application/json", data); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// lockedBuffer is a goroutine-safe log sink.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) lines(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.b.String(), "\n") {
		if line != "" && strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type fakeDLQ struct {
	mu     sync.Mutex
	msgs   []kafkago.Message
	fail   int // fail the next n writes
	closed bool
}

func (f *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return errors.New("dlq broker down")
	}
	f.msgs = append(f.msgs, msgs...)
	return nil
}

func (f *fakeDLQ) Close() error { f.closed = true; return nil }

func (f *fakeDLQ) written() []kafkago.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafkago.Message(nil), f.msgs...)
}

// recordingProjection records every event and can fail on demand.
type recordingProjection struct {
	mu     sync.Mutex
	events []report.Event
	errs   []error // returned in order, one per call, then nil
	calls  int
}

func (r *recordingProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if err != nil {
			return false, err
		}
	}
	r.events = append(r.events, e)
	return true, nil
}

func (r *recordingProjection) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type anHarness struct {
	consumer *AnalyticsConsumer
	dlq      *fakeDLQ
	logs     *lockedBuffer
	clockMu  sync.Mutex
	clock    time.Time
}

func newAnHarness(p report.Projection) *anHarness {
	h := &anHarness{dlq: &fakeDLQ{}, logs: &lockedBuffer{}, clock: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	h.consumer = &AnalyticsConsumer{
		Projection: p, DLQ: h.dlq, DLQTopic: "warehouse.inbound-receiving.analytics.dlq",
		Logger: slog.New(slog.NewJSONHandler(h.logs, nil)),
		Retry:  RetryPolicy{Initial: time.Millisecond, Max: 2 * time.Millisecond},
		Now:    h.now,
	}
	return h
}

func (h *anHarness) now() time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	return h.clock
}

func (h *anHarness) advance(d time.Duration) {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	h.clock = h.clock.Add(d)
}

// run feeds values (offsets 0..n-1) through the REAL Run loop and returns
// once every one was committed.
func (h *anHarness) run(t *testing.T, values ...[]byte) *fakeReader {
	t.Helper()
	reader := &fakeReader{}
	for i, v := range values {
		reader.queue = append(reader.queue, kafkago.Message{Topic: "warehouse.inbound-receiving.analytics", Partition: 2, Offset: int64(i), Key: []byte("ASN-1"), Value: v})
	}
	h.consumer.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.consumer.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for len(reader.commits()) < len(values) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Run returned early: %v (commits %v)", err, reader.commits())
		case <-deadline:
			cancel()
			t.Fatalf("timed out: %d of %d commits", len(reader.commits()), len(values))
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	return reader
}

func anBooked(t *testing.T, id string, at time.Time) []byte {
	t.Helper()
	_, events, err := appointment.Book(anAppt, "D1", "ACME", at.Add(time.Hour), at.Add(2*time.Hour), []string{"ASN-1"}, at)
	if err != nil {
		t.Fatal(err)
	}
	return anApptEvent(t, id, events[0])
}

// anLifecycle is a full receiving lifecycle encoded by the real encoder:
// every one of the nine events once.
func anLifecycle(t *testing.T) [][]byte {
	t.Helper()
	at := func(m int) time.Time { return anAt.Add(time.Duration(m) * time.Minute) }
	return [][]byte{
		anRegistered(t, "e-asn", anAt.Add(24*time.Hour)),
		anEncode(t, "e-asn-x", func(e *outboundkafka.AnalyticsEncoder) ([]outbox.Message, error) {
			return e.EncodeAsn(asn.ASNCancelled{Header: asn.Header{Number: "ASN-2", At: at(1)}, Reason: "no"})
		}),
		anBooked(t, "e-book", at(2)),
		anApptEvent(t, "e-in", appointment.DockAppointmentCheckedIn{Header: anApptHeader(at(60)), DoorCode: "D1", CheckedInAt: at(60)}),
		anApptEvent(t, "e-cancel", appointment.DockAppointmentCancelled{Header: appointment.Header{ID: anAppt2, At: at(3)}, DoorCode: "D1"}),
		anRcptEvent(t, "e-open", receipt.ReceiptOpened{Header: anRcptHeader(at(65)), AppointmentID: anAppt, DoorCode: "D1"}),
		anRcptEvent(t, "e-line1", receipt.ReceiptLineReceived{Header: anRcptHeader(at(70)), LineNo: 1, SKU: "SKU-1", Quantity: 30, Condition: receipt.ConditionGood}),
		anRcptEvent(t, "e-line2", receipt.ReceiptLineReceived{Header: anRcptHeader(at(71)), LineNo: 1, SKU: "SKU-1", Quantity: 4, Condition: receipt.ConditionDamaged}),
		anRcptEvent(t, "e-close", receipt.ReceiptClosed{Header: anRcptHeader(at(125)), Discrepancies: []receipt.Discrepancy{
			{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindShort, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
			{LineNo: 1, SKU: "SKU-1", Kind: receipt.KindDamaged, ExpectedQty: 40, ReceivedQty: 34, DamagedQty: 4},
		}}),
		anApptEvent(t, "e-done", appointment.DockAppointmentCompleted{Header: anApptHeader(at(125)), DoorCode: "D1", CompletedAt: at(125)}),
	}
}

func assertLifecycleDay(t *testing.T, d report.PerformanceDay) {
	t.Helper()
	counts := d.ReceiptsClosed == 1 && d.LinesReceived == 2 && d.UnitsGood == 30 && d.UnitsDamaged == 4 &&
		d.Discrepancies == (report.DiscrepancyCounts{Short: 1, Damaged: 1}) &&
		d.Appointments == (report.AppointmentCounts{Booked: 1, CheckedIn: 1, Cancelled: 1, Completed: 1})
	timings := d.ReceiptCycle.Count == 1 && d.DockDwell.Count == 1 && *d.ReceiptCycle.P50 == 3600 && *d.DockDwell.P50 == 3900
	if !counts || !timings {
		t.Fatalf("day = %+v", d)
	}
}

func TestAnalyticsConsumer_ValidEventsUpdateTheModelAndCommitInOrder(t *testing.T) {
	store := analyticsstore.NewMemory()
	h := newAnHarness(store)
	values := anLifecycle(t)
	reader := h.run(t, values...)

	day := anAt.Truncate(24 * time.Hour)
	days, err := store.PerformanceDays(context.Background(), report.Range{From: day, To: day.Add(24 * time.Hour)})
	if err != nil || len(days) != 1 {
		t.Fatalf("days = %+v, %v", days, err)
	}
	d := days[0]
	assertLifecycleDay(t, d)
	want := make([]int64, len(values))
	for i := range want {
		want[i] = int64(i)
	}
	if got := reader.commits(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commits = %v, want each offset once, in order", got)
	}
	if n := len(h.dlq.written()); n != 0 {
		t.Fatalf("%d messages dead-lettered, want 0", n)
	}
}

func TestAnalyticsConsumer_ReplayOfTheSameIDIsANoOp(t *testing.T) {
	store := analyticsstore.NewMemory()
	h := newAnHarness(store)
	closed := anRcptEvent(t, "e1", receipt.ReceiptClosed{Header: anRcptHeader(anAt), Discrepancies: []receipt.Discrepancy{}})
	reader := h.run(t, closed, closed, closed)
	day := anAt.Truncate(24 * time.Hour)
	if days, _ := store.PerformanceDays(context.Background(), report.Range{From: day, To: day.Add(24 * time.Hour)}); days[0].ReceiptsClosed != 1 {
		t.Fatalf("days = %+v", days)
	}
	if len(reader.commits()) != 3 {
		t.Fatalf("commits = %v: every duplicate must still be committed", reader.commits())
	}
}

func TestAnalyticsConsumer_UnknownTypeIsIgnoredNotDeadLettered(t *testing.T) {
	proj := &recordingProjection{}
	h := newAnHarness(proj)
	other := anWire(t, "e-other", anTypePre+"receipt.ReceiptReopened", anRcpt, anAt, map[string]any{"receipt_id": anRcpt, "asn_number": "ASN-1"})
	foreign := anWire(t, "e-foreign", "com.warehouse.wms.inventory-storage.product.ProductClassified", "SKU-1", anAt, map[string]any{"sku": "SKU-1"})
	h.run(t, other, foreign)
	if proj.callCount() != 0 || len(h.dlq.written()) != 0 {
		t.Fatalf("projection calls = %d, dlq = %d; unknown types are acknowledged untouched", proj.callCount(), len(h.dlq.written()))
	}
}

func TestAnalyticsConsumer_LegacyAndGarbageAreSkippedWithoutFloodingTheLog(t *testing.T) {
	proj := &recordingProjection{}
	h := newAnHarness(proj)
	legacy := []byte(`{"event_id":"e-1","event_type":"ReceiptClosed","occurred_at":"2026-10-05T08:00:00Z","payload":{"receipt_id":"R-1"}}`)
	garbage := []byte("not json at all \x00\x01")
	values := make([][]byte, 0, 1000)
	for i := 0; i < 500; i++ {
		values = append(values, legacy, garbage)
	}
	reader := h.run(t, values...)
	if proj.callCount() != 0 || len(h.dlq.written()) != 0 {
		t.Fatalf("projection calls = %d, dlq = %d; legacy/garbage are skipped", proj.callCount(), len(h.dlq.written()))
	}
	if len(reader.commits()) != 1000 {
		t.Fatalf("committed %d of 1000 skipped messages", len(reader.commits()))
	}
	if warns := h.logs.lines("not a CloudEvents"); warns != 1 {
		t.Fatalf("%d WARN lines for 1000 skips within one interval, want exactly 1", warns)
	}
	h.advance(61 * time.Second)
	h.run(t, legacy)
	if warns := h.logs.lines("not a CloudEvents"); warns != 2 {
		t.Fatalf("%d WARN lines after the interval elapsed, want 2", warns)
	}
	if !strings.Contains(h.logs.String(), `"suppressed_since_last_warning":999`) {
		t.Fatalf("second warning does not report the 999 suppressed skips: %s", h.logs.String())
	}
}

func TestAnalyticsConsumer_TransientFailuresAreRetriedOnTheSameMessageAndNeverDeadLettered(t *testing.T) {
	proj := &recordingProjection{errs: []error{errors.New("connection reset"), errors.New("timeout"), errors.New("timeout")}}
	h := newAnHarness(proj)
	reader := h.run(t, anRegistered(t, "e1", time.Time{}))
	if proj.callCount() != 4 {
		t.Fatalf("Apply called %d times, want 4 (three transient failures then success)", proj.callCount())
	}
	if got := reader.commits(); !reflect.DeepEqual(got, []int64{0}) {
		t.Fatalf("commits = %v: committed once, after success", got)
	}
	if len(h.dlq.written()) != 0 {
		t.Fatal("a transient failure must never reach the DLQ")
	}
}

func TestAnalyticsConsumer_ATransientFailureIsReturned(t *testing.T) {
	h := newAnHarness(&recordingProjection{errs: []error{errors.New("db down")}})
	err := h.consumer.HandleMessage(context.Background(), kafkago.Message{Value: anRegistered(t, "e1", time.Time{})})
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("HandleMessage = %v, want the transient error", err)
	}
}

func TestAnalyticsConsumer_DeterministicPoisonGoesToTheDLQWithTheRawBytes(t *testing.T) {
	line := func(extra map[string]any) map[string]any {
		m := map[string]any{"receipt_id": anRcpt, "asn_number": "ASN-1", "line_no": 1, "sku": "SKU-1", "quantity": 5, "condition": "Good"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	typ := func(name string) string { return anTypePre + name }
	cases := []struct {
		name string
		raw  []byte
	}{
		{"asn number is not the subject", anWire(t, "p1", typ("asn.ASNRegistered"), "ASN-2", anAt, map[string]any{"asn_number": "ASN-1"})},
		{"asn event without a number", anWire(t, "p2", typ("asn.ASNCancelled"), "", anAt, map[string]any{})},
		{"appointment without an id", anWire(t, "p3", typ("dockappointment.DockAppointmentBooked"), "", anAt, map[string]any{})},
		{"receipt without an asn number", anWire(t, "p4", typ("receipt.ReceiptOpened"), anRcpt, anAt, map[string]any{"receipt_id": anRcpt})},
		{"no time", anWire(t, "p5", typ("receipt.ReceiptOpened"), anRcpt, time.Time{}, map[string]any{"receipt_id": anRcpt, "asn_number": "ASN-1"})},
		{"wrong shape", anWire(t, "p6", typ("asn.ASNRegistered"), "ASN-1", anAt, map[string]any{"asn_number": 42})},
		{"quantity 0", anWire(t, "p7", typ("receipt.ReceiptLineReceived"), anRcpt, anAt, line(map[string]any{"quantity": 0}))},
		{"line number 0", anWire(t, "p8", typ("receipt.ReceiptLineReceived"), anRcpt, anAt, line(map[string]any{"line_no": 0}))},
		{"unknown condition", anWire(t, "p9", typ("receipt.ReceiptLineReceived"), anRcpt, anAt, line(map[string]any{"condition": "Soggy"}))},
		{"unknown discrepancy kind", anWire(t, "p10", typ("receipt.ReceiptClosed"), anRcpt, anAt, map[string]any{
			"receipt_id": anRcpt, "asn_number": "ASN-1", "discrepancies": []map[string]any{{"line_no": 1, "sku": "S", "kind": "Bogus"}}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := &recordingProjection{}
			h := newAnHarness(proj)
			reader := h.run(t, tc.raw)
			if proj.callCount() != 0 {
				t.Fatal("Apply called for poison")
			}
			assertDeadLettered(t, h, reader, tc.raw)
		})
	}
}

// assertDeadLettered checks the DLQ holds exactly raw (bytes and key) with
// the x-dlq-* context of offset 0, and that the offset was committed.
func assertDeadLettered(t *testing.T, h *anHarness, reader *fakeReader, raw []byte) {
	t.Helper()
	got := h.dlq.written()
	if len(got) != 1 || !bytes.Equal(got[0].Value, raw) || string(got[0].Key) != "ASN-1" {
		t.Fatalf("dlq = %d messages; want exactly the raw poison bytes and key", len(got))
	}
	headers := map[string]string{}
	for _, hd := range got[0].Headers {
		headers[hd.Key] = string(hd.Value)
	}
	want := map[string]string{
		"x-dlq-source-topic": "warehouse.inbound-receiving.analytics", "x-dlq-source-partition": "2",
		"x-dlq-source-offset": "0", "x-dlq-failed-at": "2026-10-05T12:00:00Z",
	}
	for k, v := range want {
		if headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, headers[k], v)
		}
	}
	if headers["x-dlq-error"] == "" || !reflect.DeepEqual(reader.commits(), []int64{0}) {
		t.Fatalf("dlq error = %q, commits = %v; want error context and the offset committed", headers["x-dlq-error"], reader.commits())
	}
}

func TestAnalyticsConsumer_ARejectionByTheStoreIsDeadLetteredAfterASingleAttempt(t *testing.T) {
	proj := &recordingProjection{errs: []error{report.ErrRejected}}
	h := newAnHarness(proj)
	raw := anRegistered(t, "e1", time.Time{})
	h.run(t, raw)
	if proj.callCount() != 1 {
		t.Fatalf("Apply called %d times, want 1", proj.callCount())
	}
	if got := h.dlq.written(); len(got) != 1 || !bytes.Equal(got[0].Value, raw) {
		t.Fatalf("dlq = %+v", got)
	}
}

func TestAnalyticsConsumer_ADLQWriteFailureRetriesTheMessageInsteadOfLosingIt(t *testing.T) {
	h := newAnHarness(&recordingProjection{})
	h.dlq.fail = 2
	bad := anWire(t, "bad", anTypePre+"asn.ASNRegistered", "ASN-1", anAt, map[string]any{})
	reader := h.run(t, bad)
	if len(h.dlq.written()) != 1 {
		t.Fatalf("dlq = %d, want the poison written once the DLQ recovered", len(h.dlq.written()))
	}
	if got := reader.commits(); !reflect.DeepEqual(got, []int64{0}) {
		t.Fatalf("commits = %v: commit only after the DLQ write succeeded", got)
	}
}

// Every kind maps exactly as documented in ADR 0006.
func TestAnalyticsConsumer_EventMapping(t *testing.T) {
	proj := &recordingProjection{}
	h := newAnHarness(proj)
	arrival := anAt.Add(24 * time.Hour)
	values := anLifecycle(t)
	h.run(t, values...)
	at := func(m int) time.Time { return anAt.Add(time.Duration(m) * time.Minute) }

	asnReg := report.Event{Kind: report.KindAsnRegistered, EventID: "e-asn", At: anAt, AsnNumber: "ASN-1", ExpectedArrival: &arrival}
	appt := func(k report.Kind, id string, m int, apptID string) report.Event {
		return report.Event{Kind: k, EventID: id, At: at(m), AppointmentID: apptID}
	}
	rcpt := func(k report.Kind, id string, m int) report.Event {
		return report.Event{Kind: k, EventID: id, At: at(m), AsnNumber: "ASN-1", ReceiptID: anRcpt}
	}
	line1 := rcpt(report.KindReceiptLineReceived, "e-line1", 70)
	line1.Line = &report.LineReceived{LineNo: 1, SKU: "SKU-1", Quantity: 30, Condition: report.ConditionGood}
	line2 := rcpt(report.KindReceiptLineReceived, "e-line2", 71)
	line2.Line = &report.LineReceived{LineNo: 1, SKU: "SKU-1", Quantity: 4, Condition: report.ConditionDamaged}
	closed := rcpt(report.KindReceiptClosed, "e-close", 125)
	closed.Discrepancies = []report.Discrepancy{
		{LineNo: 1, SKU: "SKU-1", Kind: report.DiscrepancyShort}, {LineNo: 1, SKU: "SKU-1", Kind: report.DiscrepancyDamaged},
	}
	want := []report.Event{
		asnReg,
		{Kind: report.KindAsnCancelled, EventID: "e-asn-x", At: at(1), AsnNumber: "ASN-2"},
		appt(report.KindAppointmentBooked, "e-book", 2, anAppt),
		appt(report.KindAppointmentCheckedIn, "e-in", 60, anAppt),
		appt(report.KindAppointmentCancelled, "e-cancel", 3, anAppt2),
		rcpt(report.KindReceiptOpened, "e-open", 65), line1, line2, closed,
		appt(report.KindAppointmentCompleted, "e-done", 125, anAppt),
	}
	if !reflect.DeepEqual(proj.events, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", proj.events, want)
	}
}

func TestAnalyticsConsumer_CloseReleasesReaderAndDLQ(t *testing.T) {
	reader, dlq := &fakeReader{}, &fakeDLQ{}
	c := &AnalyticsConsumer{Reader: reader, DLQ: dlq}
	if err := c.Close(); err != nil || !reader.closed || !dlq.closed {
		t.Fatalf("close = %v, reader closed %v, dlq closed %v", err, reader.closed, dlq.closed)
	}
	if err := (&AnalyticsConsumer{}).Close(); err != nil {
		t.Fatalf("closing an unwired consumer = %v", err)
	}
}

func TestNewAnalyticsConsumer_DerivesTheDLQTopicAndNeverDials(t *testing.T) {
	c := NewAnalyticsConsumer([]string{"127.0.0.1:1"}, "warehouse.inbound-receiving.analytics", "g", &recordingProjection{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = c.Close() }()
	if c.DLQTopic != "warehouse.inbound-receiving.analytics.dlq" {
		t.Fatalf("dlq topic = %q", c.DLQTopic)
	}
	w, ok := c.DLQ.(*kafkago.Writer)
	if !ok || w.Topic != c.DLQTopic || w.RequiredAcks != kafkago.RequireAll || !w.AllowAutoTopicCreation || w.BatchTimeout != dlqBatchTimeout {
		t.Fatalf("dlq writer = %+v", c.DLQ)
	}
	if _, ok := w.Balancer.(*kafkago.Hash); !ok {
		t.Fatalf("dlq balancer = %T, want *kafkago.Hash", w.Balancer)
	}
}

func TestWriteDLQ_RetriesOnlyWhileTheTopicIsNotReady(t *testing.T) {
	notReady := &scriptedDLQ{errs: []error{kafkago.UnknownTopicOrPartition, kafkago.WriteErrors{kafkago.LeaderNotAvailable}}}
	if err := writeDLQ(context.Background(), notReady, kafkago.Message{}); err != nil || notReady.calls != 3 {
		t.Fatalf("err = %v after %d calls, want success on the 3rd", err, notReady.calls)
	}
	other := &scriptedDLQ{errs: []error{errors.New("auth failed")}}
	if err := writeDLQ(context.Background(), other, kafkago.Message{}); err == nil || other.calls != 1 {
		t.Fatalf("err = %v after %d calls, want the first non-readiness error returned at once", err, other.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stuck := &scriptedDLQ{errs: []error{kafkago.UnknownTopicOrPartition}}
	if err := writeDLQ(ctx, stuck, kafkago.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}

func TestIsTopicNotReady(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"unknown topic":     {kafkago.UnknownTopicOrPartition, true},
		"no leader":         {kafkago.LeaderNotAvailable, true},
		"write errors, all": {kafkago.WriteErrors{kafkago.LeaderNotAvailable, nil}, true},
		"write errors, mix": {kafkago.WriteErrors{kafkago.LeaderNotAvailable, errors.New("x")}, false},
		"write errors, nil": {kafkago.WriteErrors{nil}, false},
		"other":             {errors.New("x"), false},
	} {
		if got := isTopicNotReady(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// scriptedDLQ returns errs in order, then succeeds.
type scriptedDLQ struct {
	errs  []error
	calls int
}

func (s *scriptedDLQ) WriteMessages(context.Context, ...kafkago.Message) error {
	s.calls++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return err
	}
	return nil
}

func (s *scriptedDLQ) Close() error { return nil }
