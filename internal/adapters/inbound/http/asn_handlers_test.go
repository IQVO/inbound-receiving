package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRegisterAsnCreated(t *testing.T) {
	f := newFixture(t)
	r := f.post(t, "/asns", asnBody("ASN-1001"))
	expectStatus(t, r, http.StatusCreated)
	if r.header.Get("ETag") != `"1"` || r.header.Get("Location") != "/asns/ASN-1001" || r.contentType != "application/json" {
		t.Fatalf("headers = %v", r.header)
	}
	lines, _ := r.body["lines"].([]any)
	if str(r, "asnNumber") != "ASN-1001" || str(r, "state") != "Registered" || r.body["version"] != float64(1) ||
		str(r, "expectedArrival") != "2026-10-10T08:00:00Z" || len(lines) != 2 {
		t.Fatalf("body = %s", r.raw)
	}
	if got := len(f.outbox.Messages()); got != 1 {
		t.Fatalf("outbox = %d messages", got)
	}
}

func TestRegisterAsnWithoutExpectedArrivalOmitsIt(t *testing.T) {
	f := newFixture(t)
	r := f.post(t, "/asns", `{"asnNumber":"A","supplierRef":"S","lines":[{"lineNo":1,"sku":"K","expectedQty":1}]}`)
	expectStatus(t, r, http.StatusCreated)
	if _, present := r.body["expectedArrival"]; present {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestRegisterAsnRejectsInvalidBodies(t *testing.T) {
	line := `"lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":5}]`
	cases := []struct {
		name string
		body string
		slug string
	}{
		{"not json", `{`, "malformed-request"},
		{"unknown field", `{"asnNumber":"A","supplierRef":"S",` + line + `,"extra":1}`, "malformed-request"},
		{"trailing data", `{"asnNumber":"A","supplierRef":"S",` + line + `} {}`, "malformed-request"},
		{"bad arrival", `{"asnNumber":"A","supplierRef":"S","expectedArrival":"tomorrow",` + line + `}`, "malformed-request"},
		{"bad number", `{"asnNumber":"A B","supplierRef":"S",` + line + `}`, "invalid-asn-number"},
		{"blank supplier", `{"asnNumber":"A","supplierRef":" ",` + line + `}`, "invalid-supplier-ref"},
		{"no lines", `{"asnNumber":"A","supplierRef":"S","lines":[]}`, "asn-requires-lines"},
		{"line numbers out of order", `{"asnNumber":"A","supplierRef":"S","lines":[{"lineNo":2,"sku":"K","expectedQty":1}]}`, "invalid-line-no"},
		{"duplicate sku", `{"asnNumber":"A","supplierRef":"S","lines":[{"lineNo":1,"sku":"K","expectedQty":1},{"lineNo":2,"sku":"K","expectedQty":1}]}`, "duplicate-sku"},
		{"bad sku", `{"asnNumber":"A","supplierRef":"S","lines":[{"lineNo":1,"sku":"a/b","expectedQty":1}]}`, "invalid-sku"},
		{"zero quantity", `{"asnNumber":"A","supplierRef":"S","lines":[{"lineNo":1,"sku":"K","expectedQty":0}]}`, "invalid-quantity"},
	}
	f := newFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, f.post(t, "/asns", tc.body), http.StatusBadRequest, tc.slug)
		})
	}
}

func TestRegisterAsnDuplicateNumberConflicts(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	expectProblem(t, f.post(t, "/asns", asnBody("ASN-1")), http.StatusConflict, "asn-already-exists")
}

func TestRegisterAsnUnknownSkuIsUnprocessableOnlyInKafkaMode(t *testing.T) {
	permissive := newFixture(t)
	expectStatus(t, permissive.post(t, "/asns", asnBody("ASN-1")), http.StatusCreated)

	strict := newKafkaModeFixture(t)
	_ = strict.skus.Upsert(context.Background(), "SKU-1")
	r := strict.post(t, "/asns", asnBody("ASN-1"))
	expectProblem(t, r, http.StatusUnprocessableEntity, "unknown-sku")
	if !strings.Contains(str(r, "detail"), "SKU-2") {
		t.Fatalf("detail = %q", str(r, "detail"))
	}
}

func TestRegisterAsnInternalError(t *testing.T) {
	f := newFixture(t)
	f.faults.asnSave = errDB
	r := f.post(t, "/asns", asnBody("ASN-1"))
	expectProblem(t, r, http.StatusInternalServerError, "internal-error")
	if strings.Contains(r.raw, "db down") {
		t.Fatalf("internal detail leaked: %s", r.raw)
	}
}

func TestGetAsn(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	r := f.get(t, "/asns/ASN-1")
	expectStatus(t, r, http.StatusOK)
	if r.header.Get("ETag") != `"1"` || str(r, "asnNumber") != "ASN-1" {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.get(t, "/asns/ASN-404"), http.StatusNotFound, "asn-not-found")
	expectProblem(t, f.get(t, "/asns/bad%20number"), http.StatusBadRequest, "invalid-asn-number")
	f.faults.asnGet = errDB
	expectProblem(t, f.get(t, "/asns/ASN-1"), http.StatusInternalServerError, "internal-error")
}

func TestListAsnsPagesAndFilters(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		f.seedAsn(t, n)
	}
	f.mustPost(t, "/asns/ASN-3/cancel", "", http.StatusOK)

	page := f.get(t, "/asns?limit=2")
	expectStatus(t, page, http.StatusOK)
	items, _ := page.body["items"].([]any)
	if len(items) != 2 || str(page, "nextCursor") == "" {
		t.Fatalf("page = %s", page.raw)
	}
	next := f.get(t, "/asns?limit=2&cursor="+str(page, "nextCursor"))
	items, _ = next.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("next = %s", next.raw)
	}
	if _, present := next.body["nextCursor"]; present {
		t.Fatalf("the last page must not carry nextCursor: %s", next.raw)
	}
	cancelled := f.get(t, "/asns?state=Cancelled")
	items, _ = cancelled.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("cancelled = %s", cancelled.raw)
	}
}

func TestListAsnsInvalidQueriesAndErrors(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"limit=abc", "limit=0", "limit=501", "cursor=!!", "state=Nope"} {
		expectProblem(t, f.get(t, "/asns?"+q), http.StatusBadRequest, "invalid-query")
	}
	f.faults.asnList = errDB
	expectProblem(t, f.get(t, "/asns"), http.StatusInternalServerError, "internal-error")
}

func TestCancelAsn(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	r := f.post(t, "/asns/ASN-1/cancel", `{"reason":"Supplier cancelled"}`)
	expectStatus(t, r, http.StatusOK)
	if str(r, "state") != "Cancelled" || r.header.Get("ETag") != `"2"` || r.body["version"] != float64(2) {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.post(t, "/asns/ASN-1/cancel", ""), http.StatusConflict, "asn-terminal")
}

func TestCancelAsnRefusals(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	expectProblem(t, f.post(t, "/asns/ASN-404/cancel", ""), http.StatusNotFound, "asn-not-found")
	expectProblem(t, f.post(t, "/asns/bad%20number/cancel", ""), http.StatusBadRequest, "invalid-asn-number")
	expectProblem(t, f.post(t, "/asns/ASN-1/cancel", `{"nope":1}`), http.StatusBadRequest, "malformed-request")
	expectProblem(t, f.post(t, "/asns/ASN-1/cancel", `{"reason":"`+strings.Repeat("x", 201)+`"}`), http.StatusBadRequest, "invalid-reason")
	expectProblem(t, f.postWith(t, "/asns/ASN-1/cancel", "", map[string]string{"If-Match": `"7"`}), http.StatusPreconditionFailed, "version-mismatch")
	expectProblem(t, f.postWith(t, "/asns/ASN-1/cancel", "", map[string]string{"If-Match": "abc"}), http.StatusBadRequest, "malformed-request")
	f.faults.asnSave = errDB
	expectProblem(t, f.post(t, "/asns/ASN-1/cancel", ""), http.StatusInternalServerError, "internal-error")
}

func TestCancelAsnWithMatchingIfMatchAndWildcard(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.seedAsn(t, "ASN-2")
	expectStatus(t, f.postWith(t, "/asns/ASN-1/cancel", "", map[string]string{"If-Match": `"1"`}), http.StatusOK)
	expectStatus(t, f.postWith(t, "/asns/ASN-2/cancel", "", map[string]string{"If-Match": "*"}), http.StatusOK)
}

func TestCancelAsnInProgressConflicts(t *testing.T) {
	f := newFixture(t)
	f.seedReceipt(t, "ASN-1")
	expectProblem(t, f.post(t, "/asns/ASN-1/cancel", ""), http.StatusConflict, "asn-in-progress")
}

func TestCreatingPostsRequireAnIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	for _, path := range []string{"/asns", "/appointments", "/receipts", "/receipts/" + missingReceipt + "/lines"} {
		r := f.request(t, http.MethodPost, path, "{}", nil)
		expectProblem(t, r, http.StatusBadRequest, "idempotency-key-required")
	}
	long := strings.Repeat("k", 256)
	expectProblem(t, f.request(t, http.MethodPost, "/asns", asnBody("ASN-2"), map[string]string{"Idempotency-Key": long}), http.StatusBadRequest, "malformed-request")
}

func TestActionPostsDoNotRequireAnIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	expectStatus(t, f.request(t, http.MethodPost, "/asns/ASN-1/cancel", "", nil), http.StatusOK)
}

func TestIdempotentReplayReturnsTheFirstResponse(t *testing.T) {
	f := newFixture(t)
	headers := map[string]string{"Idempotency-Key": "same-key"}
	first := f.request(t, http.MethodPost, "/asns", asnBody("ASN-1"), headers)
	second := f.request(t, http.MethodPost, "/asns", asnBody("ASN-1"), headers)
	expectStatus(t, first, http.StatusCreated)
	if second.status != http.StatusCreated || second.raw != first.raw || second.header.Get("ETag") != `"1"` || second.header.Get("Location") != "/asns/ASN-1" {
		t.Fatalf("replay = %d %v %s", second.status, second.header, second.raw)
	}
	if got := len(f.outbox.Messages()); got != 1 {
		t.Fatalf("a replay must not publish again: %d messages", got)
	}
}

func TestIdempotentKeyReuseWithAnotherBodyOrRouteIsUnprocessable(t *testing.T) {
	f := newFixture(t)
	headers := map[string]string{"Idempotency-Key": "same-key"}
	expectStatus(t, f.request(t, http.MethodPost, "/asns", asnBody("ASN-1"), headers), http.StatusCreated)
	expectProblem(t, f.request(t, http.MethodPost, "/asns", asnBody("ASN-2"), headers), http.StatusUnprocessableEntity, "idempotency-key-reused")
	expectProblem(t, f.request(t, http.MethodPost, "/asns/ASN-1/cancel", asnBody("ASN-1"), headers), http.StatusUnprocessableEntity, "idempotency-key-reused")
}

func TestIdempotentRefusalsAreReplayedToo(t *testing.T) {
	f := newFixture(t)
	headers := map[string]string{"Idempotency-Key": "k"}
	first := f.request(t, http.MethodPost, "/asns", `{"asnNumber":"A B"}`, headers)
	second := f.request(t, http.MethodPost, "/asns", `{"asnNumber":"A B"}`, headers)
	expectProblem(t, first, http.StatusBadRequest, "invalid-asn-number")
	if second.status != first.status || second.raw != first.raw {
		t.Fatalf("replayed refusal = %d %s", second.status, second.raw)
	}
}

func TestIdempotentServerFailuresAreNotStored(t *testing.T) {
	f := newFixture(t)
	headers := map[string]string{"Idempotency-Key": "retry-me"}
	f.faults.asnSave = errDB
	expectProblem(t, f.request(t, http.MethodPost, "/asns", asnBody("ASN-1"), headers), http.StatusInternalServerError, "internal-error")
	f.faults.asnSave = nil
	expectStatus(t, f.request(t, http.MethodPost, "/asns", asnBody("ASN-1"), headers), http.StatusCreated)
}

func TestIdempotencyStoreFailureIsInternalError(t *testing.T) {
	f := newFixture(t)
	f.faults.idempotency = errDB
	expectProblem(t, f.post(t, "/asns", asnBody("ASN-1")), http.StatusInternalServerError, "internal-error")
}
