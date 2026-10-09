//go:build integration

// Integration tests for the REST inbound adapter over the REAL stack: the
// real chi router (CORS, otel, Idempotency-Key middleware) on an
// httptest.Server, the real use cases, the real Postgres repositories and
// the real transactional outbox behind it — the exact wiring of cmd/api.
// They execute the main resource lifecycle end to end over HTTP (POST 201
// with Location/ETag, GET 200 round trip, validation 4xx as RFC 7807
// problem documents with the repo's problem slugs, Idempotency-Key replay
// and reuse), which the existing handler unit tests (fake use cases) cannot
// prove.
//
// Postgres comes from testcontainers: one container per package run, one
// private database per test, migrated once. Never an external DATABASE_URL,
// never t.Skip.
package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/clock"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// One Postgres container serves the whole package, migrated once into a
// template database; each test gets a private clone (milliseconds). See
// internal/application/usecases/usecases_wiring_integration_test.go for the
// same pattern's rationale. Never an external DATABASE_URL, never t.Skip.
const templateDB = "http_migrated_template"

var (
	sharedBaseURL string
	dbSeq         atomic.Uint64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_http"),
		tcpostgres.WithUsername("inbound"),
		tcpostgres.WithPassword("inbound"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}()

	sharedBaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		return 1
	}
	if err := createDatabase(ctx, templateDB); err != nil {
		fmt.Fprintf(os.Stderr, "create template database: %v\n", err)
		return 1
	}
	if err := postgres.RunMigrations(withDB(sharedBaseURL, templateDB)); err != nil {
		fmt.Fprintf(os.Stderr, "migrate template: %v\n", err)
		return 1
	}
	return m.Run()
}

// withDB rewrites the path of a connection URL to the named database.
func withDB(baseURL, name string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// createDatabase creates an empty database inside the shared container.
func createDatabase(ctx context.Context, name string) error {
	conn, err := pgx.Connect(ctx, sharedBaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// migratedDB hands the test a connection URL to its own private database
// cloned from the migrated template.
func migratedDB(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("http_%d", dbSeq.Add(1))
	conn, err := pgx.Connect(context.Background(), sharedBaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		"CREATE DATABASE %q WITH TEMPLATE %q", name, templateDB)); err != nil {
		t.Fatalf("clone database: %v", err)
	}
	return withDB(sharedBaseURL, name)
}

// httpAPI wires the REAL stack — chi router, Idempotency-Key middleware
// over the Postgres idempotency store, every use case over the real
// Postgres repos and the real outbox encoder — over a fresh private
// database, and serves it.
type httpAPI struct {
	server *httptest.Server
	pool   *pgxpool.Pool
}

func newHTTPAPI(t *testing.T) *httpAPI {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, migratedDB(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	w := usecases.Writer{
		Asns:         postgres.NewAsnRepo(pool),
		Appointments: postgres.NewAppointmentRepo(pool),
		Receipts:     postgres.NewReceiptRepo(pool),
		Outbox:       postgres.NewOutboxRepo(pool),
		Encoder:      outboundkafka.NewEncoder(),
		UoW:          postgres.NewUnitOfWork(pool),
		Clock:        clock.System{},
		IDs:          idgen.UUID{},
	}
	router := inboundhttp.NewRouter(&inboundhttp.Server{
		RegisterAsn:        &usecases.RegisterAsn{Writer: w},
		CancelAsn:          &usecases.CancelAsn{Writer: w},
		GetAsn:             &usecases.GetAsn{Asns: postgres.NewAsnRepo(pool)},
		ListAsns:           &usecases.ListAsns{Asns: postgres.NewAsnRepo(pool)},
		BookAppointment:    &usecases.BookAppointment{Writer: w},
		CheckInAppointment: &usecases.CheckInAppointment{Writer: w},
		CancelAppointment:  &usecases.CancelAppointment{Writer: w},
		GetAppointment:     &usecases.GetAppointment{Appointments: postgres.NewAppointmentRepo(pool)},
		ListAppointments:   &usecases.ListAppointments{Appointments: postgres.NewAppointmentRepo(pool)},
		OpenReceipt:        &usecases.OpenReceipt{Writer: w},
		ReceiveLine:        &usecases.ReceiveLine{Writer: w},
		CloseReceipt:       &usecases.CloseReceipt{Writer: w},
		GetReceipt:         &usecases.GetReceipt{Receipts: postgres.NewReceiptRepo(pool)},
		ListReceipts:       &usecases.ListReceipts{Receipts: postgres.NewReceiptRepo(pool)},
		ListDocks:          &usecases.ListDocks{Doors: postgres.NewDockDoors(pool)},
		Idempotency:        postgres.NewIdempotencyStore(pool),
	})
	hs := httptest.NewServer(router)
	t.Cleanup(hs.Close)
	return &httpAPI{server: hs, pool: pool}
}

// request describes one HTTP call for do().
type request struct {
	method         string
	path           string
	idempotencyKey string
	ifMatch        string
	body           string
}

// do runs one request against the API and returns the status, headers and
// decoded JSON body.
func (a *httpAPI) do(t *testing.T, req request) (int, http.Header, map[string]any) {
	t.Helper()
	var reader io.Reader
	if req.body != "" {
		reader = strings.NewReader(req.body)
	}
	httpReq, err := http.NewRequest(req.method, a.server.URL+req.path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.body != "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if req.idempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", req.idempotencyKey)
	}
	if req.ifMatch != "" {
		httpReq.Header.Set("If-Match", req.ifMatch)
	}
	resp, err := a.server.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("%s %s: %v", req.method, req.path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return resp.StatusCode, resp.Header, decoded
}

// problemSlug strips the namespace off an RFC 7807 type, leaving the slug.
func problemSlug(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, _ := body["type"].(string)
	return strings.TrimPrefix(raw, "https://errors.inbound-receiving.warehouse-systems.dev/")
}

// outboxTypes returns the event_type of every outbox row in order.
func (a *httpAPI) outboxTypes(t *testing.T) []string {
	t.Helper()
	var all string
	if err := a.pool.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(event_type, ',' ORDER BY id), '') FROM outbox_events`).Scan(&all); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if all == "" {
		return nil
	}
	return strings.Split(all, ",")
}

// TestHTTPAsnLifecycleAndValidation drives the ASN resource through HTTP
// end to end: POST /asns -> 201 with Location + ETag, GET round trip, the
// validation problems (missing key, malformed body, domain slug, conflict,
// not found), and proves a 201 wrote both the aggregate and its outbox row
// while every 4xx wrote nothing.
func TestHTTPAsnLifecycleAndValidation(t *testing.T) {
	api := newHTTPAPI(t)
	body := `{"asnNumber":"ASN-HTTP-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}`

	// A resource-creating POST without an Idempotency-Key is a 400 problem.
	status, _, problem := api.do(t, request{method: "POST", path: "/asns", body: body})
	if status != http.StatusBadRequest || problemSlug(t, problem) != "idempotency-key-required" {
		t.Fatalf("no idempotency key: status %d problem %v", status, problem)
	}

	// Malformed JSON is a 400 malformed-request problem.
	status, _, problem = api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "key-malformed", body: `{"asnNumber":`})
	if status != http.StatusBadRequest || problemSlug(t, problem) != "malformed-request" {
		t.Fatalf("malformed body: status %d problem %v", status, problem)
	}

	// A domain validation failure is a 400 problem with its slug: line
	// numbers must run 1..n in order.
	status, _, problem = api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "key-badlines",
		body: `{"asnNumber":"ASN-HTTP-BAD","supplierRef":"ACME","lines":[{"lineNo":2,"sku":"SKU-1","expectedQty":1}]}`})
	if status != http.StatusBadRequest || problemSlug(t, problem) != "invalid-line-no" {
		t.Fatalf("bad line numbers: status %d problem %v", status, problem)
	}

	// The real thing: 201, Location, ETag, and the camelCase wire shape.
	status, header, asn := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "key-asn-1", body: body})
	if status != http.StatusCreated {
		t.Fatalf("POST /asns: status %d body %v", status, asn)
	}
	if got := header.Get("Location"); got != "/asns/ASN-HTTP-1" {
		t.Fatalf("Location = %q, want /asns/ASN-HTTP-1", got)
	}
	if got := header.Get("ETag"); got != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", got)
	}
	if asn["state"] != "Registered" || asn["version"] != 1.0 {
		t.Fatalf("created asn = %v", asn)
	}

	// GET round trip: same body, 200, ETag.
	status, header, asn = api.do(t, request{method: "GET", path: "/asns/ASN-HTTP-1"})
	if status != http.StatusOK || header.Get("ETag") != `"1"` {
		t.Fatalf("GET /asns/ASN-HTTP-1: status %d etag %q", status, header.Get("ETag"))
	}
	if asn["asnNumber"] != "ASN-HTTP-1" || asn["supplierRef"] != "ACME" {
		t.Fatalf("GET body = %v", asn)
	}

	// Listing finds it.
	status, _, page := api.do(t, request{method: "GET", path: "/asns?state=Registered"})
	if status != http.StatusOK {
		t.Fatalf("GET /asns: status %d", status)
	}
	items, _ := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["asnNumber"] != "ASN-HTTP-1" {
		t.Fatalf("list asns = %v", page)
	}

	// Re-registering the same number is a 409 conflict problem.
	status, _, problem = api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "key-asn-dup", body: body})
	if status != http.StatusConflict || problemSlug(t, problem) != "asn-already-exists" {
		t.Fatalf("duplicate: status %d problem %v", status, problem)
	}

	// Unknown number: a 404 problem slug.
	status, _, problem = api.do(t, request{method: "GET", path: "/asns/ASN-NOPE"})
	if status != http.StatusNotFound || problemSlug(t, problem) != "asn-not-found" {
		t.Fatalf("unknown asn: status %d problem %v", status, problem)
	}

	// Exactly one aggregate write and one outbox row survived; every 4xx
	// above persisted nothing.
	if types := api.outboxTypes(t); len(types) != 1 || !strings.Contains(types[0], "ASNRegistered") {
		t.Fatalf("outbox = %v, want exactly one ASNRegistered", types)
	}
}

// TestHTTPIdempotencyKeyReplayAndReuse proves the Idempotency-Key
// middleware contract over the real Postgres store: a retry with the same
// key and body replays the first response without re-running the handler,
// and the same key with a different body is a 422 idempotency-key-reused.
func TestHTTPIdempotencyKeyReplayAndReuse(t *testing.T) {
	api := newHTTPAPI(t)
	first := `{"asnNumber":"ASN-IDEM-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":5}]}`
	second := `{"asnNumber":"ASN-IDEM-2","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-2","expectedQty":7}]}`

	status, header, body := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "idem-key-1", body: first})
	if status != http.StatusCreated {
		t.Fatalf("first POST: status %d body %v", status, body)
	}
	firstLocation := header.Get("Location")

	// Same key, same body: the stored response is replayed verbatim.
	status, header, body = api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "idem-key-1", body: first})
	if status != http.StatusCreated || header.Get("Location") != firstLocation {
		t.Fatalf("replay: status %d location %q body %v", status, header.Get("Location"), body)
	}

	// Same key, different body: 422 idempotency-key-reused.
	status, _, problem := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "idem-key-1", body: second})
	if status != http.StatusUnprocessableEntity || problemSlug(t, problem) != "idempotency-key-reused" {
		t.Fatalf("reuse: status %d problem %v", status, problem)
	}

	// Only the FIRST create ran: one ASN, one outbox row.
	if types := api.outboxTypes(t); len(types) != 1 {
		t.Fatalf("outbox = %v, want exactly one row (the replay must not re-write)", types)
	}
	var count int
	if err := api.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM asns WHERE asn_number LIKE 'ASN-IDEM-%'`).Scan(&count); err != nil {
		t.Fatalf("count asns: %v", err)
	}
	if count != 1 {
		t.Fatalf("asns = %d, want 1", count)
	}
}

// TestHTTPReceiptFlow walks the receipt lifecycle over HTTP: register the
// ASN, open a walk-in receipt (201), receive a line (201, with a stale
// If-Match rejected as 412 first), close (200), and reads reflect every
// step; each write left its event in the transactional outbox.
func TestHTTPReceiptFlow(t *testing.T) {
	api := newHTTPAPI(t)

	status, _, body := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "rcv-asn",
		body: `{"asnNumber":"ASN-RCPT-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}`})
	if status != http.StatusCreated {
		t.Fatalf("register asn: status %d body %v", status, body)
	}

	// Open a walk-in receipt.
	status, header, rcpt := api.do(t, request{method: "POST", path: "/receipts", idempotencyKey: "rcv-open",
		body: `{"asnNumber":"ASN-RCPT-1"}`})
	if status != http.StatusCreated {
		t.Fatalf("open receipt: status %d body %v", status, rcpt)
	}
	receiptID, _ := rcpt["receiptId"].(string)
	if !strings.HasPrefix(receiptID, "rcpt-") || header.Get("ETag") != `"1"` {
		t.Fatalf("opened receipt = %v etag %q", rcpt, header.Get("ETag"))
	}
	linesPath := fmt.Sprintf("/receipts/%s/lines", receiptID)

	// A stale If-Match (the receipt is at version 1, "5" is future) is a
	// 412 version-mismatch problem and nothing is received.
	status, _, problem := api.do(t, request{method: "POST", path: linesPath, idempotencyKey: "rcv-stale",
		ifMatch: `"5"`, body: `{"lineNo":1,"quantity":8,"condition":"Good"}`})
	if status != http.StatusPreconditionFailed || problemSlug(t, problem) != "version-mismatch" {
		t.Fatalf("stale If-Match: status %d problem %v", status, problem)
	}

	// Receive a line without a precondition: 201 and the counts move.
	status, header, line := api.do(t, request{method: "POST", path: linesPath, idempotencyKey: "rcv-line-1",
		body: `{"lineNo":1,"quantity":8,"condition":"Good"}`})
	if status != http.StatusCreated || header.Get("ETag") != `"2"` {
		t.Fatalf("receive line: status %d etag %q body %v", status, header.Get("ETag"), line)
	}

	// An invalid condition is a 400 problem slug.
	status, _, problem = api.do(t, request{method: "POST", path: linesPath, idempotencyKey: "rcv-badcond",
		body: `{"lineNo":1,"quantity":1,"condition":"Lost"}`})
	if status != http.StatusBadRequest || problemSlug(t, problem) != "invalid-condition" {
		t.Fatalf("invalid condition: status %d problem %v", status, problem)
	}

	// Close the receipt: 200, Closed, and the discrepancy (8 of 10 = Short).
	status, _, closed := api.do(t, request{method: "POST",
		path: fmt.Sprintf("/receipts/%s/close", receiptID), idempotencyKey: "rcv-close"})
	if status != http.StatusOK || closed["state"] != "Closed" {
		t.Fatalf("close receipt: status %d body %v", status, closed)
	}
	discrepancies, _ := closed["discrepancies"].([]any)
	if len(discrepancies) != 1 || discrepancies[0].(map[string]any)["kind"] != "Short" {
		t.Fatalf("discrepancies = %v, want one Short", closed["discrepancies"])
	}

	// GET shows the final state with the received counts.
	status, _, got := api.do(t, request{method: "GET", path: fmt.Sprintf("/receipts/%s", receiptID)})
	if status != http.StatusOK || got["state"] != "Closed" {
		t.Fatalf("get receipt: status %d body %v", status, got)
	}
	gotLines, _ := got["lines"].([]any)
	if len(gotLines) != 1 || gotLines[0].(map[string]any)["receivedGood"] != 8.0 {
		t.Fatalf("receipt lines = %v", got["lines"])
	}

	// The whole flow left exactly the expected outbox events, in order.
	types := api.outboxTypes(t)
	wantSubstrings := []string{"ASNRegistered", "ReceiptOpened", "ReceiptLineReceived", "ReceiptClosed"}
	if len(types) != len(wantSubstrings) {
		t.Fatalf("outbox = %v, want the four events %v", types, wantSubstrings)
	}
	for i, want := range wantSubstrings {
		if !strings.Contains(types[i], want) {
			t.Fatalf("outbox event %d = %s, want it to contain %s (all: %v)", i, types[i], want, types)
		}
	}
}
