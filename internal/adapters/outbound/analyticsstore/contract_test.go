package analyticsstore

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// store is what the contract exercises: the writer and the reader over one
// model.
type store interface {
	report.Projection
	report.Reader
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ev(id string, kind report.Kind, when string) report.Event {
	return report.Event{Kind: kind, EventID: id, At: at(when)}
}

func asnEv(id string, kind report.Kind, number, when string) report.Event {
	e := ev(id, kind, when)
	e.AsnNumber = number
	return e
}

func registered(id, number, when, expected string) report.Event {
	e := asnEv(id, report.KindAsnRegistered, number, when)
	if expected != "" {
		x := at(expected)
		e.ExpectedArrival = &x
	}
	return e
}

func apptEv(id string, kind report.Kind, appt, when string) report.Event {
	e := ev(id, kind, when)
	e.AppointmentID = appt
	return e
}

func rcptEv(id string, kind report.Kind, receipt, number, when string) report.Event {
	e := ev(id, kind, when)
	e.ReceiptID, e.AsnNumber = receipt, number
	return e
}

func lineEv(id, receipt string, lineNo int, qty int64, cond report.Condition, when string) report.Event {
	e := rcptEv(id, report.KindReceiptLineReceived, receipt, "ASN-X", when)
	e.Line = &report.LineReceived{LineNo: lineNo, SKU: "SKU-1", Quantity: qty, Condition: cond}
	return e
}

func closedEv(id, receipt, when string, ds ...report.Discrepancy) report.Event {
	e := rcptEv(id, report.KindReceiptClosed, receipt, "ASN-X", when)
	e.Discrepancies = ds
	return e
}

func mustApply(t *testing.T, s store, events ...report.Event) {
	t.Helper()
	for _, e := range events {
		if _, err := s.Apply(context.Background(), e); err != nil {
			t.Fatalf("apply %s %s: %v", e.Kind, e.EventID, err)
		}
	}
}

func perf(t *testing.T, s store, from, to string) []report.PerformanceDay {
	t.Helper()
	got, err := s.PerformanceDays(context.Background(), report.Range{From: at(from), To: at(to)})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func approx(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-6
}

func f(v float64) *float64 { return &v }

func timing(n int, p50, p95 *float64) report.Timing {
	return report.Timing{Count: n, P50: p50, P95: p95}
}

// assertDay compares a day, with the percentiles within 1e-6 (SQL numeric
// interpolation versus float arithmetic).
func assertDay(t *testing.T, got, want report.PerformanceDay) {
	t.Helper()
	if !got.Day.Equal(want.Day) || got.Day.Location() != time.UTC {
		t.Fatalf("day = %v, want %v (UTC)", got.Day, want.Day)
	}
	for _, p := range []struct {
		name      string
		got, want report.Timing
	}{{"receipt cycle", got.ReceiptCycle, want.ReceiptCycle}, {"dock dwell", got.DockDwell, want.DockDwell}} {
		if p.got.Count != p.want.Count || !approx(p.got.P50, p.want.P50) || !approx(p.got.P95, p.want.P95) {
			t.Fatalf("%s %s = %+v, want %+v", want.Day.Format("2006-01-02"), p.name, p.got, p.want)
		}
	}
	got.ReceiptCycle, want.ReceiptCycle, got.DockDwell, want.DockDwell = report.Timing{}, report.Timing{}, report.Timing{}, report.Timing{}
	got.Day = want.Day
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("day = %+v, want %+v", got, want)
	}
}

func zeroDay(d string) report.PerformanceDay {
	return report.PerformanceDay{Day: at(d + "T00:00:00Z")}
}

func assertDays(t *testing.T, got, want []report.PerformanceDay) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d days %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range got {
		assertDay(t, got[i], want[i])
	}
}

// contractScenarios are run against every store (Memory in the unit tests,
// Postgres in the integration tests): both must give the same answers.
var contractScenarios = []struct {
	name string
	run  func(t *testing.T, s store)
}{
	{"an empty projection reports zeros, dense days and no freshness", func(t *testing.T, s store) {
		assertDays(t, perf(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z"),
			[]report.PerformanceDay{zeroDay("2026-10-05"), zeroDay("2026-10-06")})
		c, err := s.Current(context.Background(), report.TodayWindow(at("2026-10-06T12:00:00Z")))
		if err != nil || c != (report.CurrentCounts{}) {
			t.Fatalf("current = %+v, %v", c, err)
		}
		last, err := s.LastEventAt(context.Background())
		if err != nil || last != nil {
			t.Fatalf("last = %v, %v", last, err)
		}
	}},
	{"a replayed id is a no-op", func(t *testing.T, s store) {
		e := rcptEv("e1", report.KindReceiptClosed, "R-1", "ASN-1", "2026-10-05T10:00:00Z")
		if applied, err := s.Apply(context.Background(), e); err != nil || !applied {
			t.Fatalf("first apply = %v, %v", applied, err)
		}
		again := e
		again.ReceiptID = "R-OTHER" // the same id is never applied twice, whatever it carries
		if applied, err := s.Apply(context.Background(), again); err != nil || applied {
			t.Fatalf("replay = %v, %v; want false, nil", applied, err)
		}
		days := perf(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z")
		if days[0].ReceiptsClosed != 1 {
			t.Fatalf("receipts closed = %d, want 1", days[0].ReceiptsClosed)
		}
	}},
	{"receiving counts: closed receipts, lines and units by condition, discrepancies by kind", func(t *testing.T, s store) {
		mustApply(t, s,
			rcptEv("o1", report.KindReceiptOpened, "R-1", "ASN-1", "2026-10-05T08:00:00Z"),
			lineEv("l1", "R-1", 1, 30, report.ConditionGood, "2026-10-05T08:30:00Z"),
			lineEv("l2", "R-1", 1, 4, report.ConditionDamaged, "2026-10-05T08:40:00Z"),
			lineEv("l3", "R-1", 2, 6, report.ConditionGood, "2026-10-06T00:00:00Z"), // == next day: out of day 5
			closedEv("c1", "R-1", "2026-10-05T10:00:00Z",
				report.Discrepancy{LineNo: 1, SKU: "SKU-1", Kind: report.DiscrepancyShort},
				report.Discrepancy{LineNo: 1, SKU: "SKU-1", Kind: report.DiscrepancyDamaged},
				report.Discrepancy{LineNo: 2, SKU: "SKU-2", Kind: report.DiscrepancyOver}),
		)
		d5 := zeroDay("2026-10-05")
		d5.ReceiptsClosed, d5.LinesReceived, d5.UnitsGood, d5.UnitsDamaged = 1, 2, 30, 4
		d5.Discrepancies = report.DiscrepancyCounts{Short: 1, Over: 1, Damaged: 1}
		d5.ReceiptCycle = timing(1, f(7200), f(7200))
		d6 := zeroDay("2026-10-06")
		d6.LinesReceived, d6.UnitsGood = 1, 6
		assertDays(t, perf(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z"), []report.PerformanceDay{d5, d6})
	}},
	{"cycle time percentiles; a receipt closed without a seen open has no cycle", func(t *testing.T, s store) {
		mustApply(t, s,
			rcptEv("o1", report.KindReceiptOpened, "R-1", "ASN-1", "2026-10-05T08:00:00Z"),
			closedEv("c1", "R-1", "2026-10-06T08:00:00Z"), // 24h
			rcptEv("o2", report.KindReceiptOpened, "R-2", "ASN-2", "2026-10-06T07:00:00Z"),
			closedEv("c2", "R-2", "2026-10-06T08:00:00Z"),                                  // 1h
			closedEv("c3", "R-3", "2026-10-06T09:00:00Z"),                                  // never seen opened
			closedEv("c4", "R-4", "2026-10-06T10:00:00Z"),                                  // opened after it closed arrives later
			rcptEv("o4", report.KindReceiptOpened, "R-4", "ASN-4", "2026-10-06T09:30:00Z"), // 30 min
		)
		d := zeroDay("2026-10-06")
		d.ReceiptsClosed = 4
		d.ReceiptCycle = timing(3, f(3600), f(3600+0.9*(86400-3600))) // 1800s, 3600s, 86400s
		assertDays(t, perf(t, s, "2026-10-06T00:00:00Z", "2026-10-07T00:00:00Z"), []report.PerformanceDay{d})
	}},
	{"appointment milestones and dock dwell, in any arrival order", func(t *testing.T, s store) {
		mustApply(t, s,
			apptEv("a-done", report.KindAppointmentCompleted, "AP-1", "2026-10-06T09:30:00Z"), // arrives first
			apptEv("a-book", report.KindAppointmentBooked, "AP-1", "2026-10-05T12:00:00Z"),
			apptEv("a-in", report.KindAppointmentCheckedIn, "AP-1", "2026-10-06T07:30:00Z"),
			apptEv("b-book", report.KindAppointmentBooked, "AP-2", "2026-10-06T12:00:00Z"),
			apptEv("b-cancel", report.KindAppointmentCancelled, "AP-2", "2026-10-06T13:00:00Z"),
			apptEv("c-book", report.KindAppointmentBooked, "AP-3", "2026-10-06T12:30:00Z"),
			apptEv("c-done", report.KindAppointmentCompleted, "AP-3", "2026-10-06T15:00:00Z"), // never seen checked in
		)
		d5, d6 := zeroDay("2026-10-05"), zeroDay("2026-10-06")
		d5.Appointments.Booked = 1
		d6.Appointments = report.AppointmentCounts{Booked: 2, CheckedIn: 1, Cancelled: 1, Completed: 2}
		d6.DockDwell = timing(1, f(7200), f(7200))
		assertDays(t, perf(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z"), []report.PerformanceDay{d5, d6})
	}},
	{"the range is half-open and clips the first and last day", func(t *testing.T, s store) {
		mustApply(t, s,
			closedEv("a", "A", "2026-10-05T09:59:59Z"),
			closedEv("b", "B", "2026-10-05T10:00:00Z"), // == from: in
			closedEv("c", "C", "2026-10-06T05:59:59Z"),
			closedEv("d", "D", "2026-10-06T06:00:00Z"), // == to: out
		)
		d5, d6 := zeroDay("2026-10-05"), zeroDay("2026-10-06")
		d5.ReceiptsClosed, d6.ReceiptsClosed = 1, 1
		assertDays(t, perf(t, s, "2026-10-05T10:00:00Z", "2026-10-06T06:00:00Z"), []report.PerformanceDay{d5, d6})
	}},
	{"current: open receipts and today's expected arrivals", func(t *testing.T, s store) {
		mustApply(t, s,
			registered("r1", "ASN-1", "2026-10-01T08:00:00Z", "2026-10-06T08:00:00Z"),
			registered("r2", "ASN-2", "2026-10-01T08:00:00Z", "2026-10-06T23:59:59Z"),
			registered("r3", "ASN-3", "2026-10-01T08:00:00Z", "2026-10-07T00:00:00Z"), // == to: tomorrow
			registered("r4", "ASN-4", "2026-10-01T08:00:00Z", "2026-10-06T10:00:00Z"),
			registered("r5", "ASN-5", "2026-10-01T08:00:00Z", ""), // no expected arrival
			asnEv("x4", report.KindAsnCancelled, "ASN-4", "2026-10-05T08:00:00Z"),
			rcptEv("o1", report.KindReceiptOpened, "R-1", "ASN-1", "2026-10-06T08:05:00Z"), // started, still open
			rcptEv("o2", report.KindReceiptOpened, "R-2", "ASN-9", "2026-10-06T08:10:00Z"),
			closedEv("c2", "R-2", "2026-10-06T09:00:00Z"),
		)
		c, err := s.Current(context.Background(), report.TodayWindow(at("2026-10-06T12:00:00Z")))
		if err != nil {
			t.Fatal(err)
		}
		if want := (report.CurrentCounts{OpenReceipts: 1, ExpectedToday: 2, ExpectedTodayStarted: 1}); c != want {
			t.Fatalf("current = %+v, want %+v", c, want)
		}
	}},
	{"freshness is the newest applied CloudEvents time", func(t *testing.T, s store) {
		mustApply(t, s,
			ev2("n2", report.KindAppointmentBooked, "2026-10-06T10:00:00Z"),
			ev2("n1", report.KindAppointmentBooked, "2026-10-05T10:00:00Z"),
		)
		last, err := s.LastEventAt(context.Background())
		if err != nil || last == nil || !last.Equal(at("2026-10-06T10:00:00Z")) || last.Location() != time.UTC {
			t.Fatalf("last = %v, %v", last, err)
		}
	}},
	{"a line the schema forbids is rejected and leaves nothing behind", func(t *testing.T, s store) {
		bad := lineEv("bad", "R-9", 1, 0, report.ConditionGood, "2026-10-05T10:00:00Z")
		if _, err := s.Apply(context.Background(), bad); !errors.Is(err, report.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
		if last, _ := s.LastEventAt(context.Background()); last != nil {
			t.Fatalf("the rejected event was marked processed: %v", last)
		}
		if d := perf(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z"); d[0].LinesReceived != 0 {
			t.Fatalf("the rejected event left a line: %+v", d[0])
		}
		good := bad
		good.Line = &report.LineReceived{LineNo: 1, SKU: "SKU-1", Quantity: 1, Condition: report.ConditionGood}
		if applied, err := s.Apply(context.Background(), good); err != nil || !applied {
			t.Fatalf("the same id must be applicable once fixed: %v, %v", applied, err)
		}
	}},
}

// ev2 is an appointment event with a throwaway appointment id.
func ev2(id string, kind report.Kind, when string) report.Event {
	return apptEv(id, kind, "AP-"+id, when)
}

func runContract(t *testing.T, newStore func(t *testing.T) store) {
	for _, sc := range contractScenarios {
		t.Run(sc.name, func(t *testing.T) { sc.run(t, newStore(t)) })
	}
}
