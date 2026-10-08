package report

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		name, from, to string
		want           Range
		wantErr        error
	}{
		{"both omitted: the 30 days ending now", "", "", Range{t0.Add(-30 * 24 * time.Hour), t0}, nil},
		{"only to", "", "2026-10-01T00:00:00Z", Range{ts("2026-09-01T00:00:00Z"), ts("2026-10-01T00:00:00Z")}, nil},
		{"only from", "2026-10-06T00:00:00Z", "", Range{ts("2026-10-06T00:00:00Z"), t0}, nil},
		{"offsets are normalised to UTC", "2026-10-06T00:00:00-03:00", "2026-10-07T00:00:00-03:00",
			Range{ts("2026-10-06T03:00:00Z"), ts("2026-10-07T03:00:00Z")}, nil},
		{"exactly 366 days is allowed", "2025-10-07T00:00:00Z", "2026-10-08T00:00:00Z",
			Range{ts("2025-10-07T00:00:00Z"), ts("2026-10-08T00:00:00Z")}, nil},
		{"one second more than 366 days", "2025-10-06T23:59:59Z", "2026-10-08T00:00:00Z", Range{}, ErrRangeTooLarge},
		{"from == to is empty", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z", Range{}, ErrEmptyRange},
		{"inverted", "2026-10-07T00:00:00Z", "2026-10-06T00:00:00Z", Range{}, ErrEmptyRange},
		{"bad from", "yesterday", "", Range{}, ErrInvalidFrom},
		{"bad to", "", "2026-10-06", Range{}, ErrInvalidTo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRange(tc.from, tc.to, t0.In(time.FixedZone("x", 3600)))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !got.From.Equal(tc.want.From) || !got.To.Equal(tc.want.To) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if err == nil && (got.From.Location() != time.UTC || got.To.Location() != time.UTC) {
				t.Fatalf("range not in UTC: %v", got)
			}
		})
	}
}

func TestWindowsArePinned(t *testing.T) {
	if DefaultWindow() != 720*time.Hour || MaxWindow() != 8784*time.Hour {
		t.Fatalf("default %v, max %v", DefaultWindow(), MaxWindow())
	}
}

func TestDayWindows(t *testing.T) {
	cases := []struct {
		name string
		r    Range
		want []DayWindow
	}{
		{"whole days", Range{ts("2026-10-05T00:00:00Z"), ts("2026-10-07T00:00:00Z")}, []DayWindow{
			{ts("2026-10-05T00:00:00Z"), ts("2026-10-05T00:00:00Z"), ts("2026-10-06T00:00:00Z")},
			{ts("2026-10-06T00:00:00Z"), ts("2026-10-06T00:00:00Z"), ts("2026-10-07T00:00:00Z")},
		}},
		{"partial first and last day", Range{ts("2026-10-05T10:00:00Z"), ts("2026-10-07T06:30:00Z")}, []DayWindow{
			{ts("2026-10-05T00:00:00Z"), ts("2026-10-05T10:00:00Z"), ts("2026-10-06T00:00:00Z")},
			{ts("2026-10-06T00:00:00Z"), ts("2026-10-06T00:00:00Z"), ts("2026-10-07T00:00:00Z")},
			{ts("2026-10-07T00:00:00Z"), ts("2026-10-07T00:00:00Z"), ts("2026-10-07T06:30:00Z")},
		}},
		{"inside one day", Range{ts("2026-10-05T10:00:00Z"), ts("2026-10-05T11:00:00Z")}, []DayWindow{
			{ts("2026-10-05T00:00:00Z"), ts("2026-10-05T10:00:00Z"), ts("2026-10-05T11:00:00Z")},
		}},
		{"non-UTC bounds", Range{ts("2026-10-05T22:00:00-03:00"), ts("2026-10-06T02:00:00Z")}, []DayWindow{
			{ts("2026-10-06T00:00:00Z"), ts("2026-10-06T01:00:00Z"), ts("2026-10-06T02:00:00Z")},
		}},
		{"empty", Range{ts("2026-10-05T10:00:00Z"), ts("2026-10-05T10:00:00Z")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DayWindows(tc.r)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d windows %v, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if !got[i].Day.Equal(tc.want[i].Day) || !got[i].From.Equal(tc.want[i].From) || !got[i].To.Equal(tc.want[i].To) {
					t.Fatalf("window %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTodayWindow(t *testing.T) {
	w := TodayWindow(ts("2026-10-08T23:59:59.5Z").In(time.FixedZone("x", -3*3600)))
	if !w.Day.Equal(ts("2026-10-08T00:00:00Z")) || !w.From.Equal(w.Day) || !w.To.Equal(ts("2026-10-09T00:00:00Z")) {
		t.Fatalf("today = %+v", w)
	}
}

func TestLaterEarlierTies(t *testing.T) {
	a, b := ts("2026-10-05T00:00:00Z"), ts("2026-10-06T00:00:00Z")
	if !later(a, b).Equal(b) || !later(b, a).Equal(b) || !earlier(a, b).Equal(a) || !earlier(b, a).Equal(a) {
		t.Fatal("later/earlier picked the wrong bound")
	}
	tie := a.In(time.FixedZone("x", 3600))
	if later(tie, a).Location() != tie.Location() || earlier(tie, a).Location() != tie.Location() {
		t.Fatal("on a tie the first argument wins")
	}
}

func TestComputeFreshness(t *testing.T) {
	if f := ComputeFreshness(nil, t0); f.AsOf != nil || f.LagSeconds != nil {
		t.Fatalf("nothing applied: %+v", f)
	}
	at := t0.Add(-90 * time.Second).In(time.FixedZone("x", 3600))
	f := ComputeFreshness(&at, t0)
	if !f.AsOf.Equal(at) || f.AsOf.Location() != time.UTC || *f.LagSeconds != 90 {
		t.Fatalf("got %v %v", f.AsOf, *f.LagSeconds)
	}
	ahead := t0.Add(time.Minute)
	if f := ComputeFreshness(&ahead, t0); *f.LagSeconds != 0 {
		t.Fatalf("clock skew must read as zero lag, got %v", *f.LagSeconds)
	}
}

func TestPercentile(t *testing.T) {
	cases := []struct {
		name   string
		sample []float64
		p      float64
		want   float64
	}{
		{"empty", nil, 0.5, 0},
		{"single", []float64{7}, 0.95, 7},
		{"odd median", []float64{1, 2, 3}, 0.5, 2},
		{"even median interpolates", []float64{1, 2, 3, 4}, 0.5, 2.5},
		{"p95 of 1..100", seq(100), 0.95, 95.05},
		{"p95 of two", []float64{10, 20}, 0.95, 19.5},
		{"p0 and p100", []float64{4, 8, 9}, 0, 4},
		{"p100", []float64{4, 8, 9}, 1, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Percentile(tc.sample, tc.p); abs(got-tc.want) > 1e-9 {
				t.Fatalf("Percentile = %v, want %v", got, tc.want)
			}
		})
	}
}

func seq(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestTimingOf(t *testing.T) {
	if got := TimingOf(nil); got.Count != 0 || got.P50 != nil || got.P95 != nil {
		t.Fatalf("empty = %+v", got)
	}
	in := []float64{30, 10, 20}
	got := TimingOf(in)
	if got.Count != 3 || *got.P50 != 20 || abs(*got.P95-29) > 1e-9 {
		t.Fatalf("got %+v p50=%v p95=%v", got, *got.P50, *got.P95)
	}
	if !reflect.DeepEqual(in, []float64{30, 10, 20}) {
		t.Fatal("TimingOf must not reorder its input")
	}
	if Seconds(1500*time.Millisecond) != 1.5 {
		t.Fatal("Seconds is wrong")
	}
}

func TestKinds(t *testing.T) {
	want := []Kind{"ASNRegistered", "ASNCancelled", "DockAppointmentBooked", "DockAppointmentCheckedIn",
		"DockAppointmentCancelled", "DockAppointmentCompleted", "ReceiptOpened", "ReceiptLineReceived", "ReceiptClosed"}
	if !reflect.DeepEqual(Kinds(), want) {
		t.Fatalf("Kinds() = %v", Kinds())
	}
	for _, k := range want {
		n := 0
		for _, b := range []bool{k.IsAsn(), k.IsAppointment(), k.IsReceipt()} {
			if b {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s belongs to %d families", k, n)
		}
	}
}

func TestMilestones(t *testing.T) {
	at := t0.In(time.FixedZone("x", 3600))
	appt := map[Kind]AppointmentTimes{
		KindAppointmentBooked:    {Booked: &t0},
		KindAppointmentCheckedIn: {CheckedIn: &t0},
		KindAppointmentCancelled: {Cancelled: &t0},
		KindAppointmentCompleted: {Completed: &t0},
		KindReceiptOpened:        {},
	}
	for kind, want := range appt {
		if got := AppointmentTimesOf(Event{Kind: kind, At: at}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", kind, got, want)
		}
	}
	rcpt := map[Kind]ReceiptTimes{
		KindReceiptOpened:       {Opened: &t0},
		KindReceiptClosed:       {Closed: &t0},
		KindReceiptLineReceived: {},
		KindAsnRegistered:       {},
	}
	for kind, want := range rcpt {
		if got := ReceiptTimesOf(Event{Kind: kind, At: at}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", kind, got, want)
		}
	}
}

func valid(kind Kind) Event {
	e := Event{Kind: kind, EventID: "id-1", At: t0}
	switch {
	case kind.IsAsn():
		e.AsnNumber = "ASN-1"
	case kind.IsAppointment():
		e.AppointmentID = "appt-1"
	case kind.IsReceipt():
		e.ReceiptID, e.AsnNumber = "rcpt-1", "ASN-1"
	}
	if kind == KindReceiptLineReceived {
		e.Line = &LineReceived{LineNo: 1, SKU: "SKU-1", Quantity: 5, Condition: ConditionGood}
	}
	return e
}

// invalidEvents breaks exactly one invariant each.
var invalidEvents = map[string]func() Event{
	"unknown kind": func() Event { e := valid(KindReceiptOpened); e.Kind = "ReceiptVoided"; return e },
	"no id":        func() Event { e := valid(KindReceiptOpened); e.EventID = ""; return e },
	"no time":      func() Event { e := valid(KindReceiptOpened); e.At = time.Time{}; return e },
	"asn without number": func() Event {
		e := valid(KindAsnCancelled)
		e.AsnNumber = ""
		return e
	},
	"appointment without id": func() Event {
		e := valid(KindAppointmentBooked)
		e.AppointmentID = ""
		return e
	},
	"receipt without receipt id": func() Event {
		e := valid(KindReceiptClosed)
		e.ReceiptID = ""
		return e
	},
	"receipt without asn": func() Event {
		e := valid(KindReceiptOpened)
		e.AsnNumber = ""
		return e
	},
	"line on another kind": func() Event {
		e := valid(KindReceiptOpened)
		e.Line = &LineReceived{LineNo: 1, Quantity: 1, Condition: ConditionGood}
		return e
	},
	"discrepancies on another kind": func() Event {
		e := valid(KindReceiptOpened)
		e.Discrepancies = []Discrepancy{{LineNo: 1, Kind: DiscrepancyShort}}
		return e
	},
	"line kind without a line": func() Event { e := valid(KindReceiptLineReceived); e.Line = nil; return e },
	"line_no 0": func() Event {
		e := valid(KindReceiptLineReceived)
		e.Line.LineNo = 0
		return e
	},
	"quantity 0": func() Event {
		e := valid(KindReceiptLineReceived)
		e.Line.Quantity = 0
		return e
	},
	"unknown condition": func() Event {
		e := valid(KindReceiptLineReceived)
		e.Line.Condition = "Lost"
		return e
	},
	"discrepancy line_no 0": func() Event {
		e := valid(KindReceiptClosed)
		e.Discrepancies = []Discrepancy{{LineNo: 0, Kind: DiscrepancyShort}}
		return e
	},
	"unknown discrepancy kind": func() Event {
		e := valid(KindReceiptClosed)
		e.Discrepancies = []Discrepancy{{LineNo: 1, Kind: "Wrong"}}
		return e
	},
}

func TestValidate(t *testing.T) {
	for _, k := range Kinds() {
		if err := valid(k).Validate(); err != nil {
			t.Errorf("valid %s rejected: %v", k, err)
		}
	}
	closed := valid(KindReceiptClosed)
	closed.Discrepancies = []Discrepancy{{LineNo: 1, SKU: "S", Kind: DiscrepancyShort}, {LineNo: 1, SKU: "S", Kind: DiscrepancyDamaged}, {LineNo: 2, SKU: "T", Kind: DiscrepancyOver}}
	if err := closed.Validate(); err != nil {
		t.Errorf("a close with all three kinds is valid: %v", err)
	}
	for name, build := range invalidEvents {
		if err := build().Validate(); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestConditionAndKindValidity(t *testing.T) {
	if !ConditionGood.Valid() || !ConditionDamaged.Valid() || Condition("x").Valid() {
		t.Fatal("Condition.Valid is wrong")
	}
	if !DiscrepancyShort.Valid() || !DiscrepancyOver.Valid() || !DiscrepancyDamaged.Valid() || DiscrepancyKind("x").Valid() {
		t.Fatal("DiscrepancyKind.Valid is wrong")
	}
}
