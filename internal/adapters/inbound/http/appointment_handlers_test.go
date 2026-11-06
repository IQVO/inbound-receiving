package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The three former write routes answer 410 appointment-moved (ADR 0019).
func TestAppointmentWritesAreGone(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	cases := []struct {
		name, path, body, detail string
	}{
		{"book", "/appointments", `{"doorCode":"DOOR-1","carrier":"ACME","windowStart":"2026-10-10T07:00:00Z","windowEnd":"2026-10-10T09:00:00Z","asnNumbers":["ASN-1"]}`,
			"dock-scheduling: POST /appointments"},
		{"check in", "/appointments/" + id + "/check-ins", "", "yard-management (POST /visits"},
		{"cancel", "/appointments/" + id + "/cancellations", `{"reason":"x"}`, "dock-scheduling: POST /appointments/{appointmentId}/cancellations"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := f.post(t, tc.path, tc.body)
			expectProblem(t, r, http.StatusGone, "appointment-moved")
			if detail, _ := r.body["detail"].(string); !strings.Contains(detail, tc.detail) {
				t.Fatalf("detail = %q, want it to name %q", detail, tc.detail)
			}
		})
	}
	// Nothing changed and nothing was announced.
	if got := f.get(t, "/appointments/"+id); str(got, "state") != "Booked" || got.header.Get("ETag") != `"1"` {
		t.Fatalf("appointment = %v %s", got.header, got.raw)
	}
	if n := len(f.outbox.Messages()); n != 1 { // the ASN registration only
		t.Fatalf("outbox holds %d messages, want 1", n)
	}
}

// A moved write answers 410 whatever the request: no Idempotency-Key, no
// body, an unknown or malformed id, a body that is not JSON.
func TestAppointmentWritesAreGoneWhateverTheRequest(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/appointments", "/appointments/nope/check-ins", "/appointments/" + missingAppointment + "/cancellations",
	} {
		for _, body := range []string{"", "{", `{"unknown":1}`} {
			expectProblem(t, f.request(t, http.MethodPost, path, body, nil), http.StatusGone, "appointment-moved")
		}
	}
}

func TestGetAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	r := f.get(t, "/appointments/"+id)
	expectStatus(t, r, http.StatusOK)
	if r.header.Get("ETag") != `"1"` || str(r, "appointmentId") != id || str(r, "state") != "Booked" || str(r, "doorCode") != "DOOR-1" ||
		str(r, "windowStart") != "2026-10-10T07:00:00Z" || str(r, "windowEnd") != "2026-10-10T09:00:00Z" || r.body["version"] != float64(1) {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.get(t, "/appointments/"+missingAppointment), http.StatusNotFound, "appointment-not-found")
	expectProblem(t, f.get(t, "/appointments/nope"), http.StatusBadRequest, "invalid-appointment-id")
	f.faults.apptGet = errDB
	expectProblem(t, f.get(t, "/appointments/"+id), http.StatusInternalServerError, "internal-error")
}

func TestListAppointmentsPagesAndFilters(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	f.seedAppointment(t, "DOOR-2", 24, "ASN-1")
	f.seedAppointment(t, "DOOR-1", 30, "ASN-1")

	page := f.get(t, "/appointments?limit=2")
	items, _ := page.body["items"].([]any)
	if len(items) != 2 || str(page, "nextCursor") == "" {
		t.Fatalf("page = %s", page.raw)
	}
	next := f.get(t, "/appointments?limit=2&cursor="+str(page, "nextCursor"))
	items, _ = next.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("next = %s", next.raw)
	}
	from := startOfDay.Add(23 * time.Hour).Format(time.RFC3339)
	to := startOfDay.Add(27 * time.Hour).Format(time.RFC3339)
	window := f.get(t, "/appointments?door=DOOR-1&state=Booked&from="+from+"&to="+to)
	items, _ = window.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("window = %s", window.raw)
	}
}

func TestListAppointmentsInvalidQueriesAndErrors(t *testing.T) {
	f := newFixture(t)
	for q, slug := range map[string]string{"limit=x": "invalid-limit", "limit=501": "invalid-limit", "cursor=!!": "invalid-cursor", "state=Nope": "invalid-query", "door=a%20b": "invalid-query", "from=yesterday": "invalid-query", "to=tomorrow": "invalid-query", "after=x": "unknown-query-parameter", "offset=1": "unknown-query-parameter", "nextToken=x": "unknown-query-parameter"} {
		expectProblem(t, f.get(t, "/appointments?"+q), http.StatusBadRequest, slug)
	}
	f.faults.apptList = errDB
	expectProblem(t, f.get(t, "/appointments"), http.StatusInternalServerError, "internal-error")
}
