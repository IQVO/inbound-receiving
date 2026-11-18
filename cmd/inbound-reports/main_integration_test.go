//go:build integration

package main

// The real entry point of cmd/inbound-reports against a real analytical
// Postgres started with testcontainers (never skip-gated, never an external
// DSN): the EMPTY database is migrated with the repo's own analytics
// migrations (the reports binary runs none -- docs/docs/operations/
// configuration.md: "No migrations run here"; the projector's boot owns
// them), the documented facts are seeded through the repo's own analytics
// writer (analyticsstore.Projection, the exact port cmd/inbound-projector
// drives), then run() serves the documented probe and reports over its
// read-only pool and a SIGTERM drains it cleanly.
//
// Every assertion traces to documented behaviour:
//   - GET /healthz, GET /reports/receiving-performance and GET
//     /reports/freshness on HTTP_ADDR (cmd/inbound-reports/main.go,
//     docs/docs/operations/configuration.md);
//   - the day block of ADR 0006: receipts closed, lines and units received
//     (good vs damaged), Short/Over/Damaged discrepancies, the open-to-close
//     receipt cycle, the dock-to-stock fact of ADR 0021 against the WERC
//     target of 3.1 h (11160 s), the receiving advice SLA share of ADR 0023
//     and the per-supplier ASN timeliness block of ADR 0025;
//   - `current` counts the open receipts and the ASNs expected today
//     (report.Reader.Current);
//   - /reports/freshness answers the CloudEvents time of the newest applied
//     event and the lag behind it, both null before the first event
//     (report.ComputeFreshness);
//   - an unparsable range is the documented 400 invalid-report-range
//     (internal/adapters/inbound/http/reports_handler.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

func startEmptyPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_analytics"),
		tcpostgres.WithUsername("itest"), tcpostgres.WithPassword("itest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return url
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func getJSON(t *testing.T, url string, into any) int {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // probe of the binary under test
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s (%s): %v", url, body, err)
	}
	return resp.StatusCode
}

// The wire shape of the receiving performance report (snake_case, days are
// UTC calendar dates, nullable numbers stay null on the wire).
type itTiming struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type itDockToStock struct {
	Count            int      `json:"count"`
	P50Seconds       *float64 `json:"p50_seconds"`
	P90Seconds       *float64 `json:"p90_seconds"`
	UnderTarget      int      `json:"under_target"`
	ShareUnderTarget *float64 `json:"share_under_target"`
}

type itDay struct {
	Day            string `json:"day"`
	ReceiptsClosed int    `json:"receipts_closed"`
	LinesReceived  int    `json:"lines_received"`
	UnitsGood      int64  `json:"units_good"`
	UnitsDamaged   int64  `json:"units_damaged"`
	Discrepancies  struct {
		Short   int `json:"short"`
		Over    int `json:"over"`
		Damaged int `json:"damaged"`
	} `json:"discrepancies"`
	ReceiptCycle itTiming `json:"receipt_cycle"`
	Appointments struct {
		Booked    int `json:"booked"`
		CheckedIn int `json:"checked_in"`
		Cancelled int `json:"cancelled"`
		Completed int `json:"completed"`
	} `json:"appointments"`
	DockDwell   itTiming      `json:"dock_dwell"`
	DockToStock itDockToStock `json:"dock_to_stock"`
	Advice      struct {
		Count          int      `json:"count"`
		WithinSLA      int      `json:"within_sla"`
		ShareWithinSLA *float64 `json:"share_within_sla"`
	} `json:"advice"`
	AsnTimeliness []struct {
		SupplierRef string   `json:"supplier_ref"`
		Registered  int      `json:"registered"`
		Late        int      `json:"late"`
		ShareOnTime *float64 `json:"share_on_time"`
	} `json:"asn_timeliness"`
}

type itReport struct {
	DockToStockTargetSeconds int     `json:"dock_to_stock_target_seconds"`
	Days                     []itDay `json:"days"`
	Current                  struct {
		OpenReceipts         int `json:"open_receipts"`
		ExpectedToday        int `json:"expected_today"`
		ExpectedTodayStarted int `json:"expected_today_started"`
	} `json:"current"`
}

func (r itReport) day(t *testing.T, day string) itDay {
	t.Helper()
	for _, d := range r.Days {
		if d.Day == day {
			return d
		}
	}
	t.Fatalf("the report has no day %s (days: %d)", day, len(r.Days))
	return itDay{}
}

func TestReportsServeTheSeededProjectionAndStopCleanly(t *testing.T) {
	url := startEmptyPostgres(t)
	// The analytical migrations of the projector's boot: the reports binary
	// runs none itself.
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writer, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	projection := analyticsstore.NewProjection(writer)

	// A closed receiving story over two fixed UTC days, seeded through the
	// repo's own analytics writer (the projector's port).
	regAt := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	openAt := time.Date(2026, 11, 11, 8, 5, 0, 0, time.UTC)
	line1At := time.Date(2026, 11, 11, 8, 10, 0, 0, time.UTC)
	line2At := time.Date(2026, 11, 11, 8, 20, 0, 0, time.UTC)
	adviceAt := time.Date(2026, 11, 11, 9, 31, 0, 0, time.UTC)
	closeAt := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	stowAt := time.Date(2026, 11, 11, 9, 50, 0, 0, time.UTC)
	expected := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	seed := []report.Event{
		{Kind: report.KindAsnRegistered, EventID: "evt-rep-1", At: regAt, AsnNumber: "ASN-REP-1",
			SupplierRef: "ACME", ExpectedArrival: &expected},
		{Kind: report.KindReceiptOpened, EventID: "evt-rep-2", At: openAt, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1"},
		{Kind: report.KindReceiptLineReceived, EventID: "evt-rep-3", At: line1At, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1",
			Line: &report.LineReceived{LineNo: 1, SKU: "SKU-REP-1", Quantity: 8, Condition: report.ConditionGood}},
		{Kind: report.KindReceiptLineReceived, EventID: "evt-rep-4", At: line2At, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1",
			Line: &report.LineReceived{LineNo: 2, SKU: "SKU-REP-2", Quantity: 3, Condition: report.ConditionDamaged}},
		{Kind: report.KindReceiptClosed, EventID: "evt-rep-5", At: closeAt, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1",
			Discrepancies: []report.Discrepancy{
				{LineNo: 1, SKU: "SKU-REP-1", Kind: report.DiscrepancyShort},
				{LineNo: 2, SKU: "SKU-REP-2", Kind: report.DiscrepancyOver},
			}},
		{Kind: report.KindReceivingAdviceIssued, EventID: "evt-rep-6", At: adviceAt, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1",
			Advice: &report.AdviceIssued{WithinSLA: true}},
		{Kind: report.KindReceiptStowCompleted, EventID: "evt-rep-7", At: stowAt, AsnNumber: "ASN-REP-1", ReceiptID: "RCPT-REP-1",
			Stow: &report.StowCompleted{ArrivedAt: time.Date(2026, 11, 11, 7, 50, 0, 0, time.UTC), StowedAt: stowAt,
				DockToStockSeconds: 7200, CartonCount: 3}},
	}
	// The today block of `current`: an ASN expected today whose receipt is
	// open (and, with it, a deterministic newest applied event time).
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayOpen := today.Add(6 * time.Hour)
	expectedToday := today.Add(9 * time.Hour)
	seed = append(seed,
		report.Event{Kind: report.KindAsnRegistered, EventID: "evt-rep-today-1", At: todayOpen, AsnNumber: "ASN-REP-TODAY",
			SupplierRef: "ACME", ExpectedArrival: &expectedToday},
		report.Event{Kind: report.KindReceiptOpened, EventID: "evt-rep-today-2", At: todayOpen, AsnNumber: "ASN-REP-TODAY", ReceiptID: "RCPT-REP-TODAY"},
	)
	for _, e := range seed {
		applied, err := projection.Apply(ctx, e)
		if err != nil || !applied {
			t.Fatalf("seed %s %s: applied=%v err=%v", e.Kind, e.EventID, applied, err)
		}
	}

	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", url)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")

	done := make(chan error, 1)
	go func() { done <- run() }()

	// The documented probe: /healthz on the reports listener.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if httpOK("http://" + addr + "/healthz") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("/healthz never answered 200")
		}
	}

	// The documented report, over the range covering both fixed days and
	// today.
	var perf itReport
	getJSON(t, fmt.Sprintf("http://%s/reports/receiving-performance?from=2026-11-10T00:00:00Z&to=%s",
		addr, today.AddDate(0, 0, 1).Format(time.RFC3339)), &perf)
	if perf.DockToStockTargetSeconds != 11160 {
		t.Errorf("dock_to_stock_target_seconds = %d, want 11160 (the WERC 3.1 h target, ADR 0021)", perf.DockToStockTargetSeconds)
	}
	reg := perf.day(t, "2026-11-10")
	if reg.ReceiptsClosed != 0 || reg.LinesReceived != 0 || reg.UnitsGood != 0 || reg.UnitsDamaged != 0 ||
		reg.Discrepancies.Short != 0 || reg.Discrepancies.Over != 0 || reg.Discrepancies.Damaged != 0 ||
		reg.ReceiptCycle.Count != 0 || reg.DockToStock.Count != 0 || reg.Advice.Count != 0 {
		t.Errorf("2026-11-10 = %+v, want every counter zero (only an ASN was registered)", reg)
	}
	if len(reg.AsnTimeliness) != 1 || reg.AsnTimeliness[0].SupplierRef != "ACME" ||
		reg.AsnTimeliness[0].Registered != 1 || reg.AsnTimeliness[0].Late != 0 ||
		reg.AsnTimeliness[0].ShareOnTime == nil || *reg.AsnTimeliness[0].ShareOnTime != 1 {
		t.Errorf("2026-11-10 asn_timeliness = %+v, want ACME 1 registered 0 late share 1 (ADR 0025)", reg.AsnTimeliness)
	}
	got := perf.day(t, "2026-11-11")
	if got.ReceiptsClosed != 1 || got.LinesReceived != 2 || got.UnitsGood != 8 || got.UnitsDamaged != 3 {
		t.Errorf("2026-11-11 counts = closed %d lines %d good %d damaged %d, want 1 2 8 3",
			got.ReceiptsClosed, got.LinesReceived, got.UnitsGood, got.UnitsDamaged)
	}
	if got.Discrepancies.Short != 1 || got.Discrepancies.Over != 1 || got.Discrepancies.Damaged != 0 {
		t.Errorf("2026-11-11 discrepancies = %+v, want short 1 over 1 damaged 0", got.Discrepancies)
	}
	// 08:05 -> 09:30 is 5100 s; one sample, so p50 = p95 = 5100.
	if got.ReceiptCycle.Count != 1 || got.ReceiptCycle.P50Seconds == nil || *got.ReceiptCycle.P50Seconds != 5100 ||
		got.ReceiptCycle.P95Seconds == nil || *got.ReceiptCycle.P95Seconds != 5100 {
		t.Errorf("2026-11-11 receipt_cycle = %+v, want count 1 p50 p95 5100", got.ReceiptCycle)
	}
	// The stow fact: 7200 s, under the 11160 s target, so share 1.
	if got.DockToStock.Count != 1 || got.DockToStock.P50Seconds == nil || *got.DockToStock.P50Seconds != 7200 ||
		got.DockToStock.P90Seconds == nil || *got.DockToStock.P90Seconds != 7200 ||
		got.DockToStock.UnderTarget != 1 || got.DockToStock.ShareUnderTarget == nil || *got.DockToStock.ShareUnderTarget != 1 {
		t.Errorf("2026-11-11 dock_to_stock = %+v, want count 1 p50 p90 7200 under 1 share 1 (ADR 0021)", got.DockToStock)
	}
	if got.Advice.Count != 1 || got.Advice.WithinSLA != 1 || got.Advice.ShareWithinSLA == nil || *got.Advice.ShareWithinSLA != 1 {
		t.Errorf("2026-11-11 advice = %+v, want count 1 within 1 share 1 (ADR 0023)", got.Advice)
	}
	if len(got.AsnTimeliness) != 0 {
		t.Errorf("2026-11-11 asn_timeliness = %+v, want empty (no ASN registered that day)", got.AsnTimeliness)
	}
	// `current`: the today ASN is expected today and its receipt is open.
	if perf.Current.OpenReceipts != 1 || perf.Current.ExpectedToday != 1 || perf.Current.ExpectedTodayStarted != 1 {
		t.Errorf("current = %+v, want open 1 expected today 1 started 1", perf.Current)
	}

	// The documented freshness probe: the newest applied event time and a
	// non-negative lag behind it.
	var freshness struct {
		AsOf       *time.Time `json:"as_of"`
		LagSeconds *float64   `json:"lag_seconds"`
	}
	getJSON(t, "http://"+addr+"/reports/freshness", &freshness)
	if freshness.AsOf == nil || !freshness.AsOf.Equal(todayOpen) {
		t.Errorf("freshness as_of = %v, want %v (the newest seeded event)", freshness.AsOf, todayOpen)
	}
	if freshness.LagSeconds == nil || *freshness.LagSeconds < 0 {
		t.Errorf("freshness lag_seconds = %v, want a non-negative number", freshness.LagSeconds)
	}

	// The documented 400 of an unparsable range.
	resp, err := http.Get("http://" + addr + "/reports/receiving-performance?from=not-a-timestamp") //nolint:noctx // probe of the binary under test
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(resp.Header.Get("Content-Type"), "application/problem+json") ||
		!strings.Contains(string(body), "invalid-report-range") {
		t.Errorf("an unparsable range = %d %s, want 400 invalid-report-range problem+json", resp.StatusCode, body)
	}

	// SIGTERM is the documented drain: the server stops inside the shutdown
	// timeout and run returns nil.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a SIGTERM'd reports server must drain cleanly, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the reports server did not stop")
	}
}

func httpOK(url string) bool {
	resp, err := http.Get(url) //nolint:noctx // probe of the binary under test
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
