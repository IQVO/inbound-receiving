package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// ReportsServiceName labels the reports binary for logs/spans/metrics,
// distinct from the api's DefaultServiceName.
const ReportsServiceName = "inbound-receiving-reports"

// ReportsServer is the inbound HTTP adapter of cmd/inbound-reports: the
// read-only receiving performance report of ADR 0006. It depends only on the
// report.Reader port over the analytical database; it never touches the OLTP
// use cases, and it writes nothing.
type ReportsServer struct {
	Reader report.Reader
	// Now is the clock the default range ends at, "today" is taken from and
	// the freshness lag is measured against (time.Now when nil).
	Now func() time.Time
}

const dayLayout = "2006-01-02"

// The wire shapes (snake_case like the rest of this API); days are UTC
// calendar dates and the report structs never leak onto the wire.
type timingDTO struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type discrepanciesDTO struct {
	Short   int `json:"short"`
	Over    int `json:"over"`
	Damaged int `json:"damaged"`
}

type appointmentsDTO struct {
	Booked    int `json:"booked"`
	CheckedIn int `json:"checked_in"`
	Cancelled int `json:"cancelled"`
	Completed int `json:"completed"`
}

type performanceDayDTO struct {
	Day            string           `json:"day"`
	ReceiptsClosed int              `json:"receipts_closed"`
	LinesReceived  int              `json:"lines_received"`
	UnitsGood      int64            `json:"units_good"`
	UnitsDamaged   int64            `json:"units_damaged"`
	Discrepancies  discrepanciesDTO `json:"discrepancies"`
	ReceiptCycle   timingDTO        `json:"receipt_cycle"`
	Appointments   appointmentsDTO  `json:"appointments"`
	DockDwell      timingDTO        `json:"dock_dwell"`
}

type currentDTO struct {
	OpenReceipts         int `json:"open_receipts"`
	ExpectedToday        int `json:"expected_today"`
	ExpectedTodayStarted int `json:"expected_today_started"`
}

type performanceReportDTO struct {
	From    time.Time           `json:"from"`
	To      time.Time           `json:"to"`
	Days    []performanceDayDTO `json:"days"`
	Current currentDTO          `json:"current"`
}

// NewReportsRouter builds the router of cmd/inbound-reports: /healthz,
// GET /reports/receiving-performance and GET /reports/freshness. No auth
// middleware (fleet rule), no CORS: the service is cluster-internal.
func NewReportsRouter(s *ReportsServer) http.Handler {
	r := chi.NewRouter()
	r.Use(otelchi.Middleware(ReportsServiceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(ReportsServiceName)))
	r.Get("/healthz", handleHealthz)
	r.Get("/reports/receiving-performance", s.handleReceivingPerformance)
	r.Get("/reports/freshness", s.handleFreshness)
	return r
}

func (s *ReportsServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func timingToDTO(t report.Timing) timingDTO {
	return timingDTO{Count: t.Count, P50Seconds: t.P50, P95Seconds: t.P95}
}

func dayToDTO(d report.PerformanceDay) performanceDayDTO {
	return performanceDayDTO{
		Day: d.Day.UTC().Format(dayLayout), ReceiptsClosed: d.ReceiptsClosed, LinesReceived: d.LinesReceived,
		UnitsGood: d.UnitsGood, UnitsDamaged: d.UnitsDamaged,
		Discrepancies: discrepanciesDTO{Short: d.Discrepancies.Short, Over: d.Discrepancies.Over, Damaged: d.Discrepancies.Damaged},
		ReceiptCycle:  timingToDTO(d.ReceiptCycle),
		Appointments: appointmentsDTO{
			Booked: d.Appointments.Booked, CheckedIn: d.Appointments.CheckedIn,
			Cancelled: d.Appointments.Cancelled, Completed: d.Appointments.Completed,
		},
		DockDwell: timingToDTO(d.DockDwell),
	}
}

// handleReceivingPerformance serves the per-day counts over ?from=&to= and
// the current state (open receipts, today's expected arrivals).
func (s *ReportsServer) handleReceivingPerformance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := s.now()
	rg, err := report.ParseRange(q.Get("from"), q.Get("to"), now)
	if err != nil {
		writeReportProblem(w, r, http.StatusBadRequest, "invalid-report-range",
			"from and to must form a valid RFC 3339 range of at most 366 days", err.Error())
		return
	}
	days, err := s.Reader.PerformanceDays(r.Context(), rg)
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	cur, err := s.Reader.Current(r.Context(), report.TodayWindow(now))
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := performanceReportDTO{
		From: rg.From, To: rg.To, Days: make([]performanceDayDTO, 0, len(days)),
		Current: currentDTO{OpenReceipts: cur.OpenReceipts, ExpectedToday: cur.ExpectedToday, ExpectedTodayStarted: cur.ExpectedTodayStarted},
	}
	for _, d := range days {
		out.Days = append(out.Days, dayToDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFreshness serves how far the projection is behind (now - the newest
// applied event time). Both fields are null until the first event is applied.
func (s *ReportsServer) handleFreshness(w http.ResponseWriter, r *http.Request) {
	asOf, err := s.Reader.LastEventAt(r.Context())
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report.ComputeFreshness(asOf, s.now()))
}

// writeReportStoreError is the 500 for a failed analytical query. The cause
// is logged, never echoed: it can carry SQL and connection details.
func writeReportStoreError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "report query failed", "error", err, "path", r.URL.Path)
	writeReportProblem(w, r, http.StatusInternalServerError, "report-store-error",
		"The report could not be served", "the analytical database query failed")
}

func writeReportProblem(w http.ResponseWriter, r *http.Request, status int, slug, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Type: problemBaseURI + slug, Title: title, Status: status, Detail: detail, Instance: r.URL.EscapedPath(),
	})
}
