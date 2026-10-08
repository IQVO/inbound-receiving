package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
)

func TestBookAppointmentCreated(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	r := f.post(t, "/appointments", bookBody("DOOR-1", 24, "ASN-1"))
	expectStatus(t, r, http.StatusCreated)
	id := str(r, "appointmentId")
	asns, _ := r.body["asnNumbers"].([]any)
	if !strings.HasPrefix(id, "appt-") || r.header.Get("Location") != "/appointments/"+id || r.header.Get("ETag") != `"1"` ||
		str(r, "state") != "Booked" || str(r, "doorCode") != "DOOR-1" || str(r, "windowStart") != "2026-10-10T07:00:00Z" ||
		str(r, "windowEnd") != "2026-10-10T09:00:00Z" || len(asns) != 1 || r.body["version"] != float64(1) {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
}

func TestBookAppointmentRejectsInvalidBodies(t *testing.T) {
	good := bookBody("DOOR-1", 24, "ASN-1")
	start := startOfDay.Add(24 * time.Hour)
	body := func(door, carrier string, s, e time.Time, asns string) string {
		return fmt.Sprintf(`{"doorCode":%q,"carrier":%q,"windowStart":%q,"windowEnd":%q,"asnNumbers":%s}`,
			door, carrier, s.Format(time.RFC3339), e.Format(time.RFC3339), asns)
	}
	cases := []struct {
		name, body, slug string
	}{
		{"not json", `[`, "malformed-request"},
		{"unknown field", strings.Replace(good, `"carrier"`, `"extra":1,"carrier"`, 1), "malformed-request"},
		{"bad instant", strings.Replace(good, `"windowStart":"`, `"windowStart":"x`, 1), "malformed-request"},
		{"bad door", body("a b", "C", start, start.Add(time.Hour), `["ASN-1"]`), "invalid-door-code"},
		{"blank carrier", body("D", " ", start, start.Add(time.Hour), `["ASN-1"]`), "invalid-carrier"},
		{"window too long", body("D", "C", start, start.Add(5*time.Hour), `["ASN-1"]`), "invalid-window"},
		{"window ends before it starts", body("D", "C", start, start.Add(-time.Hour), `["ASN-1"]`), "invalid-window"},
		{"window in the past", body("D", "C", startOfDay.Add(-3*time.Hour), startOfDay.Add(-2*time.Hour), `["ASN-1"]`), "window-in-past"},
		{"no asns", body("D", "C", start, start.Add(time.Hour), `[]`), "appointment-requires-asns"},
		{"duplicate asn", body("D", "C", start, start.Add(time.Hour), `["ASN-1","ASN-1"]`), "duplicate-asn-number"},
		{"bad asn number", body("D", "C", start, start.Add(time.Hour), `["a b"]`), "invalid-asn-number"},
	}
	f := newFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblem(t, f.post(t, "/appointments", tc.body), http.StatusBadRequest, tc.slug)
		})
	}
}

func TestBookAppointmentOverlapAndUnreceivableAsn(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.seedAsn(t, "ASN-2")
	f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	expectProblem(t, f.post(t, "/appointments", bookBody("DOOR-1", 25, "ASN-2")), http.StatusConflict, "door-window-overlap")
	expectStatus(t, f.post(t, "/appointments", bookBody("DOOR-2", 25, "ASN-2")), http.StatusCreated)

	f.mustPost(t, "/asns/ASN-1/cancel", "", http.StatusOK)
	expectProblem(t, f.post(t, "/appointments", bookBody("DOOR-3", 48, "ASN-1")), http.StatusConflict, "asn-not-receivable")
}

func TestBookAppointmentUnprocessable(t *testing.T) {
	f := newFixture(t)
	expectProblem(t, f.post(t, "/appointments", bookBody("DOOR-1", 24, "ASN-404")), http.StatusUnprocessableEntity, "unknown-asn")

	strict := newKafkaModeFixture(t)
	_ = strict.skus.Upsert(t.Context(), "SKU-1")
	_ = strict.skus.Upsert(t.Context(), "SKU-2")
	strict.seedAsn(t, "ASN-1")
	expectProblem(t, strict.post(t, "/appointments", bookBody("WH1-STOR-01", 24, "ASN-1")), http.StatusUnprocessableEntity, "unknown-dock-door")
	_ = strict.doors.Upsert(t.Context(), repository.DockDoor{Code: "WH1-DOCK-IN-01", Flow: repository.DockFlowInbound})
	expectStatus(t, strict.post(t, "/appointments", bookBody("WH1-DOCK-IN-01", 24, "ASN-1")), http.StatusCreated)
}

func TestBookAppointmentInternalError(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	f.faults.apptSave = errDB
	expectProblem(t, f.post(t, "/appointments", bookBody("DOOR-1", 24, "ASN-1")), http.StatusInternalServerError, "internal-error")
}

func TestGetAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	r := f.get(t, "/appointments/"+id)
	expectStatus(t, r, http.StatusOK)
	if r.header.Get("ETag") != `"1"` || str(r, "appointmentId") != id {
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
	for _, q := range []string{"limit=x", "limit=501", "cursor=!!", "state=Nope", "door=a%20b", "from=yesterday", "to=tomorrow"} {
		expectProblem(t, f.get(t, "/appointments?"+q), http.StatusBadRequest, "invalid-query")
	}
	f.faults.apptList = errDB
	expectProblem(t, f.get(t, "/appointments"), http.StatusInternalServerError, "internal-error")
}

func TestCheckInAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	path := "/appointments/" + id + "/check-in"

	// at 07:00 the window opens at 08:00: inside the 30-minute lead? no.
	expectProblem(t, f.post(t, path, ""), http.StatusConflict, "outside-check-in-window")
	f.clock.set(startOfDay.Add(time.Hour))
	r := f.postWith(t, path, "", map[string]string{"If-Match": `"1"`})
	expectStatus(t, r, http.StatusOK)
	if str(r, "state") != "CheckedIn" || r.header.Get("ETag") != `"2"` {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.post(t, path, ""), http.StatusConflict, "appointment-not-booked")
}

func TestAppointmentActionRefusals(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 1, "ASN-1")
	for _, action := range []string{"check-in", "cancel"} {
		path := "/appointments/" + id + "/" + action
		expectProblem(t, f.post(t, "/appointments/"+missingAppointment+"/"+action, ""), http.StatusNotFound, "appointment-not-found")
		expectProblem(t, f.post(t, "/appointments/nope/"+action, ""), http.StatusBadRequest, "invalid-appointment-id")
		expectProblem(t, f.postWith(t, path, "", map[string]string{"If-Match": `"9"`}), http.StatusPreconditionFailed, "version-mismatch")
		expectProblem(t, f.postWith(t, path, "", map[string]string{"If-Match": "x"}), http.StatusBadRequest, "malformed-request")
		expectProblem(t, f.post(t, path, `{"unknown":1}`), http.StatusBadRequest, "malformed-request")
	}
	f.faults.apptSave = errDB
	f.clock.set(startOfDay.Add(time.Hour))
	expectProblem(t, f.post(t, "/appointments/"+id+"/check-in", ""), http.StatusInternalServerError, "internal-error")
	expectProblem(t, f.post(t, "/appointments/"+id+"/cancel", ""), http.StatusInternalServerError, "internal-error")
}

func TestCancelAppointment(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	id := f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	path := "/appointments/" + id + "/cancel"
	expectProblem(t, f.post(t, path, `{"reason":"`+strings.Repeat("x", 201)+`"}`), http.StatusBadRequest, "invalid-reason")
	r := f.post(t, path, `{"reason":"Carrier delayed"}`)
	expectStatus(t, r, http.StatusOK)
	if str(r, "state") != "Cancelled" || r.header.Get("ETag") != `"2"` {
		t.Fatalf("response = %v %s", r.header, r.raw)
	}
	expectProblem(t, f.post(t, path, ""), http.StatusConflict, "appointment-not-booked")
}
