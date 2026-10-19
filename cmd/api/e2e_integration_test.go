//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The end-to-end proof that the shipped binary is really wired: the test boots
// the REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it only
// through REST and Kafka. It reads the effects from Postgres and from the
// produced topics (the integration topic and the analytics topic), restarts
// the service, and finally takes the dependencies away to watch /readyz
// follow them. Every Kafka read is bounded by a deadline; run it with
// -timeout 300s.

const (
	topicProducts  = "warehouse.product-master.events"
	topicFacility  = "warehouse.facility.events"
	topicEvents    = "warehouse.inbound-receiving.events"
	topicAnalytics = "warehouse.inbound-receiving.analytics"

	source     = "/warehouse/inbound-receiving"
	typePrefix = "com.warehouse.wms.inbound-receiving."

	doorCode = "WH1-DOCK-IN-01"
)

// containerStartTimeout bounds a testcontainers start.
const containerStartTimeout = 120 * time.Second

func startPostgres(t *testing.T) (*tcpostgres.PostgresContainer, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_e2e"), tcpostgres.WithUsername("e2e"), tcpostgres.WithPassword("e2e"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	return c, url
}

func startKafka(t *testing.T) (*tckafka.KafkaContainer, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("inbound-receiving-e2e"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	return c, brokers
}

// topicExists asks the broker for the topic through a metadata request that
// cannot create it (kafka-go's Client.Metadata never auto-creates).
func topicExists(t *testing.T, brokers []string, topic string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := (&kafkago.Client{Addr: kafkago.TCP(brokers...)}).Metadata(ctx, &kafkago.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	for _, tp := range resp.Topics {
		if tp.Name == topic && tp.Error == nil {
			return true
		}
	}
	return false
}

// createTopic creates a one-partition topic and waits for its leader.
func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// produce writes already-encoded messages to topic with RequireAll acks.
func produce(t *testing.T, brokers []string, topic string, msgs ...kafkago.Message) {
	t.Helper()
	transport := &kafkago.Transport{}
	defer transport.CloseIdleConnections()
	w := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond, Transport: transport}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

// inputEvent builds a CloudEvent as a sibling context publishes it.
func inputEvent(t *testing.T, id, src, typ, subject string, data map[string]any) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(src)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Now().UTC())
	e.SetDataSchema("urn:warehouse:e2e:events:X:v1")
	if err := e.SetData("application/json", data); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// consume reads exactly n messages of the one-partition topic from the start,
// bounded by a deadline.
func consume(t *testing.T, brokers []string, topic string, n int) []kafkago.Message {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	var out []kafkago.Message
	for len(out) < n {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read %s: got %d of %d messages: %v", topic, len(out), n, err)
		}
		out = append(out, m)
	}
	return out
}

// noMoreMessages fails if the topic holds more than want messages after a
// settling period (nothing was duplicated).
func noMoreMessages(t *testing.T, brokers []string, topic string, want int) {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := 0
	for {
		if _, err := r.ReadMessage(ctx); err != nil {
			break
		}
		got++
	}
	if got != want {
		t.Fatalf("%s holds %d messages, want exactly %d", topic, got, want)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// service is one running instance of the real entry point.
type service struct {
	t      *testing.T
	base   string
	cancel context.CancelFunc
	done   chan error
}

// env is the environment a deployment sets.
type env struct {
	dbURL   string
	brokers []string
	addr    string
}

func (e env) apply(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", e.dbURL)
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", strings.Join(e.brokers, ","))
	t.Setenv("EVENT_PUBLISHER", "kafka")
	t.Setenv("PRODUCT_MODE", "kafka")
	t.Setenv("PRODUCT_CONSUMER_GROUP", "inbound-receiving-product-e2e")
	t.Setenv("DOCK_DOOR_MODE", "kafka")
	t.Setenv("DOCK_DOOR_CONSUMER_GROUP", "inbound-receiving-dock-doors-e2e")
	t.Setenv("HTTP_ADDR", e.addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "200ms")
	t.Setenv("LOG_LEVEL", "warn")
}

// start boots run in-process and waits until the listener answers /healthz.
func (e env) start(t *testing.T) *service {
	t.Helper()
	e.apply(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + e.addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(s.stopQuietly)
	waitFor(t, 90*time.Second, "the service to listen on "+e.addr, func() bool {
		select {
		case err := <-s.done:
			t.Fatalf("run returned while booting: %v", err)
		default:
		}
		status, _ := s.try(http.MethodGet, "/healthz", "", "")
		return status == http.StatusOK
	})
	return s
}

// stop cancels run and requires a clean shutdown within a bound.
func (s *service) stop() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(60 * time.Second):
		s.t.Fatal("run did not return after the shutdown")
	}
	s.done <- nil // let the cleanup find it stopped
}

func (s *service) stopQuietly() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
	}
}

// try performs a request and returns -1 when the connection fails. A non-empty
// key is sent as the Idempotency-Key.
func (s *service) try(method, path, body, key string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error()
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (s *service) call(method, path, body, key string, want int) string {
	s.t.Helper()
	status, raw := s.try(method, path, body, key)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// pgView reads the service's own database.
type pgView struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func newPGView(t *testing.T, url string) pgView {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pgView{t: t, pool: pool}
}

func (v pgView) count(query string, args ...any) int {
	v.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := v.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		v.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The CloudEvents context attributes of a published message, plus its data.
type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, m kafkago.Message) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		t.Fatalf("not a CloudEvent: %v: %s", err, m.Value)
	}
	return e
}

// expectedEvent is one published occurrence: entity segment, event name,
// subject (CloudEvents `subject`) and Kafka key.
type expectedEvent struct{ entity, name, subject, key string }

// assertEnvelope checks every context attribute and the Kafka key/header of a
// message of this service.
func assertEnvelope(t *testing.T, m kafkago.Message, want expectedEvent, stream string) envelope {
	t.Helper()
	e := decodeEnvelope(t, m)
	wantType := typePrefix + want.entity + "." + want.name
	schema := fmt.Sprintf("urn:warehouse:inbound-receiving:%s:%s:v1", stream, want.name)
	if e.SpecVersion != "1.0" || e.Type != wantType || e.Source != source || e.DataSchema != schema ||
		e.DataContentType != "application/json" || e.Subject != want.subject || e.ID == "" {
		t.Fatalf("envelope = %+v, want 1.0 %s from %s (%s) about %s", e, wantType, source, schema, want.subject)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		t.Fatalf("time %q is not RFC 3339: %v", e.Time, err)
	}
	if string(m.Key) != want.key {
		t.Fatalf("kafka key = %q, want %q", m.Key, want.key)
	}
	var ct string
	for _, h := range m.Headers {
		if h.Key == "content-type" {
			ct = string(h.Value)
		}
	}
	if ct != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("content-type header = %q", ct)
	}
	return e
}

func readyz(s *service) (int, string) { return s.try(http.MethodGet, "/readyz", "", "") }

func waitReadyzStatus(t *testing.T, s *service, want int, what string) string {
	t.Helper()
	var body string
	waitFor(t, 60*time.Second, what, func() bool {
		var status int
		status, body = readyz(s)
		return status == want
	})
	return body
}

// world is what the phases share.
type world struct {
	e         env
	pg        *tcpostgres.PostgresContainer
	kc        *tckafka.KafkaContainer
	db        pgView
	brokers   []string
	receiptID string
	apptID    string
	expected  []expectedEvent
}

const asnBody = `{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}`

// Phase 1: the output topics do not exist yet (a fresh cluster before
// anything published). Readiness needs Postgres plus a broker answering
// metadata, NOT topics, so the service is ready at once, and it creates no
// output topic itself.
func phaseTopicsMissing(t *testing.T, s *service, brokers []string) {
	t.Helper()
	body := waitReadyzStatus(t, s, http.StatusOK, "/readyz to be 200 although no output topic exists")
	if !strings.Contains(body, "ready") {
		t.Fatalf("readyz = %s", body)
	}
	for _, topic := range []string{topicEvents, topicAnalytics} {
		if topicExists(t, brokers, topic) {
			t.Fatalf("topic %s exists: the service must never create its output topics itself", topic)
		}
	}
}

// Phase 2: the local copies are fed through Kafka only. Before the SKU is
// known the SKU copy is enforced (422); after the ProductRegistered event
// (delivered twice, same CloudEvents id) and the dock-door event, the copies
// hold exactly one SKU and one door.
func phaseLocalCopies(t *testing.T, s *service, w *world) {
	t.Helper()
	assertProblem(t, s.call(http.MethodPost, "/asns", asnBody, "pre-1", http.StatusUnprocessableEntity), "unknown-sku")

	product := inputEvent(t, "prod-1", "/warehouse/product-master", "com.warehouse.wms.product-master.product.ProductRegistered",
		"SKU-1", map[string]any{"sku": "SKU-1", "description": "Pallet of tomatoes", "version": 1})
	produce(t, w.brokers, topicProducts,
		kafkago.Message{Key: []byte("SKU-1"), Value: product}, kafkago.Message{Key: []byte("SKU-1"), Value: product})
	produce(t, w.brokers, topicFacility, kafkago.Message{Key: []byte(doorCode), Value: inputEvent(t, "slot-1",
		"/warehouse/facility-layout", "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered", doorCode,
		map[string]any{"eventName": "LocationSlotRegistered", "locationCode": doorCode, "locationType": "DockDoor", "role": "Dock", "dockFlow": "Inbound", "maxWeightKg": 0, "maxVolumeM3": 0})})

	waitFor(t, 60*time.Second, "both local copies to be fed from Kafka", func() bool {
		return w.db.count(`SELECT count(*) FROM known_skus WHERE sku = 'SKU-1'`) == 1 &&
			w.db.count(`SELECT count(*) FROM dock_doors WHERE door_code = $1`, doorCode) == 1
	})
	if n := w.db.count(`SELECT count(*) FROM processed_events`); n != 2 {
		t.Fatalf("processed_events = %d, want 2 (the duplicate delivery was deduped by CloudEvents id)", n)
	}
	docks := s.call(http.MethodGet, "/docks", "", "", http.StatusOK)
	if !strings.Contains(docks, `"mode":"kafka"`) || !strings.Contains(docks, doorCode) {
		t.Fatalf("GET /docks = %s, want mode kafka listing %s", docks, doorCode)
	}
}

func assertProblem(t *testing.T, body, slug string) {
	t.Helper()
	if !strings.Contains(body, "/"+slug+`"`) {
		t.Fatalf("body %s is not the %s problem", body, slug)
	}
}

// Phase 3: the whole receiving workflow through REST only. Five occurrences
// reach the outbox, each written to the integration AND the analytics topic.
func phaseWorkflow(t *testing.T, s *service, w *world) {
	t.Helper()
	first := s.call(http.MethodPost, "/asns", asnBody, "reg-1", http.StatusCreated)
	// The same key and body replays the first response and records nothing new.
	if replay := s.call(http.MethodPost, "/asns", asnBody, "reg-1", http.StatusCreated); replay != first {
		t.Fatalf("idempotent replay = %s, want the first response %s", replay, first)
	}
	assertProblem(t, s.call(http.MethodPost, "/asns", `{"asnNumber":"ASN-2","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}`,
		"reg-1", http.StatusUnprocessableEntity), "idempotency-key-reused")

	start := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Hour)
	appt := fmt.Sprintf(`{"doorCode":%q,"carrier":"ACME Freight","windowStart":%q,"windowEnd":%q,"asnNumbers":["ASN-1"]}`,
		doorCode, start.Format(time.RFC3339), start.Add(2*time.Hour).Format(time.RFC3339))
	var booked struct {
		AppointmentID string `json:"appointmentId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/appointments", appt, "book-1", http.StatusCreated), &booked)
	w.apptID = booked.AppointmentID

	var opened struct {
		ReceiptID string `json:"receiptId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/receipts", `{"asnNumber":"ASN-1"}`, "open-1", http.StatusCreated), &opened)
	w.receiptID = opened.ReceiptID
	s.call(http.MethodPost, "/receipts/"+w.receiptID+"/lines", `{"lineNo":1,"quantity":10,"condition":"Good"}`, "line-1", http.StatusCreated)
	s.call(http.MethodPost, "/receipts/"+w.receiptID+"/close", "", "", http.StatusOK)

	w.expected = []expectedEvent{
		{"asn", "ASNRegistered", "ASN-1", "ASN-1"},
		{"dockappointment", "DockAppointmentBooked", w.apptID, w.apptID},
		{"receipt", "ReceiptOpened", w.receiptID, "ASN-1"},
		{"receipt", "ReceiptLineReceived", w.receiptID, "ASN-1"},
		{"receipt", "ReceiptClosed", w.receiptID, "ASN-1"},
	}
}

func mustDecode(t *testing.T, raw string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

// Phase 4: a NUL is a uniform client error on the real runtime (the guard's
// slugs malformed-request / resource-not-found, not the domain's per-field
// ones), never a Postgres 22021 -> 500, and a rejected request does not
// consume its Idempotency-Key.
func phaseNUL(t *testing.T, s *service, w *world) {
	t.Helper()
	before := w.db.count(`SELECT count(*) FROM idempotency_keys`)
	nulBody := `{"asnNumber":"ASN-9","supplierRef":"AC\u0000ME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}`
	assertProblem(t, s.call(http.MethodPost, "/asns", nulBody, "nul-1", http.StatusBadRequest), "malformed-request")
	assertProblem(t, s.call(http.MethodGet, "/asns/AS%00N-1", "", "", http.StatusNotFound), "resource-not-found")
	if after := w.db.count(`SELECT count(*) FROM idempotency_keys`); after != before {
		t.Fatalf("idempotency_keys %d -> %d: a NUL request recorded its key", before, after)
	}
	// The same key is still free for a clean request.
	s.call(http.MethodPost, "/asns", `{"asnNumber":"ASN-9","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}`, "nul-1", http.StatusCreated)
	w.expected = append(w.expected, expectedEvent{"asn", "ASNRegistered", "ASN-9", "ASN-9"})
}

// Phase 5: the rows are in Postgres, and the relay drained every outbox row.
func phaseRows(t *testing.T, w *world) {
	t.Helper()
	db := w.db
	for table, want := range map[string]int{"asns": 2, "appointments": 1, "receipts": 1, "receipt_lines": 1} {
		if n := db.count("SELECT count(*) FROM " + table); n != want {
			t.Fatalf("%s rows = %d, want %d", table, n, want)
		}
	}
	if n := db.count(`SELECT count(*) FROM receipts WHERE id = $1 AND state = 'Closed'`, w.receiptID); n != 1 {
		t.Fatalf("receipt %s is not Closed in Postgres", w.receiptID)
	}
	// Each occurrence is written twice in the same transaction: the integration
	// topic and the analytics topic.
	if n := db.count(`SELECT count(*) FROM outbox_events`); n != 2*len(w.expected) {
		t.Fatalf("outbox rows = %d, want %d", n, 2*len(w.expected))
	}
	waitFor(t, 60*time.Second, "the relay to publish every outbox row", func() bool {
		return db.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	if n := db.count(`SELECT count(*) FROM outbox_events WHERE topic = $1`, topicEvents); n != len(w.expected) {
		t.Fatalf("outbox rows for %s = %d, want %d", topicEvents, n, len(w.expected))
	}
}

// Phase 6: the CloudEvents on both output topics, exact envelope and key.
func phaseTopics(t *testing.T, w *world) {
	t.Helper()
	for _, tc := range []struct{ topic, stream string }{{topicEvents, "events"}, {topicAnalytics, "analytics"}} {
		msgs := consume(t, w.brokers, tc.topic, len(w.expected))
		for i, m := range msgs {
			assertEnvelope(t, m, w.expected[i], tc.stream)
		}
	}
	// One occurrence, one CloudEvents id on both topics.
	ev, an := consume(t, w.brokers, topicEvents, len(w.expected)), consume(t, w.brokers, topicAnalytics, len(w.expected))
	for i := range ev {
		if decodeEnvelope(t, ev[i]).ID != decodeEnvelope(t, an[i]).ID {
			t.Fatalf("occurrence %d has different ids on the two topics", i)
		}
	}
	var reg struct {
		AsnNumber   string `json:"asn_number"`
		SupplierRef string `json:"supplier_ref"`
		Lines       []struct {
			LineNo      int    `json:"line_no"`
			SKU         string `json:"sku"`
			ExpectedQty int64  `json:"expected_qty"`
		} `json:"lines"`
	}
	mustDecode(t, string(decodeEnvelope(t, ev[0]).Data), &reg)
	if reg.AsnNumber != "ASN-1" || reg.SupplierRef != "ACME" || len(reg.Lines) != 1 || reg.Lines[0].SKU != "SKU-1" || reg.Lines[0].ExpectedQty != 10 {
		t.Fatalf("ASNRegistered data = %+v", reg)
	}
	var line struct {
		ReceiptID string `json:"receipt_id"`
		SKU       string `json:"sku"`
		Quantity  int64  `json:"quantity"`
		Condition string `json:"condition"`
	}
	mustDecode(t, string(decodeEnvelope(t, ev[3]).Data), &line)
	if line.ReceiptID != w.receiptID || line.SKU != "SKU-1" || line.Quantity != 10 || line.Condition != "Good" {
		t.Fatalf("ReceiptLineReceived data = %+v", line)
	}
}

// Phase 7: restart the real entry point in-process on the same database and
// broker: nothing is lost, nothing is published or consumed twice.
func phaseRestart(t *testing.T, w *world, s *service) *service {
	t.Helper()
	s.stop()
	s2 := w.e.start(t)
	var got struct {
		State string `json:"state"`
	}
	mustDecode(t, s2.call(http.MethodGet, "/receipts/"+w.receiptID, "", "", http.StatusOK), &got)
	if got.State != "Closed" {
		t.Fatalf("after the restart the receipt is %q, want Closed", got.State)
	}
	// Give the restarted consumers time to (wrongly) re-apply and the relay to
	// (wrongly) re-publish; neither may happen.
	time.Sleep(3 * time.Second)
	if n := w.db.count(`SELECT count(*) FROM processed_events`); n != 2 {
		t.Fatalf("processed_events after the restart = %d, want still 2", n)
	}
	noMoreMessages(t, w.brokers, topicEvents, len(w.expected))
	noMoreMessages(t, w.brokers, topicAnalytics, len(w.expected))
	return s2
}

// Phase 8: /readyz follows the dependencies. Postgres goes first (Kafka still
// up): only postgres is named. Then Kafka goes: kafka is named too.
// /healthz stays liveness and answers 200 throughout.
func phaseDependenciesGoAway(t *testing.T, s *service, w *world) {
	t.Helper()
	stopTimeout := 10 * time.Second
	if err := w.pg.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}
	body := waitReadyzStatus(t, s, http.StatusServiceUnavailable, "/readyz to turn 503 once Postgres is gone")
	if !strings.Contains(body, "dependency-unavailable") || !strings.Contains(body, "postgres") || strings.Contains(body, "kafka") {
		t.Fatalf("readyz with Postgres down = %s, want dependency-unavailable naming only postgres", body)
	}
	if err := w.kc.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop kafka: %v", err)
	}
	waitFor(t, 60*time.Second, "/readyz to name kafka as well", func() bool {
		_, body := readyz(s)
		return strings.Contains(body, "postgres") && strings.Contains(body, "kafka")
	})
	if status, body := s.try(http.MethodGet, "/healthz", "", ""); status != http.StatusOK {
		t.Fatalf("healthz with every dependency down = %d %s, want 200 (liveness only)", status, body)
	}
}

func TestRunOnRealPostgresAndKafka(t *testing.T) {
	pg, dbURL := startPostgres(t)
	kc, brokers := startKafka(t)
	// The input topics are infra's: create them the way the cluster has them.
	createTopic(t, brokers, topicProducts)
	createTopic(t, brokers, topicFacility)
	w := &world{e: env{dbURL: dbURL, brokers: brokers, addr: freeAddr(t)}, pg: pg, kc: kc, db: newPGView(t, dbURL), brokers: brokers}

	s := w.e.start(t)
	phaseTopicsMissing(t, s, brokers)
	phaseLocalCopies(t, s, w)
	phaseWorkflow(t, s, w)
	phaseNUL(t, s, w)
	phaseRows(t, w)
	phaseTopics(t, w)
	s = phaseRestart(t, w, s)
	phaseDependenciesGoAway(t, s, w)
}
