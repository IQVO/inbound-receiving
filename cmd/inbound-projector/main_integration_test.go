//go:build integration

package main

// The real entry point of cmd/inbound-projector against real infrastructure,
// all started with testcontainers (never skip-gated, never a hardcoded
// broker): an EMPTY analytical Postgres (the binary's own embedded analytics
// migrations run at boot, docs/docs/operations/configuration.md) and a real
// Kafka. The repo's own FanoutEncoder writes the ASN and receipt events of a
// small receiving story to BOTH production topics exactly the way the OLTP
// outbox does (one CloudEvents id per occurrence, ADR 0006 section 3), the
// projector consumes warehouse.inbound-receiving.analytics and folds them
// into the documented fact tables (ADR 0006 section 1), and a SIGTERM drains
// the consumer cleanly.
//
// Every assertion traces to documented behaviour:
//   - boot runs the analytics migrations over the direct DSN and /readyz is
//     200 only while the analytical database answers a ping AND a broker
//     answers a metadata request (cmd/inbound-projector/main.go, ADR 0006);
//   - the projector is the only writer of the analytical database
//     (AGENTS.md rule 7);
//   - ASNRegistered folds supplier_ref, expected_arrival, registered_at and
//     (through ReceiptOpened) receipt_opened_at into asn_facts;
//     ASNCancelled folds cancelled_at (ADR 0006, report package);
//   - ReceiptOpened/ReceiptClosed fold opened_at/closed_at into
//     receipt_facts, ReceiptLineReceived appends receipt_line_events,
//     ReceiptClosed appends receipt_discrepancies (Short/Over/Damaged);
//   - ReceiptStowCompleted folds the dock-to-stock fact (ADR 0021);
//   - each applied event claims its CloudEvents id in
//     analytics_processed_events exactly once (ADR 0006 section 5).

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// startEmptyPostgres starts a FRESH analytical Postgres: no migration runs
// here, the binary's own boot does (unlike cmd/api's helper, which migrates).
func startEmptyPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_analytics"),
		tcpostgres.WithUsername("itest"), tcpostgres.WithPassword("itest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return url
}

func startKafka(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("inbound-projector-itest"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	brokers, err := container.Brokers(ctx)
	if err != nil {
		t.Fatalf("brokers: %v", err)
	}
	return brokers
}

// createTopic creates the topic explicitly and waits for its partition
// leader, the working recipe of the repo's kafka integration tests.
func createTopic(t *testing.T, broker, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", broker)
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

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func httpOK(url string) bool {
	resp, err := http.Get(url) //nolint:noctx // probe of the binary under test
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestProjectorBootsMigratesProjectsAndStopsCleanly(t *testing.T) {
	url := startEmptyPostgres(t)
	brokers := startKafka(t)
	for _, topic := range []string{outboundkafka.Topic, outboundkafka.AnalyticsTopic} {
		createTopic(t, brokers[0], topic)
	}

	// The repo's own FanoutEncoder: every domain event becomes the
	// integration message AND the analytics message under one id (ADR 0006
	// section 3), exactly what cmd/api's outbox relays to the broker.
	encoder := outboundkafka.NewFanoutEncoder()
	regAt := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	reg2At := time.Date(2026, 11, 10, 8, 5, 0, 0, time.UTC)
	cancelAt := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	expected := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	openAt := time.Date(2026, 11, 11, 8, 5, 0, 0, time.UTC)
	line1At := time.Date(2026, 11, 11, 8, 10, 0, 0, time.UTC)
	line2At := time.Date(2026, 11, 11, 8, 20, 0, 0, time.UTC)
	closeAt := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	stowEventAt := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	asnHeader := func(number string, at time.Time) asn.Header { return asn.Header{Number: asn.Number(number), At: at} }
	rcptHeader := func(id string, at time.Time) receipt.Header {
		return receipt.Header{ID: receipt.ID(id), Asn: asn.Number("ASN-ITCOV-1"), At: at}
	}
	asnMsgs, err := encoder.EncodeAsn(
		asn.ASNRegistered{Header: asnHeader("ASN-ITCOV-1", regAt), SupplierRef: "ACME", ExpectedArrival: expected, RegisteredAt: regAt},
		asn.ASNRegistered{Header: asnHeader("ASN-ITCOV-2", reg2At), SupplierRef: "ACME"},
		asn.ASNCancelled{Header: asnHeader("ASN-ITCOV-2", cancelAt), Reason: "supplier"},
	)
	if err != nil {
		t.Fatal(err)
	}
	receiptMsgs, err := encoder.EncodeReceipt(
		receipt.ReceiptOpened{Header: rcptHeader("RCPT-ITCOV-1", openAt), AppointmentID: appointment.ID("appt-itcov-1"), DoorCode: appointment.DoorCode("D1")},
		receipt.ReceiptLineReceived{Header: rcptHeader("RCPT-ITCOV-1", line1At), LineNo: 1, SKU: shared.SKU("SKU-ITCOV-1"), Quantity: 8, Condition: receipt.ConditionGood},
		receipt.ReceiptLineReceived{Header: rcptHeader("RCPT-ITCOV-1", line2At), LineNo: 2, SKU: shared.SKU("SKU-ITCOV-2"), Quantity: 3, Condition: receipt.ConditionDamaged},
		receipt.ReceiptClosed{Header: rcptHeader("RCPT-ITCOV-1", closeAt), Discrepancies: []receipt.Discrepancy{
			{LineNo: 1, SKU: shared.SKU("SKU-ITCOV-1"), Kind: receipt.KindShort},
			{LineNo: 2, SKU: shared.SKU("SKU-ITCOV-2"), Kind: receipt.KindOver},
		}},
		receipt.ReceiptStowCompleted{Header: rcptHeader("RCPT-ITCOV-1", stowEventAt), AppointmentID: appointment.ID("appt-itcov-1"),
			ArrivedAt: time.Date(2026, 11, 11, 7, 50, 0, 0, time.UTC), ClosedAt: closeAt,
			StowedAt: time.Date(2026, 11, 11, 9, 50, 0, 0, time.UTC), DockToStockSeconds: 7200, CartonCount: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	producer := &kafkago.Writer{Addr: kafkago.TCP(brokers...), BatchTimeout: 10 * time.Millisecond, RequiredAcks: kafkago.RequireAll}
	t.Cleanup(func() { _ = producer.Close() })
	out := append(asnMsgs, receiptMsgs...)
	msgs := make([]kafkago.Message, 0, len(out))
	for _, m := range out {
		km := kafkago.Message{Topic: m.Topic, Key: m.Key, Value: m.Value}
		for _, h := range m.Headers {
			km.Headers = append(km.Headers, kafkago.Header{Key: h.Key, Value: []byte(h.Value)})
		}
		msgs = append(msgs, km)
	}
	if err := producer.WriteMessages(context.Background(), msgs...); err != nil {
		t.Fatal(err)
	}

	// The binary's real environment (docs/docs/operations/configuration.md):
	// direct DSN over an EMPTY database (boot migrates), brokers, an
	// env-supplied consumer group, a free admin port and the sweeper off.
	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", url)
	t.Setenv("KAFKA_BROKERS", strings.Join(brokers, ","))
	t.Setenv("ANALYTICS_CONSUMER_GROUP", "inbound-projector-itest")
	t.Setenv("ADMIN_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("HOUSEKEEPING_INTERVAL", "0")

	done := make(chan error, 1)
	go func() { done <- run() }()

	// The documented probes: /healthz is liveness only; /readyz is 200 only
	// once the migrations ran, the analytical database pings and a broker
	// answers a metadata request.
	waitUntil(t, 90*time.Second, "/healthz 200", func() bool { return httpOK("http://" + addr + "/healthz") })
	waitUntil(t, 90*time.Second, "/readyz 200", func() bool {
		select {
		case err := <-done:
			t.Fatalf("the projector exited before becoming ready: %v", err)
			return false
		default:
		}
		return httpOK("http://" + addr + "/readyz")
	})

	ctx := context.Background()
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// The documented projection: every event folded into its fact table and
	// every CloudEvents id claimed exactly once (8 events in total).
	waitUntil(t, 90*time.Second, "all eight events projected", func() bool {
		var n int
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM asn_facts)
			+ (SELECT count(*) FROM receipt_facts)
			+ (SELECT count(*) FROM receipt_line_events)
			+ (SELECT count(*) FROM receipt_discrepancies)
			+ (SELECT count(*) FROM dock_to_stock_facts)
			+ (SELECT count(*) FROM analytics_processed_events)`).Scan(&n)
		return err == nil && n == 16
	})

	var supplier string
	var gotExpected, gotRegistered, gotOpened, gotCancelled *time.Time
	if err := pool.QueryRow(ctx, `SELECT supplier_ref, expected_arrival, registered_at, receipt_opened_at, cancelled_at
		FROM asn_facts WHERE asn_number = 'ASN-ITCOV-1'`).Scan(&supplier, &gotExpected, &gotRegistered, &gotOpened, &gotCancelled); err != nil {
		t.Fatal(err)
	}
	if supplier != "ACME" || gotExpected == nil || !gotExpected.Equal(expected) ||
		gotRegistered == nil || !gotRegistered.Equal(regAt) || gotOpened == nil || !gotOpened.Equal(openAt) || gotCancelled != nil {
		t.Errorf("asn_facts[ASN-ITCOV-1] = supplier %q expected %v registered %v opened %v cancelled %v", supplier, gotExpected, gotRegistered, gotOpened, gotCancelled)
	}
	var cancelled2 *time.Time
	if err := pool.QueryRow(ctx, `SELECT cancelled_at FROM asn_facts WHERE asn_number = 'ASN-ITCOV-2'`).Scan(&cancelled2); err != nil {
		t.Fatal(err)
	}
	if cancelled2 == nil || !cancelled2.Equal(cancelAt) {
		t.Errorf("asn_facts[ASN-ITCOV-2].cancelled_at = %v, want %v", cancelled2, cancelAt)
	}
	var gotOpen, gotClose *time.Time
	if err := pool.QueryRow(ctx, `SELECT opened_at, closed_at FROM receipt_facts WHERE receipt_id = 'RCPT-ITCOV-1'`).Scan(&gotOpen, &gotClose); err != nil {
		t.Fatal(err)
	}
	if gotOpen == nil || !gotOpen.Equal(openAt) || gotClose == nil || !gotClose.Equal(closeAt) {
		t.Errorf("receipt_facts[RCPT-ITCOV-1] = opened %v closed %v, want %v / %v", gotOpen, gotClose, openAt, closeAt)
	}
	rows, err := pool.Query(ctx, `SELECT line_no, sku, quantity, condition FROM receipt_line_events
		WHERE receipt_id = 'RCPT-ITCOV-1' ORDER BY line_no`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []struct {
		lineNo    int
		sku       string
		quantity  int64
		condition string
	}
	for rows.Next() {
		var l struct {
			lineNo    int
			sku       string
			quantity  int64
			condition string
		}
		if err := rows.Scan(&l.lineNo, &l.sku, &l.quantity, &l.condition); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 ||
		lines[0].lineNo != 1 || lines[0].sku != "SKU-ITCOV-1" || lines[0].quantity != 8 || lines[0].condition != "Good" ||
		lines[1].lineNo != 2 || lines[1].sku != "SKU-ITCOV-2" || lines[1].quantity != 3 || lines[1].condition != "Damaged" {
		t.Errorf("receipt_line_events = %+v, want line 1 x8 Good and line 2 x3 Damaged", lines)
	}
	var short, over int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'Short'), count(*) FILTER (WHERE kind = 'Over')
		FROM receipt_discrepancies WHERE receipt_id = 'RCPT-ITCOV-1'`).Scan(&short, &over); err != nil {
		t.Fatal(err)
	}
	if short != 1 || over != 1 {
		t.Errorf("receipt_discrepancies = short %d over %d, want 1 and 1", short, over)
	}
	var dockToStock int64
	var cartons int
	if err := pool.QueryRow(ctx, `SELECT dock_to_stock_seconds, carton_count FROM dock_to_stock_facts WHERE receipt_id = 'RCPT-ITCOV-1'`).Scan(&dockToStock, &cartons); err != nil {
		t.Fatal(err)
	}
	if dockToStock != 7200 || cartons != 3 {
		t.Errorf("dock_to_stock_facts = %ds for %d cartons, want 7200s for 3", dockToStock, cartons)
	}

	// SIGTERM is the documented drain: readiness flips, the admin server and
	// the consumer stop inside the shutdown timeout, and run returns nil.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a SIGTERM'd projector must drain cleanly, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the projector did not stop")
	}
}
