//go:build integration

// Replay classes over the REAL stack (the chi router, the Idempotency-Key
// middleware over the Postgres store, the real use cases and repositories):
// a RETRYABLE refusal stores nothing, so its key is released and the retry
// executes again (docs/adr/0027). Postgres comes from testcontainers, never
// an external DATABASE_URL, never t.Skip.
package http_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// keyRows counts the idempotency rows stored under key, from the pool (a
// connection of its own, so only committed rows count).
func (a *httpAPI) keyRows(t *testing.T, key string) int {
	t.Helper()
	var n int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("count idempotency rows of %q: %v", key, err)
	}
	return n
}

// receiptGate makes the first two receipt loads of an armed gate wait for
// each other, so two requests read the SAME version before either saves: a
// genuine optimistic-lock race, deterministic in outcome (one winner, one
// 409 concurrent-modification).
type receiptGate struct {
	ports.ReceiptRepository
	armed   atomic.Bool
	arrived atomic.Int32
	release chan struct{}
	once    sync.Once
}

func newReceiptGate() *receiptGate { return &receiptGate{release: make(chan struct{})} }

func (g *receiptGate) Get(ctx context.Context, id receipt.ID) (*receipt.Receipt, error) {
	r, err := g.ReceiptRepository.Get(ctx, id)
	if err != nil || !g.armed.Load() {
		return r, err
	}
	if g.arrived.Add(1) == 2 {
		g.once.Do(func() { close(g.release) })
	}
	select {
	case <-g.release:
	case <-time.After(20 * time.Second):
	}
	return r, nil
}

type answer struct {
	status int
	header http.Header
	slug   string
	err    error
}

// post sends one keyed POST without touching t, so it can run in a goroutine.
func (a *httpAPI) post(path, key, body string) answer {
	req, err := http.NewRequest(http.MethodPost, a.server.URL+path, strings.NewReader(body))
	if err != nil {
		return answer{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := a.server.Client().Do(req)
	if err != nil {
		return answer{err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	slug := ""
	if i := strings.Index(string(raw), `"type":"`); i >= 0 {
		rest := string(raw)[i+len(`"type":"`):]
		slug = strings.TrimPrefix(rest[:strings.Index(rest, `"`)], "https://errors.inbound-receiving.warehouse-systems.dev/")
	}
	return answer{status: resp.StatusCode, header: resp.Header, slug: slug}
}

func openReceiptOn(t *testing.T, api *httpAPI, asnNumber string) string {
	t.Helper()
	status, _, body := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "reg-" + asnNumber,
		body: fmt.Sprintf(`{"asnNumber":%q,"supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}`, asnNumber)})
	if status != http.StatusCreated {
		t.Fatalf("register asn: %d %v", status, body)
	}
	status, _, rcpt := api.do(t, request{method: "POST", path: "/receipts", idempotencyKey: "open-" + asnNumber,
		body: fmt.Sprintf(`{"asnNumber":%q}`, asnNumber)})
	if status != http.StatusCreated {
		t.Fatalf("open receipt: %d %v", status, rcpt)
	}
	id, _ := rcpt["receiptId"].(string)
	return id
}

// TestHTTPConcurrentModificationReleasesTheKeySoTheRetryRuns: two requests
// read the same receipt version; the loser answers 409 concurrent-modification
// (RETRYABLE). Its key row must be ABSENT, its retry under the SAME key must
// execute (201, not a replayed 409) and only then be stored and replayed.
func TestHTTPConcurrentModificationReleasesTheKeySoTheRetryRuns(t *testing.T) {
	gate := newReceiptGate()
	api := newHTTPAPIWith(t, func(r ports.ReceiptRepository) ports.ReceiptRepository {
		gate.ReceiptRepository = r
		return gate
	})
	receiptID := openReceiptOn(t, api, "ASN-RACE-1")
	path := fmt.Sprintf("/receipts/%s/lines", receiptID)
	bodies := map[string]string{
		"race-a": `{"lineNo":1,"quantity":1,"condition":"Good"}`,
		"race-b": `{"lineNo":1,"quantity":2,"condition":"Good"}`,
	}

	gate.armed.Store(true)
	results := make(map[string]answer, len(bodies))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for key, body := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := api.post(path, key, body)
			mu.Lock()
			results[key] = got
			mu.Unlock()
		}()
	}
	wg.Wait()
	gate.armed.Store(false)

	var winner, loser string
	for key, got := range results {
		if got.err != nil {
			t.Fatalf("request %s: %v", key, got.err)
		}
		switch got.status {
		case http.StatusCreated:
			winner = key
		case http.StatusConflict:
			if got.slug != "concurrent-modification" {
				t.Fatalf("request %s: 409 with slug %q, want concurrent-modification", key, got.slug)
			}
			loser = key
		default:
			t.Fatalf("request %s: unexpected status %d", key, got.status)
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("want one winner and one loser, got %+v", results)
	}
	if got := api.keyRows(t, winner); got != 1 {
		t.Errorf("winner's key rows = %d, want 1: a 2xx is stored", got)
	}
	if got := api.keyRows(t, loser); got != 0 {
		t.Fatalf("loser's key rows = %d, want 0: a RETRYABLE refusal releases its key", got)
	}

	retry := api.post(path, loser, bodies[loser])
	if retry.err != nil || retry.status != http.StatusCreated {
		t.Fatalf("retry under the released key = %+v, want a fresh 201", retry)
	}
	if retry.header.Get("Idempotency-Replayed") != "" {
		t.Errorf("the retry carries Idempotency-Replayed: it must have executed")
	}
	if got := api.keyRows(t, loser); got != 1 {
		t.Errorf("key rows after the retry succeeded = %d, want 1", got)
	}
	again := api.post(path, loser, bodies[loser])
	if again.status != http.StatusCreated || again.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("third request = %d replayed=%q, want the stored 201 replayed", again.status, again.header.Get("Idempotency-Replayed"))
	}
	if got := countOutbox(t, api, "ReceiptLineReceived"); got != 2 {
		t.Errorf("ReceiptLineReceived events = %d, want 2 (one per accepted request, none for the replay)", got)
	}
	_, _, rcpt := api.do(t, request{method: "GET", path: "/receipts/" + receiptID})
	lines, _ := rcpt["lines"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["receivedGood"] != 3.0 {
		t.Errorf("receipt lines = %v, want receivedGood 3", rcpt["lines"])
	}
}

func countOutbox(t *testing.T, api *httpAPI, kind string) int {
	t.Helper()
	n := 0
	for _, ty := range api.outboxTypes(t) {
		if strings.Contains(ty, kind) {
			n++
		}
	}
	return n
}

// TestHTTPUnknownAsnIsNotStoredSoTheKeyWorksOnceTheAsnArrives: opening a
// receipt for an ASN that is not registered yet is 422 unknown-asn
// (RETRYABLE: another request delivers the fact). The key is released; after
// the ASN is registered the SAME request under the SAME key succeeds.
func TestHTTPUnknownAsnIsNotStoredSoTheKeyWorksOnceTheAsnArrives(t *testing.T) {
	api := newHTTPAPI(t)
	body := `{"asnNumber":"ASN-LATE-1"}`

	refused := api.post("/receipts", "late-asn", body)
	if refused.err != nil || refused.status != http.StatusUnprocessableEntity || refused.slug != "unknown-asn" {
		t.Fatalf("open before the asn exists = %+v, want 422 unknown-asn", refused)
	}
	if got := api.keyRows(t, "late-asn"); got != 0 {
		t.Fatalf("key rows after unknown-asn = %d, want 0: the key is released", got)
	}

	status, _, reg := api.do(t, request{method: "POST", path: "/asns", idempotencyKey: "late-asn-register",
		body: `{"asnNumber":"ASN-LATE-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":4}]}`})
	if status != http.StatusCreated {
		t.Fatalf("register asn: %d %v", status, reg)
	}

	retry := api.post("/receipts", "late-asn", body)
	if retry.err != nil || retry.status != http.StatusCreated || retry.header.Get("Idempotency-Replayed") != "" {
		t.Fatalf("retry after the asn arrived = %+v, want a fresh 201", retry)
	}
	again := api.post("/receipts", "late-asn", body)
	if again.status != http.StatusCreated || again.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("third request = %d replayed=%q, want the stored 201 replayed", again.status, again.header.Get("Idempotency-Replayed"))
	}
	if got := countOutbox(t, api, "ReceiptOpened"); got != 1 {
		t.Errorf("ReceiptOpened events = %d, want 1", got)
	}
}

// TestHTTPFinalRefusalIsStillStoredAndReplayed pins the other side: a
// validation refusal is a function of the request alone (FINAL), so it stays
// stored and replayed byte for byte.
func TestHTTPFinalRefusalIsStillStoredAndReplayed(t *testing.T) {
	api := newHTTPAPI(t)
	body := `{"asnNumber":"A B","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}`

	first := api.post("/asns", "final-key", body)
	if first.err != nil || first.status != http.StatusBadRequest || first.slug != "invalid-asn-number" {
		t.Fatalf("first = %+v, want 400 invalid-asn-number", first)
	}
	if got := api.keyRows(t, "final-key"); got != 1 {
		t.Fatalf("key rows after a FINAL refusal = %d, want 1", got)
	}
	again := api.post("/asns", "final-key", body)
	if again.status != http.StatusBadRequest || again.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay = %d replayed=%q, want the stored 400 replayed", again.status, again.header.Get("Idempotency-Replayed"))
	}
}
