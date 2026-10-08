package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func linePath(id string) string { return "/receipts/" + id + "/lines" }

func TestOpenWalkInReceipt(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	r := f.post(t, "/receipts", `{"asnNumber":"ASN-1"}`)
	expectStatus(t, r, http.StatusCreated)
	id := str(r, "receiptId")
	lines, _ := r.body["lines"].([]any)
	disc, _ := r.body["discrepancies"].([]any)
	if !strings.HasPrefix(id, "rcpt-") || r.header.Get("Location") != "/receipts/"+id || r.header.Get("ETag") != `"1"` ||
		str(r, "state") != "Open" || len(lines) != 2 || disc == nil || len(disc) != 2 || str(r, "openedAt") != "2026-10-09T07:00:00Z" {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	for _, absent := range []string{"appointmentId", "doorCode", "closedAt"} {
		if _, present := r.body[absent]; present {
			t.Fatalf("%s must be omitted for a walk-in open receipt: %s", absent, r.raw)
		}
	}
	asn := f.get(t, "/asns/ASN-1")
	if str(asn, "state") != "Receiving" {
		t.Fatalf("asn = %s", asn.raw)
	}
}

func TestOpenReceiptFromACheckedInAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	appt := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	f.clock.set(startOfDay.Add(time.Hour))
	f.mustPost(t, "/appointments/"+appt+"/check-in", "", http.StatusOK)
	r := f.post(t, "/receipts", `{"asnNumber":"ASN-1","appointmentId":"`+appt+`"}`)
	expectStatus(t, r, http.StatusCreated)
	if str(r, "appointmentId") != appt || str(r, "doorCode") != "DOOR-1" {
		t.Fatalf("response = %s", r.raw)
	}
}

func TestOpenReceiptRefusals(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.seedAsn(t, "ASN-2")
	appt := f.seedAppointment(t, "DOOR-1", 24, "ASN-2")
	cases := []struct {
		name   string
		body   string
		status int
		slug   string
	}{
		{"not json", `{`, 400, "malformed-request"},
		{"bad asn number", `{"asnNumber":"a b"}`, 400, "invalid-asn-number"},
		{"bad appointment id", `{"asnNumber":"ASN-1","appointmentId":"nope"}`, 400, "invalid-appointment-id"},
		{"unknown asn", `{"asnNumber":"ASN-404"}`, 422, "unknown-asn"},
		{"unknown appointment", `{"asnNumber":"ASN-1","appointmentId":"` + missingAppointment + `"}`, 422, "unknown-appointment"},
		{"asn not on appointment", `{"asnNumber":"ASN-1","appointmentId":"` + appt + `"}`, 422, "asn-not-on-appointment"},
		{"appointment not checked in", `{"asnNumber":"ASN-2","appointmentId":"` + appt + `"}`, 409, "appointment-not-checked-in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, f.post(t, "/receipts", tc.body), tc.status, tc.slug)
		})
	}
}

func TestOneOpenReceiptPerAsnAndNotReceivable(t *testing.T) {
	f := newFixture(t)
	f.seedReceipt(t, "ASN-1")
	expectProblem(t, f.post(t, "/receipts", `{"asnNumber":"ASN-1"}`), http.StatusConflict, "receipt-already-open")

	f.seedAsn(t, "ASN-2")
	f.mustPost(t, "/asns/ASN-2/cancel", "", http.StatusOK)
	expectProblem(t, f.post(t, "/receipts", `{"asnNumber":"ASN-2"}`), http.StatusConflict, "asn-not-receivable")
}

func TestOpenReceiptInternalError(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.faults.receiptSave = errDB
	expectProblem(t, f.post(t, "/receipts", `{"asnNumber":"ASN-1"}`), http.StatusInternalServerError, "internal-error")
}

func TestReceiveLineGoodAndDamagedThenClose(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	good := f.post(t, linePath(id), `{"lineNo":1,"quantity":30,"condition":"Good"}`)
	expectStatus(t, good, http.StatusCreated)
	if good.header.Get("ETag") != `"2"` || good.body["version"] != float64(2) {
		t.Fatalf("response = %v %s", good.header, good.raw)
	}
	expectStatus(t, f.post(t, linePath(id), `{"lineNo":1,"quantity":4,"condition":"Damaged"}`), http.StatusCreated)
	over := f.post(t, linePath(id), `{"lineNo":2,"quantity":7,"condition":"Good"}`)
	expectStatus(t, over, http.StatusCreated)

	closed := f.post(t, "/receipts/"+id+"/close", "")
	expectStatus(t, closed, http.StatusOK)
	disc, _ := closed.body["discrepancies"].([]any)
	if str(closed, "state") != "Closed" || str(closed, "closedAt") == "" || len(disc) != 3 || closed.header.Get("ETag") != `"5"` {
		t.Fatalf("closed = %v %s", closed.header, closed.raw)
	}
	kinds := []string{}
	for _, d := range disc {
		kinds = append(kinds, d.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "Short,Damaged,Over" {
		t.Fatalf("kinds = %v", kinds)
	}
	expectProblem(t, f.post(t, "/receipts/"+id+"/close", ""), http.StatusConflict, "receipt-closed")
	expectProblem(t, f.post(t, linePath(id), `{"lineNo":1,"quantity":1,"condition":"Good"}`), http.StatusConflict, "receipt-closed")
	if asn := f.get(t, "/asns/ASN-1"); str(asn, "state") != "Closed" {
		t.Fatalf("asn = %s", asn.raw)
	}
}

func TestReceiveLineRefusals(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	cases := []struct {
		name   string
		path   string
		body   string
		status int
		slug   string
	}{
		{"not json", linePath(id), `{`, 400, "malformed-request"},
		{"unknown field", linePath(id), `{"lineNo":1,"quantity":1,"condition":"Good","x":1}`, 400, "malformed-request"},
		{"bad condition", linePath(id), `{"lineNo":1,"quantity":1,"condition":"Broken"}`, 400, "invalid-condition"},
		{"zero quantity", linePath(id), `{"lineNo":1,"quantity":0,"condition":"Good"}`, 400, "invalid-quantity"},
		{"bad receipt id", linePath("nope"), `{"lineNo":1,"quantity":1,"condition":"Good"}`, 400, "invalid-receipt-id"},
		{"unknown receipt", linePath(missingReceipt), `{"lineNo":1,"quantity":1,"condition":"Good"}`, 404, "receipt-not-found"},
		{"line not on asn", linePath(id), `{"lineNo":9,"quantity":1,"condition":"Good"}`, 422, "line-not-on-asn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, f.post(t, tc.path, tc.body), tc.status, tc.slug)
		})
	}
}

func TestReceiveLineVersionPreconditionAndInternalError(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	body := `{"lineNo":1,"quantity":1,"condition":"Good"}`
	expectProblem(t, f.postWith(t, linePath(id), body, map[string]string{"If-Match": `"9"`}), http.StatusPreconditionFailed, "version-mismatch")
	expectStatus(t, f.postWith(t, linePath(id), body, map[string]string{"If-Match": `"1"`}), http.StatusCreated)
	f.faults.receiptSave = errDB
	expectProblem(t, f.post(t, linePath(id), body), http.StatusInternalServerError, "internal-error")
}

// TestReceiveLineReplayNeverCountsTwice is the point of the required key on
// this route: a network retry must not double-count units.
func TestReceiveLineReplayNeverCountsTwice(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	headers := map[string]string{"Idempotency-Key": "scan-42"}
	body := `{"lineNo":1,"quantity":40,"condition":"Good"}`
	first := f.request(t, http.MethodPost, linePath(id), body, headers)
	second := f.request(t, http.MethodPost, linePath(id), body, headers)
	expectStatus(t, first, http.StatusCreated)
	if second.status != http.StatusCreated || second.raw != first.raw {
		t.Fatalf("replay = %d %s", second.status, second.raw)
	}
	got := f.get(t, "/receipts/"+id)
	lines, _ := got.body["lines"].([]any)
	if lines[0].(map[string]any)["receivedGood"] != float64(40) {
		t.Fatalf("units were counted twice: %s", got.raw)
	}
	expectProblem(t, f.request(t, http.MethodPost, linePath(id), `{"lineNo":1,"quantity":41,"condition":"Good"}`, headers), http.StatusUnprocessableEntity, "idempotency-key-reused")
}

func TestCloseReceiptRefusals(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	expectProblem(t, f.post(t, "/receipts/nope/close", ""), http.StatusBadRequest, "invalid-receipt-id")
	expectProblem(t, f.post(t, "/receipts/"+missingReceipt+"/close", ""), http.StatusNotFound, "receipt-not-found")
	expectProblem(t, f.postWith(t, "/receipts/"+id+"/close", "", map[string]string{"If-Match": `"9"`}), http.StatusPreconditionFailed, "version-mismatch")
	expectProblem(t, f.postWith(t, "/receipts/"+id+"/close", "", map[string]string{"If-Match": "x"}), http.StatusBadRequest, "malformed-request")
	f.faults.receiptSave = errDB
	expectProblem(t, f.post(t, "/receipts/"+id+"/close", ""), http.StatusInternalServerError, "internal-error")
}

func TestCloseReceiptCompletesTheAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	appt := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	f.clock.set(startOfDay.Add(time.Hour))
	f.mustPost(t, "/appointments/"+appt+"/check-in", "", http.StatusOK)
	rcpt := str(f.mustPost(t, "/receipts", `{"asnNumber":"ASN-1","appointmentId":"`+appt+`"}`, http.StatusCreated), "receiptId")
	f.mustPost(t, "/receipts/"+rcpt+"/close", "", http.StatusOK)
	if got := f.get(t, "/appointments/"+appt); str(got, "state") != "Completed" {
		t.Fatalf("appointment = %s", got.raw)
	}
}

func TestGetReceipt(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-1")
	r := f.get(t, "/receipts/"+id)
	expectStatus(t, r, http.StatusOK)
	if r.header.Get("ETag") != `"1"` || str(r, "receiptId") != id {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.get(t, "/receipts/"+missingReceipt), http.StatusNotFound, "receipt-not-found")
	expectProblem(t, f.get(t, "/receipts/nope"), http.StatusBadRequest, "invalid-receipt-id")
	f.faults.receiptGet = errDB
	expectProblem(t, f.get(t, "/receipts/"+id), http.StatusInternalServerError, "internal-error")
}

func TestListReceiptsPagesAndFilters(t *testing.T) {
	f := newFixture(t)
	f.seedReceipt(t, "ASN-1")
	two := f.seedReceipt(t, "ASN-2")
	f.seedReceipt(t, "ASN-3")
	f.mustPost(t, "/receipts/"+two+"/close", "", http.StatusOK)

	page := f.get(t, "/receipts?limit=2")
	items, _ := page.body["items"].([]any)
	if len(items) != 2 || str(page, "nextCursor") == "" {
		t.Fatalf("page = %s", page.raw)
	}
	next := f.get(t, "/receipts?limit=2&cursor="+str(page, "nextCursor"))
	items, _ = next.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("next = %s", next.raw)
	}
	closed := f.get(t, "/receipts?state=Closed&asnNumber=ASN-2")
	items, _ = closed.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("closed = %s", closed.raw)
	}
}

func TestListReceiptsInvalidQueriesAndErrors(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"limit=x", "limit=501", "cursor=!!", "state=Nope", "asnNumber=a%20b"} {
		expectProblem(t, f.get(t, "/receipts?"+q), http.StatusBadRequest, "invalid-query")
	}
	f.faults.receiptList = errDB
	expectProblem(t, f.get(t, "/receipts"), http.StatusInternalServerError, "internal-error")
}
