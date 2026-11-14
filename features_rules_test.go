package main_test

// Step definitions of the documentation-derived rule features (asn_rules,
// receiving_rules, feeds_rules, operations_rules). They add four things to the
// existing vocabulary: assertions on the published CloudEvents (envelope and
// payload), explicit-version feeds of the local copies, a readiness gate and
// the reports service (cmd/inbound-reports) over a faked analytical store.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// rulesState is the per-scenario state of these steps.
type rulesState struct {
	// readiness is the gate the next server is built with (nil = none).
	readiness     *inboundhttp.Readiness
	postgres      atomic.Bool
	kafka         atomic.Bool
	reader        *fakeReportReader
	reportNow     time.Time
	redeliverLast func(context.Context) (usecases.Outcome, error)
}

// fakeReportReader is the analytical store of the reports service: it answers
// every day of the range with zeros (like the real reader) and remembers the
// range it was asked for.
type fakeReportReader struct {
	asked   report.Range
	lastAt  *time.Time
	failing bool
}

var errStoreDown = errors.New("pq: connection refused: host=analytics.internal password=hunter2")

func (f *fakeReportReader) PerformanceDays(_ context.Context, r report.Range) ([]report.PerformanceDay, error) {
	if f.failing {
		return nil, errStoreDown
	}
	f.asked = r
	var out []report.PerformanceDay
	for _, d := range report.DayWindows(r) {
		out = append(out, report.PerformanceDay{Day: d.Day})
	}
	return out, nil
}

func (f *fakeReportReader) Current(context.Context, report.DayWindow) (report.CurrentCounts, error) {
	if f.failing {
		return report.CurrentCounts{}, errStoreDown
	}
	return report.CurrentCounts{}, nil
}

func (f *fakeReportReader) LastEventAt(context.Context) (*time.Time, error) {
	if f.failing {
		return nil, errStoreDown
	}
	return f.lastAt, nil
}

// ---- published events ------------------------------------------------------

// lastEvent decodes the newest outbox message of the given type (the part
// after the type prefix, e.g. "receipt.ReceiptClosed").
func (w *world) lastEvent(kind string) (any, error) {
	var raw []byte
	for _, m := range w.outbox.Messages() {
		if m.EventType == eventTypePrefix+kind {
			raw = m.Value
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("no %s event in the outbox (%v)", kind, w.outboxTypes())
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// walkJSON follows a dotted path; a numeric segment indexes an array.
func walkJSON(cur any, path string) (any, bool) {
	for _, key := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[key]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func (w *world) theLastEventHas(kind, path, want string) error {
	doc, err := w.lastEvent(kind)
	if err != nil {
		return err
	}
	v, ok := walkJSON(doc, path)
	if !ok {
		return fmt.Errorf("%s has no %q (%v)", kind, path, doc)
	}
	if got := render(v); got != w.expand(want) {
		return fmt.Errorf("%s %q = %q, want %q", kind, path, got, w.expand(want))
	}
	return nil
}

func (w *world) theLastEventHasNo(kind, path string) error {
	doc, err := w.lastEvent(kind)
	if err != nil {
		return err
	}
	if v, ok := walkJSON(doc, path); ok {
		return fmt.Errorf("%s carries %q = %v, want it omitted", kind, path, v)
	}
	return nil
}

func (w *world) everyEventIsKeyedBy(asn string) error {
	msgs := w.outbox.Messages()
	if len(msgs) == 0 {
		return errors.New("the outbox is empty")
	}
	for _, m := range msgs {
		if string(m.Key) != asn {
			return fmt.Errorf("%s is keyed %q, want %q", m.EventType, m.Key, asn)
		}
	}
	return nil
}

func (w *world) theOutboxHoldsN(n int, kind string) error {
	got := 0
	for _, m := range w.outbox.Messages() {
		if m.EventType == eventTypePrefix+kind {
			got++
		}
	}
	if got != n {
		return fmt.Errorf("%s events = %d, want %d (outbox %v)", kind, got, n, w.outboxTypes())
	}
	return nil
}

// theDiscrepanciesAre asserts the discrepancies of the last receipt response
// as "Kind@line" pairs in order, or "none".
func (w *world) theDiscrepanciesAre(want string) error {
	v, ok, err := w.field("discrepancies")
	if err != nil {
		return fmt.Errorf("no discrepancies in %s: %w", w.body, err)
	}
	if !ok {
		return fmt.Errorf("no discrepancies in %s", w.body)
	}
	items, _ := v.([]any)
	got := make([]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		got = append(got, fmt.Sprintf("%v@%s", m["kind"], render(m["lineNo"])))
	}
	if want == "none" {
		want = ""
	}
	if strings.Join(got, ",") != strings.Join(splitList(want), ",") {
		return fmt.Errorf("discrepancies = %v, want %q", got, want)
	}
	return nil
}

// ---- requests ---------------------------------------------------------------

func (w *world) iPOSTWithKeyOfLength(path string, n int, body string) error {
	return w.call(http.MethodPost, path, strings.Repeat("k", n), body, nil)
}

func (w *world) iCancelWithReasonOfLength(number string, n int) error {
	body := `{"reason":"` + strings.Repeat("r", n) + `"}`
	return w.call(http.MethodPost, "/asns/"+number+"/cancellations", w.freshKey(), body, nil)
}

// lineIsCounted books a Good and a Damaged count on one line, skipping a zero
// (a quantity of 0 is not a valid receive).
func (w *world) lineIsCounted(line, good, damaged int) error {
	if good > 0 {
		if err := w.iReceive(good, line, "Good"); err != nil {
			return err
		}
	}
	if damaged > 0 {
		return w.iReceive(damaged, line, "Damaged")
	}
	return nil
}

func (w *world) iReceiveWithIfMatch(version, body string) error {
	return w.call(http.MethodPost, w.expand("/receipts/{receipt}/lines"), w.freshKey(), body, map[string]string{"If-Match": `"` + version + `"`})
}

func (w *world) iCloseWithIfMatch(version string) error {
	return w.call(http.MethodPost, w.expand("/receipts/{receipt}/closures"), w.freshKey(), "", map[string]string{"If-Match": `"` + version + `"`})
}

// ---- appointments -----------------------------------------------------------

// anAppointmentCoversAsns is the Given of an appointment that covers several ASNs.
func (w *world) anAppointmentCoversAsns(ctx context.Context, door, from, to, asns string) error {
	e, err := w.newAppointmentEvent(door, from, to, "ASN-X", 1)
	if err != nil {
		return err
	}
	e.AsnNumbers = splitList(asns)
	w.feedBooked(ctx, e)
	if w.wave2.err != nil {
		return fmt.Errorf("setup: booking gave %q: %w", w.wave2.outcome, w.wave2.err)
	}
	if w.wave2.outcome != usecases.Applied {
		return fmt.Errorf("setup: booking gave %q, want %q", w.wave2.outcome, usecases.Applied)
	}
	w.appointment, w.apptVersion = e.AppointmentID, e.Version
	return nil
}

// dockSchedulingAnnounces feeds an arbitrary booking, valid or not.
func (w *world) dockSchedulingAnnounces(ctx context.Context, direction, door, from, to, asns string, version int64) error {
	e, err := w.newAppointmentEvent(door, from, to, "ASN-X", version)
	if err != nil {
		return err
	}
	e.Direction, e.AsnNumbers = direction, splitList(asns)
	w.feedBooked(ctx, e)
	return nil
}

func (w *world) dockSchedulingSendsFollowUp(ctx context.Context, kind string, version int64) error {
	id := w.nextEventID("bdd-appt-follow")
	if kind == "reschedule" {
		start, _ := time.Parse(time.RFC3339, "2026-10-09T14:00:00Z")
		ev := usecases.DockAppointmentRescheduled{AppointmentID: w.appointment, DoorCode: "DOOR-NEW", WindowStart: start, WindowEnd: start.Add(time.Hour), Version: version}
		uc := &usecases.ApplyDockAppointmentRescheduled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
		w.rules.redeliverLast = func(ctx context.Context) (usecases.Outcome, error) { return uc.Handle(ctx, id, ev) }
	} else {
		ev := usecases.DockAppointmentDoorReassigned{AppointmentID: w.appointment, FromDoorCode: "DOOR-1", ToDoorCode: "DOOR-NEW", Version: version}
		uc := &usecases.ApplyDockAppointmentDoorReassigned{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
		w.rules.redeliverLast = func(ctx context.Context) (usecases.Outcome, error) { return uc.Handle(ctx, id, ev) }
	}
	w.wave2.outcome, w.wave2.err = w.rules.redeliverLast(ctx)
	return nil
}

func (w *world) dockSchedulingRedeliversFollowUp(ctx context.Context) error {
	if w.rules.redeliverLast == nil {
		return errors.New("no follow-up announcement to redeliver")
	}
	w.wave2.outcome, w.wave2.err = w.rules.redeliverLast(ctx)
	return nil
}

func (w *world) dockSchedulingCancelsAtVersion(ctx context.Context, version int64) error {
	uc := &usecases.ApplyDockAppointmentCancelled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, w.nextEventID("bdd-appt-cancel"), w.appointment, version)
	return nil
}

func (w *world) dockSchedulingCancelsAppointmentAtVersion(ctx context.Context, id string, version int64) error {
	uc := &usecases.ApplyDockAppointmentCancelled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, w.nextEventID("bdd-appt-cancel"), id, version)
	return nil
}

func (w *world) dockSchedulingCancelsANeverCopied(ctx context.Context) error {
	uc := &usecases.ApplyDockAppointmentCancelled{UoW: w.uow, ProcessedEvents: w.processed, Appointments: w.appts}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, w.nextEventID("bdd-appt-cancel"), "appt-00000000-0000-4000-8000-0000000000aa", 2)
	return nil
}

// ---- packaging view ---------------------------------------------------------

func (w *world) productMasterDeclaresCase(ctx context.Context, caseGTIN, sku string, eaches int64, each string, version int64) error {
	uc := &usecases.ApplyPackLevelDeclared{UoW: w.uow, ProcessedEvents: w.processed, Packaging: w.packaging}
	ev := usecases.PackLevelDeclared{
		SKU: sku, Level: "CASE", GTIN: caseGTIN, ChildGTIN: each, QtyOfChild: eaches, UnitsPerPack: eaches,
		EffectiveFrom: bddStart.Add(-24 * time.Hour), Version: version,
	}
	id := w.nextEventID("bdd-pack")
	w.rules.redeliverLast = func(ctx context.Context) (usecases.Outcome, error) { return uc.Handle(ctx, id, ev) }
	w.wave2.outcome, w.wave2.err = w.rules.redeliverLast(ctx)
	return nil
}

func (w *world) productMasterRetires(ctx context.Context, gtin string, version int64) error {
	uc := &usecases.ApplyPackLevelRetired{UoW: w.uow, ProcessedEvents: w.processed, Packaging: w.packaging}
	id := w.nextEventID("bdd-retire")
	w.rules.redeliverLast = func(ctx context.Context) (usecases.Outcome, error) {
		return uc.Handle(ctx, id, gtin, bddStart, version)
	}
	w.wave2.outcome, w.wave2.err = w.rules.redeliverLast(ctx)
	return nil
}

// ---- readiness --------------------------------------------------------------

func (w *world) theReadinessGateWatches(n int, unit string) error {
	ttl, err := time.ParseDuration(strconv.Itoa(n) + unit)
	if err != nil {
		return err
	}
	w.rules.postgres.Store(true)
	w.rules.kafka.Store(true)
	dep := func(name string, up *atomic.Bool) inboundhttp.Dependency {
		return inboundhttp.Dependency{Name: name, Check: func(context.Context) error {
			if up.Load() {
				return nil
			}
			return errors.New(name + " is unreachable")
		}}
	}
	w.rules.readiness = inboundhttp.NewReadiness(ttl, time.Second, dep("postgres", &w.rules.postgres), dep("kafka", &w.rules.kafka))
	w.stop() // the next request rebuilds the server with the gate
	return nil
}

func (w *world) aDependencyChanges(name, change string) error {
	up := change == "starts"
	switch name {
	case "postgres":
		w.rules.postgres.Store(up)
	case "kafka":
		w.rules.kafka.Store(up)
	default:
		return fmt.Errorf("unknown dependency %q", name)
	}
	return nil
}

func (w *world) shutdownBegins() error {
	w.rules.readiness.SetNotReady()
	return nil
}

func (w *world) theVerdictWindowPasses() error {
	time.Sleep(60 * time.Millisecond)
	return nil
}

// ---- reports service ----------------------------------------------------------

func (w *world) theReportsAreServed(now string) error {
	t, err := time.Parse(time.RFC3339, now)
	if err != nil {
		return err
	}
	w.stop()
	w.rules.reader, w.rules.reportNow = &fakeReportReader{}, t
	w.server = httptest.NewServer(inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{
		Reader: w.rules.reader, Now: func() time.Time { return w.rules.reportNow },
	}))
	return nil
}

func (w *world) theStoreNewestEventIsAt(at string) error {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return err
	}
	w.rules.reader.lastAt = &t
	return nil
}

func (w *world) theStoreFails() error {
	w.rules.reader.failing = true
	return nil
}

func (w *world) theReportCoversRange(from, to string) error {
	got := w.rules.reader.asked
	if got.From.Format(time.RFC3339) != from || got.To.Format(time.RFC3339) != to {
		return fmt.Errorf("the store was asked for [%s, %s), want [%s, %s)", got.From.Format(time.RFC3339), got.To.Format(time.RFC3339), from, to)
	}
	return nil
}

func (w *world) registerRules(sc *godog.ScenarioContext) {
	sc.Step(`^the last "([^"]*)" event has "([^"]*)" equal to "([^"]*)"$`, w.theLastEventHas)
	sc.Step(`^the last "([^"]*)" event has no "([^"]*)"$`, w.theLastEventHasNo)
	sc.Step(`^every outbox event is keyed by the ASN "([^"]*)"$`, w.everyEventIsKeyedBy)
	sc.Step(`^the outbox holds (\d+) "([^"]*)" events?$`, w.theOutboxHoldsN)
	sc.Step(`^the response discrepancies are "([^"]*)"$`, w.theDiscrepanciesAre)

	sc.Step(`^I POST "([^"]*)" with an idempotency key of (\d+) characters and body '(.*)'$`, w.iPOSTWithKeyOfLength)
	sc.Step(`^I cancel the ASN "([^"]*)" with a reason of (\d+) characters$`, w.iCancelWithReasonOfLength)
	sc.Step(`^line (\d+) is counted (\d+) Good and (\d+) Damaged$`, w.lineIsCounted)
	sc.Step(`^I receive with If-Match "([^"]*)" the body '(.*)'$`, w.iReceiveWithIfMatch)
	sc.Step(`^I close the receipt with If-Match "([^"]*)"$`, w.iCloseWithIfMatch)

	sc.Step(`^an appointment at door "([^"]*)" from "([^"]*)" to "([^"]*)" covers the ASNs "([^"]*)"$`, w.anAppointmentCoversAsns)
	sc.Step(`^dock-scheduling announces an appointment with direction "([^"]*)" at door "([^"]*)" from "([^"]*)" to "([^"]*)" for the ASNs "([^"]*)" at version (\d+)$`, w.dockSchedulingAnnounces)
	sc.Step(`^dock-scheduling sends a (reschedule|reassignment) of the appointment at version (\d+)$`, w.dockSchedulingSendsFollowUp)
	sc.Step(`^the last follow-up announcement is redelivered$`, w.dockSchedulingRedeliversFollowUp)
	sc.Step(`^dock-scheduling cancels the appointment at version (\d+)$`, w.dockSchedulingCancelsAtVersion)
	sc.Step(`^dock-scheduling cancels the appointment "([^"]*)" at version (\d+)$`, w.dockSchedulingCancelsAppointmentAtVersion)
	sc.Step(`^dock-scheduling cancels an appointment this service never copied$`, w.dockSchedulingCancelsANeverCopied)

	sc.Step(`^product-master declares the case GTIN "([^"]*)" of SKU "([^"]*)" as (-?\d+) eaches of "([^"]*)" at version (\d+)$`, w.productMasterDeclaresCase)
	sc.Step(`^product-master retires the pack level "([^"]*)" at version (\d+)$`, w.productMasterRetires)

	sc.Step(`^the readiness gate watches postgres and kafka and reuses a verdict for (\d+)(ms|h)$`, w.theReadinessGateWatches)
	sc.Step(`^(postgres|kafka) (stops|starts) answering$`, w.aDependencyChanges)
	sc.Step(`^shutdown begins$`, w.shutdownBegins)
	sc.Step(`^the verdict window passes$`, w.theVerdictWindowPasses)

	sc.Step(`^the reports service runs at "([^"]*)" over an analytical store that has applied nothing$`, w.theReportsAreServed)
	sc.Step(`^the analytical store's newest applied event is at "([^"]*)"$`, w.theStoreNewestEventIsAt)
	sc.Step(`^the analytical store fails every query$`, w.theStoreFails)
	sc.Step(`^the report was computed over "([^"]*)" to "([^"]*)"$`, w.theReportCoversRange)
}
