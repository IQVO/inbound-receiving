package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/idempotency"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

var errDB = errors.New("db down")

// startOfDay is the fixture's fixed "now".
var startOfDay = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

type mutableClock struct{ now atomic.Pointer[time.Time] }

func (c *mutableClock) Now() time.Time  { return *c.now.Load() }
func (c *mutableClock) set(t time.Time) { c.now.Store(&t) }

// faults injects repository errors; nil means "behave normally".
type faults struct {
	asnGet, asnList, asnSave             error
	apptGet, apptList, apptSave          error
	receiptGet, receiptList, receiptSave error
	docksList, idempotency               error
}

type faultyAsns struct {
	*memory.AsnRepo
	f *faults
}

func (r faultyAsns) Get(ctx context.Context, n asn.Number) (*asn.Asn, error) {
	if r.f.asnGet != nil {
		return nil, r.f.asnGet
	}
	return r.AsnRepo.Get(ctx, n)
}

func (r faultyAsns) Save(ctx context.Context, a *asn.Asn, v int64) error {
	if r.f.asnSave != nil {
		return r.f.asnSave
	}
	return r.AsnRepo.Save(ctx, a, v)
}

func (r faultyAsns) List(ctx context.Context, f repository.AsnFilter, after asn.Number, limit int) ([]*asn.Asn, error) {
	if r.f.asnList != nil {
		return nil, r.f.asnList
	}
	return r.AsnRepo.List(ctx, f, after, limit)
}

type faultyAppointments struct {
	*memory.AppointmentRepo
	f *faults
}

func (r faultyAppointments) Get(ctx context.Context, id appointment.ID) (*appointment.DockAppointment, error) {
	if r.f.apptGet != nil {
		return nil, r.f.apptGet
	}
	return r.AppointmentRepo.Get(ctx, id)
}

func (r faultyAppointments) Save(ctx context.Context, d *appointment.DockAppointment, v int64) error {
	if r.f.apptSave != nil {
		return r.f.apptSave
	}
	return r.AppointmentRepo.Save(ctx, d, v)
}

func (r faultyAppointments) List(ctx context.Context, f repository.AppointmentFilter, after repository.AppointmentCursor, limit int) ([]*appointment.DockAppointment, error) {
	if r.f.apptList != nil {
		return nil, r.f.apptList
	}
	return r.AppointmentRepo.List(ctx, f, after, limit)
}

type faultyReceipts struct {
	*memory.ReceiptRepo
	f *faults
}

func (r faultyReceipts) Get(ctx context.Context, id receipt.ID) (*receipt.Receipt, error) {
	if r.f.receiptGet != nil {
		return nil, r.f.receiptGet
	}
	return r.ReceiptRepo.Get(ctx, id)
}

func (r faultyReceipts) Save(ctx context.Context, x *receipt.Receipt, v int64) error {
	if r.f.receiptSave != nil {
		return r.f.receiptSave
	}
	return r.ReceiptRepo.Save(ctx, x, v)
}

func (r faultyReceipts) List(ctx context.Context, f repository.ReceiptFilter, after receipt.ID, limit int) ([]*receipt.Receipt, error) {
	if r.f.receiptList != nil {
		return nil, r.f.receiptList
	}
	return r.ReceiptRepo.List(ctx, f, after, limit)
}

type faultyDoors struct {
	*memory.DockDoors
	f *faults
}

func (d faultyDoors) List(ctx context.Context) ([]repository.DockDoor, error) {
	if d.f.docksList != nil {
		return nil, d.f.docksList
	}
	return d.DockDoors.List(ctx)
}

type faultyIdempotency struct {
	*memory.IdempotencyStore
	f *faults
}

func (s faultyIdempotency) Do(ctx context.Context, req idempotency.Request, h func(context.Context) idempotency.Response) (idempotency.Response, idempotency.Outcome, error) {
	if s.f.idempotency != nil {
		return idempotency.Response{}, 0, s.f.idempotency
	}
	return s.IdempotencyStore.Do(ctx, req, h)
}

type fixture struct {
	server    *httptest.Server
	clock     *mutableClock
	faults    *faults
	outbox    *memory.OutboxRepo
	doors     *memory.DockDoors
	skus      *memory.KnownSkus
	readiness *inboundhttp.Readiness
	keys      atomic.Int64
}

// newFixture serves the real router over the memory adapters in permissive
// mode; newKafkaModeFixture enforces the local copies.
func newFixture(t *testing.T) *fixture { return build(t, usecases.ModePermissive) }

func newKafkaModeFixture(t *testing.T) *fixture { return build(t, usecases.ModeKafka) }

func build(t *testing.T, mode usecases.Mode) *fixture {
	t.Helper()
	f := &faults{}
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed)
	clock := &mutableClock{}
	clock.set(startOfDay)

	fa, fap, fr, fd := faultyAsns{asns, f}, faultyAppointments{appts, f}, faultyReceipts{receipts, f}, faultyDoors{doors, f}
	w := usecases.Writer{Asns: fa, Appointments: fap, Receipts: fr, Outbox: ob, Encoder: kafka.NewEncoder(), UoW: uow, Clock: clock, IDs: idgen.UUID{}}
	readiness := &inboundhttp.Readiness{}
	s := &inboundhttp.Server{
		RegisterAsn:        &usecases.RegisterAsn{Writer: w, SkuMode: mode, Skus: skus},
		CancelAsn:          &usecases.CancelAsn{Writer: w},
		GetAsn:             &usecases.GetAsn{Asns: fa},
		ListAsns:           &usecases.ListAsns{Asns: fa},
		BookAppointment:    &usecases.BookAppointment{Writer: w, DoorMode: mode, Doors: doors},
		CheckInAppointment: &usecases.CheckInAppointment{Writer: w},
		CancelAppointment:  &usecases.CancelAppointment{Writer: w},
		GetAppointment:     &usecases.GetAppointment{Appointments: fap},
		ListAppointments:   &usecases.ListAppointments{Appointments: fap},
		OpenReceipt:        &usecases.OpenReceipt{Writer: w},
		ReceiveLine:        &usecases.ReceiveLine{Writer: w},
		CloseReceipt:       &usecases.CloseReceipt{Writer: w},
		GetReceipt:         &usecases.GetReceipt{Receipts: fr},
		ListReceipts:       &usecases.ListReceipts{Receipts: fr},
		ListDocks:          &usecases.ListDocks{Doors: fd, Mode: mode},
		Idempotency:        faultyIdempotency{memory.NewIdempotencyStore(), f},
		Readiness:          readiness,
		Metrics:            http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "# metrics\n") }),
	}
	srv := httptest.NewServer(inboundhttp.NewRouter(s))
	t.Cleanup(srv.Close)
	return &fixture{server: srv, clock: clock, faults: f, outbox: ob, doors: doors, skus: skus, readiness: readiness}
}

type response struct {
	status      int
	contentType string
	header      http.Header
	body        map[string]any
	raw         string
}

// request performs one call with the given headers.
func (f *fixture) request(t *testing.T, method, path, body string, headers map[string]string) response {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (f *fixture) get(t *testing.T, path string) response {
	return f.request(t, http.MethodGet, path, "", nil)
}

// post sends a POST with a fresh Idempotency-Key.
func (f *fixture) post(t *testing.T, path, body string) response {
	t.Helper()
	return f.postWith(t, path, body, nil)
}

func (f *fixture) postWith(t *testing.T, path, body string, headers map[string]string) response {
	t.Helper()
	h := map[string]string{"Idempotency-Key": fmt.Sprintf("key-%d", f.keys.Add(1))}
	for k, v := range headers {
		h[k] = v
	}
	return f.request(t, http.MethodPost, path, body, h)
}

// mustPost performs a setup POST and fails unless it returned want.
func (f *fixture) mustPost(t *testing.T, path, body string, want int) response {
	t.Helper()
	r := f.post(t, path, body)
	if r.status != want {
		t.Fatalf("POST %s = %d %s, want %d", path, r.status, r.raw, want)
	}
	return r
}

// expectProblem asserts an RFC 7807 response with the given status and slug.
func expectProblem(t *testing.T, r response, status int, slug string) {
	t.Helper()
	if r.status != status || r.contentType != "application/problem+json" ||
		r.body["type"] != "https://errors.inbound-receiving.warehouse-systems.dev/"+slug ||
		r.body["status"] != float64(status) || r.body["title"] == "" || r.body["detail"] == "" {
		t.Fatalf("got %d %s %s, want %d problem %s", r.status, r.contentType, r.raw, status, slug)
	}
}

func expectStatus(t *testing.T, r response, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d %s, want %d", r.status, r.raw, status)
	}
}

func str(r response, key string) string {
	s, _ := r.body[key].(string)
	return s
}

// Request bodies.

func asnBody(number string) string {
	return `{"asnNumber":"` + number + `","supplierRef":"ACME","expectedArrival":"2026-10-10T08:00:00Z","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40},{"lineNo":2,"sku":"SKU-2","expectedQty":5}]}`
}

func bookBody(door string, startHour int, asns ...string) string {
	quoted := make([]string, len(asns))
	for i, a := range asns {
		quoted[i] = `"` + a + `"`
	}
	start := startOfDay.Add(time.Duration(startHour) * time.Hour)
	return fmt.Sprintf(`{"doorCode":"%s","carrier":"ACME Freight","windowStart":"%s","windowEnd":"%s","asnNumbers":[%s]}`,
		door, start.Format(time.RFC3339), start.Add(2*time.Hour).Format(time.RFC3339), strings.Join(quoted, ","))
}

// seedAsn registers an ASN through the API.
func (f *fixture) seedAsn(t *testing.T, number string) {
	t.Helper()
	f.mustPost(t, "/asns", asnBody(number), http.StatusCreated)
}

// seedAppointment books an appointment at hour (relative to the fixture
// clock) and returns its id.
func (f *fixture) seedAppointment(t *testing.T, door string, hour int, asns ...string) string {
	t.Helper()
	return str(f.mustPost(t, "/appointments", bookBody(door, hour, asns...), http.StatusCreated), "appointmentId")
}

// seedReceipt registers ASN-<n>, opens a walk-in receipt and returns its id.
func (f *fixture) seedReceipt(t *testing.T, number string) string {
	t.Helper()
	f.seedAsn(t, number)
	return str(f.mustPost(t, "/receipts", `{"asnNumber":"`+number+`"}`, http.StatusCreated), "receiptId")
}

const missingAppointment = "appt-00000000-0000-4000-8000-0000000000ff"
const missingReceipt = "rcpt-00000000-0000-4000-8000-0000000000ff"
