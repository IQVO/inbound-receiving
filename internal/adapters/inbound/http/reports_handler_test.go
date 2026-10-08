package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

var reportsNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func reportsGet(t *testing.T, s *ReportsServer, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewReportsRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func seededReportsStore(t *testing.T) *analyticsstore.Memory {
	t.Helper()
	m := analyticsstore.NewMemory()
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	arrival := func(s string) *time.Time { v := at(s); return &v }
	rcpt := func(k report.Kind, id, rid, asn, when string) report.Event {
		return report.Event{Kind: k, EventID: id, At: at(when), ReceiptID: rid, AsnNumber: asn}
	}
	line := rcpt(report.KindReceiptLineReceived, "l1", "R-1", "ASN-0", "2026-10-06T08:30:00Z")
	line.Line = &report.LineReceived{LineNo: 1, SKU: "S", Quantity: 10, Condition: report.ConditionGood}
	damaged := rcpt(report.KindReceiptLineReceived, "l2", "R-1", "ASN-0", "2026-10-06T08:40:00Z")
	damaged.Line = &report.LineReceived{LineNo: 1, SKU: "S", Quantity: 2, Condition: report.ConditionDamaged}
	closed := rcpt(report.KindReceiptClosed, "c1", "R-1", "ASN-0", "2026-10-06T10:00:00Z")
	closed.Discrepancies = []report.Discrepancy{{LineNo: 1, SKU: "S", Kind: report.DiscrepancyShort}}
	events := []report.Event{
		{Kind: report.KindAsnRegistered, EventID: "a1", At: at("2026-10-01T08:00:00Z"), AsnNumber: "ASN-1", ExpectedArrival: arrival("2026-10-07T08:00:00Z")},
		{Kind: report.KindAsnRegistered, EventID: "a2", At: at("2026-10-01T08:00:00Z"), AsnNumber: "ASN-2", ExpectedArrival: arrival("2026-10-07T09:00:00Z")},
		{Kind: report.KindAsnCancelled, EventID: "a3", At: at("2026-10-02T08:00:00Z"), AsnNumber: "ASN-2"},
		rcpt(report.KindReceiptOpened, "o1", "R-1", "ASN-0", "2026-10-06T08:00:00Z"), line, damaged, closed,
		rcpt(report.KindReceiptOpened, "o2", "R-2", "ASN-1", "2026-10-07T08:05:00Z"),
		{Kind: report.KindAppointmentBooked, EventID: "p1", At: at("2026-10-05T12:00:00Z"), AppointmentID: "AP-1"},
		{Kind: report.KindAppointmentCheckedIn, EventID: "p2", At: at("2026-10-06T07:00:00Z"), AppointmentID: "AP-1"},
		{Kind: report.KindAppointmentCompleted, EventID: "p3", At: at("2026-10-06T10:00:00Z"), AppointmentID: "AP-1"},
	}
	for _, e := range events {
		if _, err := m.Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestReports_ReceivingPerformance(t *testing.T) {
	s := &ReportsServer{Reader: seededReportsStore(t), Now: func() time.Time { return reportsNow }}
	rec := reportsGet(t, s, "/reports/receiving-performance?from=2026-10-06T00:00:00Z&to=2026-10-07T12:00:00Z")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d %s: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	want := `{"from":"2026-10-06T00:00:00Z","to":"2026-10-07T12:00:00Z","days":[` +
		`{"day":"2026-10-06","receipts_closed":1,"lines_received":2,"units_good":10,"units_damaged":2,` +
		`"discrepancies":{"short":1,"over":0,"damaged":0},` +
		`"receipt_cycle":{"count":1,"p50_seconds":7200,"p95_seconds":7200},` +
		`"appointments":{"booked":0,"checked_in":1,"cancelled":0,"completed":1},` +
		`"dock_dwell":{"count":1,"p50_seconds":10800,"p95_seconds":10800}},` +
		`{"day":"2026-10-07","receipts_closed":0,"lines_received":0,"units_good":0,"units_damaged":0,` +
		`"discrepancies":{"short":0,"over":0,"damaged":0},` +
		`"receipt_cycle":{"count":0,"p50_seconds":null,"p95_seconds":null},` +
		`"appointments":{"booked":0,"checked_in":0,"cancelled":0,"completed":0},` +
		`"dock_dwell":{"count":0,"p50_seconds":null,"p95_seconds":null}}],` +
		`"current":{"open_receipts":1,"expected_today":1,"expected_today_started":1}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body:\n got %s\nwant %s", rec.Body, want)
	}
}

func TestReports_DefaultRangeIsTheThirtyDaysEndingNow(t *testing.T) {
	s := &ReportsServer{Reader: analyticsstore.NewMemory(), Now: func() time.Time { return reportsNow }}
	rec := reportsGet(t, s, "/reports/receiving-performance")
	var body struct {
		From, To time.Time
		Days     []json.RawMessage
		Current  map[string]int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, %v: %s", rec.Code, err, rec.Body)
	}
	if !body.To.Equal(reportsNow) || !body.From.Equal(reportsNow.Add(-30*24*time.Hour)) || len(body.Days) != 31 {
		t.Fatalf("range %v..%v with %d days, want 30 days ending now over 31 calendar days", body.From, body.To, len(body.Days))
	}
	if body.Current["open_receipts"] != 0 || body.Current["expected_today"] != 0 {
		t.Fatalf("an empty projection must report nothing current: %v", body.Current)
	}
}

func TestReports_InvalidRangesAreProblem400s(t *testing.T) {
	s := &ReportsServer{Reader: analyticsstore.NewMemory(), Now: func() time.Time { return reportsNow }}
	for _, q := range []string{"from=yesterday", "to=2026-10-07", "from=2026-10-07T00:00:00Z&to=2026-10-06T00:00:00Z", "from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		rec := reportsGet(t, s, "/reports/receiving-performance?"+q)
		var p problemDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/problem+json" ||
			p.Type != problemBaseURI+"invalid-report-range" || p.Status != 400 || p.Instance != "/reports/receiving-performance" || p.Detail == "" {
			t.Errorf("%s: %d %+v", q, rec.Code, p)
		}
	}
}

// failingReader fails the method named in fail.
type failingReader struct {
	report.Reader
	fail string
}

var errStore = errors.New("pq: password authentication failed for user reports at 10.0.0.1")

func (f failingReader) PerformanceDays(ctx context.Context, r report.Range) ([]report.PerformanceDay, error) {
	if f.fail == "days" {
		return nil, errStore
	}
	return f.Reader.PerformanceDays(ctx, r)
}

func (f failingReader) Current(ctx context.Context, today report.DayWindow) (report.CurrentCounts, error) {
	if f.fail == "current" {
		return report.CurrentCounts{}, errStore
	}
	return f.Reader.Current(ctx, today)
}

func (f failingReader) LastEventAt(ctx context.Context) (*time.Time, error) {
	if f.fail == "last" {
		return nil, errStore
	}
	return f.Reader.LastEventAt(ctx)
}

func TestReports_StoreFailuresAre500sThatDoNotLeakTheCause(t *testing.T) {
	for fail, path := range map[string]string{"days": "/reports/receiving-performance", "current": "/reports/receiving-performance", "last": "/reports/freshness"} {
		s := &ReportsServer{Reader: failingReader{Reader: analyticsstore.NewMemory(), fail: fail}, Now: func() time.Time { return reportsNow }}
		rec := reportsGet(t, s, path)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "report-store-error") || strings.Contains(rec.Body.String(), "password") {
			t.Errorf("%s: %d %s", fail, rec.Code, rec.Body)
		}
	}
}

func TestReports_Freshness(t *testing.T) {
	empty := &ReportsServer{Reader: analyticsstore.NewMemory(), Now: func() time.Time { return reportsNow }}
	if rec := reportsGet(t, empty, "/reports/freshness"); rec.Code != 200 || rec.Body.String() != `{"as_of":null,"lag_seconds":null}`+"\n" {
		t.Fatalf("empty freshness = %d %s", rec.Code, rec.Body)
	}
	s := &ReportsServer{Reader: seededReportsStore(t), Now: func() time.Time { return time.Date(2026, 10, 7, 8, 6, 30, 0, time.UTC) }}
	rec := reportsGet(t, s, "/reports/freshness")
	if rec.Body.String() != `{"as_of":"2026-10-07T08:05:00Z","lag_seconds":90}`+"\n" {
		t.Fatalf("freshness = %s", rec.Body)
	}
}

func TestReports_HealthzAndNoWriteRoutes(t *testing.T) {
	s := &ReportsServer{Reader: analyticsstore.NewMemory()}
	if rec := reportsGet(t, s, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		NewReportsRouter(s).ServeHTTP(rec, httptest.NewRequest(method, "/reports/receiving-performance", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405: the reports API is read-only", method, rec.Code)
		}
	}
	// The real clock is used when Now is nil.
	rec := reportsGet(t, s, "/reports/receiving-performance")
	var body struct{ To time.Time }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if time.Since(body.To) > time.Minute || rec.Code != 200 {
		t.Fatalf("default to = %v (%d)", body.To, rec.Code)
	}
}
