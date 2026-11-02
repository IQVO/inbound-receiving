package http_test

import (
	"net/http"
	"testing"
)

// TestRetiredRoutesAreGone pins the REST guideline cutover: the verb routes
// were replaced by transition resources and must not answer any more.
func TestRetiredRoutesAreGone(t *testing.T) {
	f := newFixture(t)
	f.seedAsn(t, "ASN-1")
	appt := f.seedAppointment(t, "DOOR-1", 24, "ASN-1")
	rcpt := "/receipts/" + missingReceipt
	for _, path := range []string{
		"/asns/ASN-1/cancel",
		"/appointments/" + appt + "/check-in",
		"/appointments/" + appt + "/cancel",
		rcpt + "/close",
	} {
		r := f.post(t, path, "")
		if r.status != http.StatusNotFound && r.status != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 404 or 405", path, r.status)
		}
	}
}
