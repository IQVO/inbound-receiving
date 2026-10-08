//go:build integration

package analyticsstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// startAnalyticsDB boots a real Postgres via testcontainers, applies the
// embedded analytical migrations (twice: boot runs them every start) and
// returns the DSN and an open writer pool.
func startAnalyticsDB(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_receiving_analytics"),
		tcpostgres.WithUsername("analytics"),
		tcpostgres.WithPassword("analytics"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := RunMigrations(url); err != nil {
			t.Fatalf("analytics migrations (run %d): %v", i+1, err)
		}
	}
	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return url, pool
}

// pgStore is the Projection and the Reader over one database.
type pgStore struct {
	*Projection
	*Reader
}

func TestPostgres_Contract(t *testing.T) {
	runContract(t, func(t *testing.T) store {
		_, pool := startAnalyticsDB(t)
		return pgStore{NewProjection(pool), NewReader(pool)}
	})
}

// TestPostgres_DedupeMarkAndEffectAreAtomic: a projection failure after the
// claim (a trigger raising on receipt_facts) leaves neither the mark nor the
// effect, and the error is transient (not ErrRejected) for a non-22/23 code.
func TestPostgres_DedupeMarkAndEffectAreAtomic(t *testing.T) {
	ctx := context.Background()
	_, pool := startAnalyticsDB(t)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION boom() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'boom' USING ERRCODE = '57P01'; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER boom BEFORE INSERT ON receipt_facts FOR EACH ROW EXECUTE FUNCTION boom();`); err != nil {
		t.Fatal(err)
	}
	p := NewProjection(pool)
	e := rcptEv("e1", report.KindReceiptOpened, "R-1", "ASN-1", "2026-10-05T10:00:00Z")
	_, err := p.Apply(ctx, e)
	if err == nil || errors.Is(err, report.ErrRejected) {
		t.Fatalf("err = %v, want a transient (non-rejected) error", err)
	}
	var marks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM analytics_processed_events`).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("marks = %d, %v; the mark must roll back with the effect", marks, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER boom ON receipt_facts`); err != nil {
		t.Fatal(err)
	}
	if applied, err := p.Apply(ctx, e); err != nil || !applied {
		t.Fatalf("redelivery after the failure = %v, %v; want applied", applied, err)
	}
}

// TestPostgres_ReadOnlyPoolCannotWrite: the reports pool refuses writes even
// on a read-write role.
func TestPostgres_ReadOnlyPoolCannotWrite(t *testing.T) {
	ctx := context.Background()
	url, _ := startAnalyticsDB(t)
	ro, err := NewReadOnlyPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := NewProjection(ro).Apply(ctx, closedEv("x", "R-1", "2026-10-05T10:00:00Z")); err == nil {
		t.Fatal("a write through the read-only pool succeeded")
	}
	if _, err := NewReader(ro).Current(ctx, report.TodayWindow(at("2026-10-05T10:00:00Z"))); err != nil {
		t.Fatalf("a read through the read-only pool failed: %v", err)
	}
}

// TestPostgres_DayWindowsMatchTheGoDefinition: the SQL day series equals
// report.DayWindows for awkward ranges (partial days, a session time zone
// with DST, a single instant-wide window).
func TestPostgres_DayWindowsMatchTheGoDefinition(t *testing.T) {
	ctx := context.Background()
	_, pool := startAnalyticsDB(t)
	if _, err := pool.Exec(ctx, `ALTER DATABASE inbound_receiving_analytics SET timezone TO 'America/Sao_Paulo'`); err != nil {
		t.Fatal(err)
	}
	pool.Reset() // new connections pick up the time zone
	r := NewReader(pool)
	for _, rg := range []report.Range{
		{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-07T06:30:00Z")},
		{From: at("2026-10-05T00:00:00Z"), To: at("2026-10-06T00:00:00Z")},
		{From: at("2026-02-27T13:00:00Z"), To: at("2026-03-02T00:00:01Z")},
		{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-05T10:00:01Z")},
	} {
		got, err := r.PerformanceDays(ctx, rg)
		if err != nil {
			t.Fatal(err)
		}
		want := report.DayWindows(rg)
		if len(got) != len(want) {
			t.Fatalf("%v: %d SQL days, %d Go windows", rg, len(got), len(want))
		}
		for i := range got {
			if !got[i].Day.Equal(want[i].Day) {
				t.Fatalf("%v: day %d = %v, want %v", rg, i, got[i].Day, want[i].Day)
			}
		}
	}
}
