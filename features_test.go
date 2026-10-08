// Package main_test hosts the godog (Cucumber for Go) acceptance suite. It
// drives the REAL chi router over HTTP with the in-memory adapters, wired the
// way cmd/api wires them, and feeds the REAL consumers' use cases, so every
// scenario in features/*.feature is a black-box test of the service.
package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// TestFeatures runs every Gherkin feature under features/.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// bddStart is the service clock at the start of every scenario.
var bddStart = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time { return c.now }

const eventTypePrefix = "com.warehouse.wms.inbound-receiving."

// world is the per-scenario state.
type world struct {
	server *httptest.Server
	clock  *steppingClock
	outbox *memory.OutboxRepo
	skus   *memory.KnownSkus
	doors  *memory.DockDoors

	uow       *memory.UnitOfWork
	processed *memory.ProcessedEventRepo
	build     func() *httptest.Server

	skuMode, doorMode usecases.Mode
	keys              int
	events            int

	status int
	header http.Header
	body   []byte

	appointment, receipt string
	listQuery            string
}

func (w *world) start() {
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	w.skus, w.doors = memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	w.outbox, w.processed = ob, processed
	w.clock = &steppingClock{now: bddStart}
	w.skuMode, w.doorMode = usecases.ModePermissive, usecases.ModePermissive
	w.status, w.header, w.body, w.keys, w.appointment, w.receipt, w.listQuery = 0, nil, nil, 0, "", "", ""
	uow := memory.NewUnitOfWork(asns, appts, receipts, w.skus, w.doors, ob, processed)
	w.uow = uow
	writer := usecases.Writer{
		Asns: asns, Appointments: appts, Receipts: receipts, Outbox: ob, Encoder: outboundkafka.NewEncoder(),
		UoW: uow, Clock: w.clock, IDs: idgen.UUID{},
	}
	idem := memory.NewIdempotencyStore()
	// The server is built lazily so a Given can pick the enforcement modes
	// before the first request.
	w.build = func() *httptest.Server {
		s := &inboundhttp.Server{
			RegisterAsn:        &usecases.RegisterAsn{Writer: writer, SkuMode: w.skuMode, Skus: w.skus},
			CancelAsn:          &usecases.CancelAsn{Writer: writer},
			GetAsn:             &usecases.GetAsn{Asns: asns},
			ListAsns:           &usecases.ListAsns{Asns: asns},
			BookAppointment:    &usecases.BookAppointment{Writer: writer, DoorMode: w.doorMode, Doors: w.doors},
			CheckInAppointment: &usecases.CheckInAppointment{Writer: writer},
			CancelAppointment:  &usecases.CancelAppointment{Writer: writer},
			GetAppointment:     &usecases.GetAppointment{Appointments: appts},
			ListAppointments:   &usecases.ListAppointments{Appointments: appts},
			OpenReceipt:        &usecases.OpenReceipt{Writer: writer},
			ReceiveLine:        &usecases.ReceiveLine{Writer: writer},
			CloseReceipt:       &usecases.CloseReceipt{Writer: writer},
			GetReceipt:         &usecases.GetReceipt{Receipts: receipts},
			ListReceipts:       &usecases.ListReceipts{Receipts: receipts},
			ListDocks:          &usecases.ListDocks{Doors: w.doors, Mode: w.doorMode},
			Idempotency:        idem,
		}
		return httptest.NewServer(inboundhttp.NewRouter(s))
	}
}

func (w *world) stop() {
	if w.server != nil {
		w.server.Close()
		w.server = nil
	}
}

// expand substitutes the remembered ids into a path.
func (w *world) expand(s string) string {
	return strings.NewReplacer("{appointment}", w.appointment, "{receipt}", w.receipt).Replace(s)
}

// call issues a request and remembers the response. A non-empty key is sent
// as the Idempotency-Key.
func (w *world) call(method, path, key, body string, extra map[string]string) error {
	if w.server == nil {
		w.server = w.build()
	}
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(w.expand(body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, w.server.URL+w.expand(path), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	w.status, w.header = resp.StatusCode, resp.Header
	w.body, err = io.ReadAll(resp.Body)
	return err
}

// freshKey is a never-reused Idempotency-Key.
func (w *world) freshKey() string {
	w.keys++
	return "bdd-key-" + strconv.Itoa(w.keys)
}

// mustCall is call for Given steps: the setup itself must succeed.
func (w *world) mustCall(method, path, body string) error {
	if err := w.call(method, path, w.freshKey(), body, nil); err != nil {
		return err
	}
	if w.status != http.StatusOK && w.status != http.StatusCreated {
		return fmt.Errorf("setup %s %s = %d %s", method, path, w.status, w.body)
	}
	return nil
}

func (w *world) iPOST(path, body string) error {
	return w.call(http.MethodPost, path, w.freshKey(), body, nil)
}

func (w *world) iPOSTWithKey(path, key, body string) error {
	return w.call(http.MethodPost, path, key, body, nil)
}

func (w *world) iPOSTWithoutKey(path, body string) error {
	return w.call(http.MethodPost, path, "", body, nil)
}

func (w *world) iPOSTWithIfMatch(path, version, body string) error {
	return w.call(http.MethodPost, path, w.freshKey(), body, map[string]string{"If-Match": `"` + version + `"`})
}

func (w *world) iGET(path string) error { return w.call(http.MethodGet, path, "", "", nil) }

// Givens.

func (w *world) skuRegistryEnforced(skus string) error {
	w.skuMode = usecases.ModeKafka
	w.stop() // the next request rebuilds the server with the new mode
	apply := &usecases.ApplyProductRegistered{UoW: w.uow, ProcessedEvents: w.processed, Skus: w.skus}
	for _, s := range splitList(skus) {
		w.events++
		if _, err := apply.Handle(context.Background(), fmt.Sprintf("bdd-product-%d", w.events), s); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) dockDoorsEnforced(codes string) error {
	w.doorMode = usecases.ModeKafka
	w.stop() // the next request rebuilds the server with the new mode
	apply := &usecases.ApplyLocationSlotRegistered{UoW: w.uow, ProcessedEvents: w.processed, Doors: w.doors}
	for _, c := range splitList(codes) {
		w.events++
		slot := usecases.LocationSlot{LocationCode: c, Role: "Dock", DockFlow: "Inbound"}
		if _, err := apply.Handle(context.Background(), fmt.Sprintf("bdd-slot-%d", w.events), slot); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) clockIsAt(at string) error {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return err
	}
	w.clock.now = t
	return nil
}

func asnBody(number string) string {
	return `{"asnNumber":"` + number + `","supplierRef":"ACME","expectedArrival":"2026-10-10T08:00:00Z","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40},{"lineNo":2,"sku":"SKU-2","expectedQty":5}]}`
}

func (w *world) asnIsRegistered(number string) error {
	return w.mustCall(http.MethodPost, "/asns", asnBody(number))
}

func (w *world) asnIsCancelled(number string) error {
	return w.mustCall(http.MethodPost, "/asns/"+number+"/cancel", "")
}

func (w *world) appointmentIsBooked(door, from, to, number string) error {
	body := fmt.Sprintf(`{"doorCode":%q,"carrier":"ACME Freight","windowStart":%q,"windowEnd":%q,"asnNumbers":[%q]}`, door, from, to, number)
	if err := w.mustCall(http.MethodPost, "/appointments", body); err != nil {
		return err
	}
	return w.rememberID("appointmentId", &w.appointment)
}

func (w *world) appointmentIsCheckedIn() error {
	return w.mustCall(http.MethodPost, "/appointments/{appointment}/check-in", "")
}

func (w *world) walkInReceiptIsOpen(number string) error {
	if err := w.mustCall(http.MethodPost, "/receipts", `{"asnNumber":"`+number+`"}`); err != nil {
		return err
	}
	return w.rememberID("receiptId", &w.receipt)
}

func (w *world) receiptIsOpenOnAppointment(number string) error {
	body := `{"asnNumber":"` + number + `","appointmentId":"` + w.appointment + `"}`
	if err := w.mustCall(http.MethodPost, "/receipts", body); err != nil {
		return err
	}
	return w.rememberID("receiptId", &w.receipt)
}

func (w *world) iReceive(qty, line int, condition string) error {
	body := fmt.Sprintf(`{"lineNo":%d,"quantity":%d,"condition":%q}`, line, qty, condition)
	return w.mustCall(http.MethodPost, "/receipts/{receipt}/lines", body)
}

func (w *world) receiptIsClosed() error {
	return w.mustCall(http.MethodPost, "/receipts/{receipt}/close", "")
}

func (w *world) rememberID(field string, dest *string) error {
	v, ok, err := w.field(field)
	if err != nil || !ok {
		return fmt.Errorf("no %s in %s (%v)", field, w.body, err)
	}
	*dest = fmt.Sprint(v)
	return nil
}

func splitList(raw string) []string {
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Thens.

func (w *world) theResponseStatusIs(want int) error {
	if w.status != want {
		return fmt.Errorf("status = %d (%s), want %d", w.status, w.body, want)
	}
	return nil
}

// field walks a dotted path into the last JSON response; a numeric segment
// indexes an array.
func (w *world) field(path string) (any, bool, error) {
	var cur any
	if err := json.Unmarshal(w.body, &cur); err != nil {
		return nil, false, fmt.Errorf("response is not JSON: %s", w.body)
	}
	for _, key := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[key]
			if !ok {
				return nil, false, nil
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false, nil
			}
			cur = node[i]
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

// render prints a JSON value the way the feature files write it: arrays as
// comma-separated items, numbers without a fraction.
func render(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = render(item)
		}
		return strings.Join(parts, ",")
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func (w *world) theResponseFieldIs(path, want string) error {
	v, ok, err := w.field(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("field %q absent in %s", path, w.body)
	}
	if got := render(v); got != want {
		return fmt.Errorf("field %q = %q, want %q (%s)", path, got, want, w.body)
	}
	return nil
}

func (w *world) theResponseFieldIsAbsent(path string) error {
	if _, ok, err := w.field(path); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("field %q present in %s", path, w.body)
	}
	return nil
}

func (w *world) theResponseFieldIsPresent(path string) error {
	if _, ok, err := w.field(path); err != nil || !ok {
		return fmt.Errorf("field %q absent in %s (%v)", path, w.body, err)
	}
	return nil
}

func (w *world) theETagIs(version string) error {
	if got := w.header.Get("ETag"); got != `"`+version+`"` {
		return fmt.Errorf("ETag = %s, want \"%s\"", got, version)
	}
	return nil
}

func (w *world) theProblemTypeIs(slug string) error {
	return w.theResponseFieldIs("type", "https://errors.inbound-receiving.warehouse-systems.dev/"+slug)
}

func (w *world) theOutboxEventTypesAre(names string) error {
	want := make([]string, 0)
	for _, n := range splitList(names) {
		want = append(want, eventTypePrefix+n)
	}
	var got []string
	for _, m := range w.outbox.Messages() {
		got = append(got, m.EventType)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("outbox = %v, want %v", got, want)
	}
	return nil
}

func (w *world) theOutboxIsEmpty() error { return w.theOutboxEventTypesAre("") }

func (w *world) iListWithQuery(path, query string) error {
	w.listQuery = path + "?" + query
	return w.iGET(w.listQuery)
}

func (w *world) iListTheNextPage() error {
	cursor, ok, err := w.field("nextCursor")
	if err != nil || !ok {
		return fmt.Errorf("no nextCursor in %s (%v)", w.body, err)
	}
	return w.iGET(w.listQuery + "&cursor=" + fmt.Sprint(cursor))
}

func (w *world) theListedItemsAre(key, want string) error {
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.body, &page); err != nil {
		return err
	}
	got := make([]string, len(page.Items))
	for i, it := range page.Items {
		got[i] = fmt.Sprint(it[key])
	}
	if strings.Join(got, ",") != strings.Join(splitList(want), ",") {
		return fmt.Errorf("listed %v, want %s", got, want)
	}
	return nil
}

// InitializeScenario registers every step.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w.start()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w.stop()
		return ctx, err
	})

	sc.Step(`^I POST "([^"]*)" with body '(.*)'$`, w.iPOST)
	sc.Step(`^I POST "([^"]*)" with key "([^"]*)" and body '(.*)'$`, w.iPOSTWithKey)
	sc.Step(`^I POST "([^"]*)" without an idempotency key and body '(.*)'$`, w.iPOSTWithoutKey)
	sc.Step(`^I POST "([^"]*)" with If-Match "([^"]*)" and body '(.*)'$`, w.iPOSTWithIfMatch)
	sc.Step(`^I GET "([^"]*)"$`, w.iGET)
	sc.Step(`^I list "([^"]*)" with query "([^"]*)"$`, w.iListWithQuery)
	sc.Step(`^I list the next page$`, w.iListTheNextPage)

	sc.Step(`^the product registry is enforced and knows "([^"]*)"$`, w.skuRegistryEnforced)
	sc.Step(`^the dock doors are enforced and know "([^"]*)"$`, w.dockDoorsEnforced)
	sc.Step(`^the clock is at "([^"]*)"$`, w.clockIsAt)
	sc.Step(`^the ASN "([^"]*)" is registered$`, w.asnIsRegistered)
	sc.Step(`^the ASN "([^"]*)" is cancelled$`, w.asnIsCancelled)
	sc.Step(`^an appointment at door "([^"]*)" from "([^"]*)" to "([^"]*)" covers the ASN "([^"]*)"$`, w.appointmentIsBooked)
	sc.Step(`^the appointment is checked in$`, w.appointmentIsCheckedIn)
	sc.Step(`^a walk-in receipt is open for the ASN "([^"]*)"$`, w.walkInReceiptIsOpen)
	sc.Step(`^a receipt is open for the ASN "([^"]*)" on the appointment$`, w.receiptIsOpenOnAppointment)
	sc.Step(`^the receipt is closed$`, w.receiptIsClosed)
	sc.Step(`^(\d+) units of line (\d+) were received as "([^"]*)"$`, w.iReceive)

	sc.Step(`^the response status is (\d+)$`, w.theResponseStatusIs)
	sc.Step(`^the response field "([^"]*)" is "([^"]*)"$`, w.theResponseFieldIs)
	sc.Step(`^the response field "([^"]*)" is absent$`, w.theResponseFieldIsAbsent)
	sc.Step(`^the response field "([^"]*)" is present$`, w.theResponseFieldIsPresent)
	sc.Step(`^the ETag is "([^"]*)"$`, w.theETagIs)
	sc.Step(`^the problem type is "([^"]*)"$`, w.theProblemTypeIs)
	sc.Step(`^the outbox event types are "([^"]*)"$`, w.theOutboxEventTypesAre)
	sc.Step(`^the outbox is empty$`, w.theOutboxIsEmpty)
	sc.Step(`^the listed "([^"]*)" values are "([^"]*)"$`, w.theListedItemsAre)
}
