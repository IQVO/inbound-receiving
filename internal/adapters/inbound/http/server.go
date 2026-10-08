// Package http is the REST inbound adapter: every route of apis/openapi.yaml
// on a chi router, RFC 7807 problem documents for every error, the
// Idempotency-Key middleware on the resource-creating POSTs, and the
// liveness/readiness/metrics endpoints. It has no auth middleware (fleet
// decision 2026-09-11; internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// DefaultServiceName labels this service for telemetry.
const DefaultServiceName = "inbound-receiving"

// defaultCORSAllowedOrigins is the local-dev default for
// CORS_ALLOWED_ORIGINS (comma-separated).
const defaultCORSAllowedOrigins = "http://localhost:5173"

// Server holds the use cases the handlers call.
type Server struct {
	RegisterAsn        *usecases.RegisterAsn
	CancelAsn          *usecases.CancelAsn
	GetAsn             *usecases.GetAsn
	ListAsns           *usecases.ListAsns
	BookAppointment    *usecases.BookAppointment
	CheckInAppointment *usecases.CheckInAppointment
	CancelAppointment  *usecases.CancelAppointment
	GetAppointment     *usecases.GetAppointment
	ListAppointments   *usecases.ListAppointments
	OpenReceipt        *usecases.OpenReceipt
	ReceiveLine        *usecases.ReceiveLine
	CloseReceipt       *usecases.CloseReceipt
	GetReceipt         *usecases.GetReceipt
	ListReceipts       *usecases.ListReceipts
	ListDocks          *usecases.ListDocks

	// Idempotency backs the Idempotency-Key middleware.
	Idempotency ports.IdempotencyStore
	// Readiness backs GET /readyz; nil means always ready.
	Readiness *Readiness
	// Metrics backs GET /metrics (a Prometheus handler built by cmd/);
	// nil leaves the route unregistered.
	Metrics http.Handler
}

// NewRouter builds the chi router for every endpoint in
// .claude/rules/rest-api.md. otelchi runs first so every handler runs inside
// a span labelled with the route pattern (e.g. "/asns/{asnNumber}").
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(otelchi.Middleware(DefaultServiceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(DefaultServiceName)))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsAllowedOrigins(),
		AllowedMethods:   []string{http.MethodGet, http.MethodPost},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Idempotency-Key", "If-Match"},
		ExposedHeaders:   []string{"ETag", "Location"},
		AllowCredentials: false,
	}))

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	if s.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", s.Metrics)
	}

	// Resource-creating POSTs require an Idempotency-Key; action POSTs honour it.
	create := r.With(RequireIdempotencyKey(s.Idempotency))
	action := r.With(HonourIdempotencyKey(s.Idempotency))

	create.Post("/asns", s.handleRegisterAsn)
	r.Get("/asns", s.handleListAsns)
	r.Get("/asns/{asnNumber}", s.handleGetAsn)
	action.Post("/asns/{asnNumber}/cancel", s.handleCancelAsn)

	create.Post("/appointments", s.handleBookAppointment)
	r.Get("/appointments", s.handleListAppointments)
	r.Get("/appointments/{appointmentId}", s.handleGetAppointment)
	action.Post("/appointments/{appointmentId}/check-in", s.handleCheckInAppointment)
	action.Post("/appointments/{appointmentId}/cancel", s.handleCancelAppointment)

	create.Post("/receipts", s.handleOpenReceipt)
	r.Get("/receipts", s.handleListReceipts)
	r.Get("/receipts/{receiptId}", s.handleGetReceipt)
	create.Post("/receipts/{receiptId}/lines", s.handleReceiveLine)
	action.Post("/receipts/{receiptId}/close", s.handleCloseReceipt)

	r.Get("/docks", s.handleListDocks)
	return r
}

func corsAllowedOrigins() []string {
	raw := os.Getenv("CORS_ALLOWED_ORIGINS")
	if raw == "" {
		raw = defaultCORSAllowedOrigins
	}
	parts := strings.Split(raw, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusBody{Status: "ok"})
}
