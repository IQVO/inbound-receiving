package analyticsstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// Reader is the Postgres READER (report.Reader). Days are UTC calendar days;
// every window is half-open [lo, hi). SQL counts, sums and takes percentiles
// (percentile_cont, the definition report.Percentile mirrors); the range
// rules and the freshness lag live in internal/analytics/report.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader constructs a Reader over the (read-only) reports pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

var _ report.Reader = (*Reader)(nil)

// performanceDaysSQL builds the same windows as report.DayWindows: one per
// UTC day intersecting [$1, $2), clipped to the range. The day series runs
// over UTC-naive timestamps so a session time zone with DST cannot shift a
// day. A negative duration (events recorded out of order) counts as zero.
const performanceDaysSQL = `
	WITH days AS (
		SELECT d::date AS day,
		       GREATEST(d AT TIME ZONE 'UTC', $1::timestamptz)                    AS lo,
		       LEAST((d + interval '1 day') AT TIME ZONE 'UTC', $2::timestamptz) AS hi
		FROM generate_series(
		         date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC'),
		         ($2::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC',
		         interval '1 day') AS d
		WHERE $1::timestamptz < $2::timestamptz
	)
	SELECT days.day,
	       (SELECT count(*) FROM receipt_facts r WHERE r.closed_at >= days.lo AND r.closed_at < days.hi),
	       (SELECT count(*) FROM receipt_line_events l WHERE l.occurred_at >= days.lo AND l.occurred_at < days.hi),
	       (SELECT COALESCE(sum(l.quantity), 0)::bigint FROM receipt_line_events l
	         WHERE l.condition = 'Good' AND l.occurred_at >= days.lo AND l.occurred_at < days.hi),
	       (SELECT COALESCE(sum(l.quantity), 0)::bigint FROM receipt_line_events l
	         WHERE l.condition = 'Damaged' AND l.occurred_at >= days.lo AND l.occurred_at < days.hi),
	       (SELECT count(*) FROM receipt_discrepancies x WHERE x.kind = 'Short'   AND x.closed_at >= days.lo AND x.closed_at < days.hi),
	       (SELECT count(*) FROM receipt_discrepancies x WHERE x.kind = 'Over'    AND x.closed_at >= days.lo AND x.closed_at < days.hi),
	       (SELECT count(*) FROM receipt_discrepancies x WHERE x.kind = 'Damaged' AND x.closed_at >= days.lo AND x.closed_at < days.hi),
	       cycle.n, cycle.p50, cycle.p95,
	       (SELECT count(*) FROM appointment_facts a WHERE a.booked_at     >= days.lo AND a.booked_at     < days.hi),
	       (SELECT count(*) FROM appointment_facts a WHERE a.checked_in_at >= days.lo AND a.checked_in_at < days.hi),
	       (SELECT count(*) FROM appointment_facts a WHERE a.cancelled_at  >= days.lo AND a.cancelled_at  < days.hi),
	       (SELECT count(*) FROM appointment_facts a WHERE a.completed_at  >= days.lo AND a.completed_at  < days.hi),
	       dwell.n, dwell.p50, dwell.p95,
	       d2s.n, d2s.p50, d2s.p90, d2s.under,
	       adv.n, adv.within
	FROM days
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY s.secs) AS p50,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY s.secs) AS p95
		FROM (SELECT GREATEST(0, EXTRACT(EPOCH FROM r.closed_at - r.opened_at))::float8 AS secs
		      FROM receipt_facts r
		      WHERE r.opened_at IS NOT NULL AND r.closed_at >= days.lo AND r.closed_at < days.hi) s
	) cycle
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY s.secs) AS p50,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY s.secs) AS p95
		FROM (SELECT GREATEST(0, EXTRACT(EPOCH FROM a.completed_at - a.checked_in_at))::float8 AS secs
		      FROM appointment_facts a
		      WHERE a.checked_in_at IS NOT NULL AND a.completed_at >= days.lo AND a.completed_at < days.hi) s
	) dwell
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY s.secs) AS p50,
		       percentile_cont(0.90) WITHIN GROUP (ORDER BY s.secs) AS p90,
		       count(*) FILTER (WHERE s.secs <= $3) AS under
		FROM (SELECT f.dock_to_stock_seconds::float8 AS secs
		      FROM dock_to_stock_facts f
		      WHERE f.stowed_at >= days.lo AND f.stowed_at < days.hi) s
	) d2s
	CROSS JOIN LATERAL (
		SELECT count(*) AS n, count(*) FILTER (WHERE f.within_sla) AS within
		FROM advice_facts f
		WHERE f.issued_at >= days.lo AND f.issued_at < days.hi
	) adv
	ORDER BY days.day`

// timelinessDaysSQL builds the same day windows as performanceDaysSQL and
// folds the day's ASNs per supplier for the asn_timeliness block (ADR 0025):
// registered in the UTC day of the ASN's registered_at, late the subset with
// an asn_late_facts row. A day with no registration reports no rows.
const timelinessDaysSQL = `
	WITH days AS (
		SELECT d::date AS day,
		       GREATEST(d AT TIME ZONE 'UTC', $1::timestamptz)                    AS lo,
		       LEAST((d + interval '1 day') AT TIME ZONE 'UTC', $2::timestamptz) AS hi
		FROM generate_series(
		         date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC'),
		         ($2::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC',
		         interval '1 day') AS d
		WHERE $1::timestamptz < $2::timestamptz
	)
	SELECT days.day, COALESCE(a.supplier_ref, ''), count(a.asn_number), count(l.asn_number) FILTER (WHERE l.asn_number IS NOT NULL)
	FROM days
	JOIN asn_facts a ON a.registered_at >= days.lo AND a.registered_at < days.hi
	LEFT JOIN asn_late_facts l ON l.asn_number = a.asn_number
	GROUP BY days.day, COALESCE(a.supplier_ref, '')
	ORDER BY days.day, COALESCE(a.supplier_ref, '')`

// readTimeliness fills each day's AsnTimeliness block (ADR 0025), bounded by
// report.MaxTimelinessSuppliers.
func (r *Reader) readTimeliness(ctx context.Context, rg report.Range, days []report.PerformanceDay) error {
	byDay := map[string][]report.SupplierTimeliness{}
	rows, err := r.pool.Query(ctx, timelinessDaysSQL, rg.From.UTC(), rg.To.UTC())
	if err != nil {
		return fmt.Errorf("analyticsstore: asn timeliness days: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var row report.SupplierTimeliness
		if err := rows.Scan(&day, &row.SupplierRef, &row.Registered, &row.Late); err != nil {
			return fmt.Errorf("analyticsstore: scan asn timeliness: %w", err)
		}
		if row.Registered > 0 {
			share := 1 - float64(row.Late)/float64(row.Registered)
			row.ShareOnTime = &share
		}
		byDay[day.UTC().Format("2006-01-02")] = append(byDay[day.UTC().Format("2006-01-02")], row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("analyticsstore: asn timeliness rows: %w", err)
	}
	for i := range days {
		days[i].AsnTimeliness = boundedTimeliness(byDay[days[i].Day.UTC().Format("2006-01-02")])
	}
	return nil
}

// boundedTimeliness ranks the day's suppliers exactly like
// report.AsnTimelinessOf: biggest registered count first, ties broken by
// supplier_ref ascending (the SQL only groups; the rank is defined once), then
// keeps the top MaxTimelinessSuppliers.
func boundedTimeliness(rows []report.SupplierTimeliness) []report.SupplierTimeliness {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Registered != rows[j].Registered {
			return rows[i].Registered > rows[j].Registered
		}
		return rows[i].SupplierRef < rows[j].SupplierRef
	})
	if len(rows) > report.MaxTimelinessSuppliers {
		rows = rows[:report.MaxTimelinessSuppliers]
	}
	return rows
}

// PerformanceDays implements report.Reader.
func (r *Reader) PerformanceDays(ctx context.Context, rg report.Range) ([]report.PerformanceDay, error) {
	rows, err := r.pool.Query(ctx, performanceDaysSQL, rg.From.UTC(), rg.To.UTC(), report.DockToStockTargetSeconds)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: performance days: %w", err)
	}
	defer rows.Close()
	out := []report.PerformanceDay{}
	for rows.Next() {
		var d report.PerformanceDay
		var d2s struct {
			count, under int
			p50, p90     *float64
		}
		var adv struct{ count, within int }
		if err := rows.Scan(&d.Day, &d.ReceiptsClosed, &d.LinesReceived, &d.UnitsGood, &d.UnitsDamaged,
			&d.Discrepancies.Short, &d.Discrepancies.Over, &d.Discrepancies.Damaged,
			&d.ReceiptCycle.Count, &d.ReceiptCycle.P50, &d.ReceiptCycle.P95,
			&d.Appointments.Booked, &d.Appointments.CheckedIn, &d.Appointments.Cancelled, &d.Appointments.Completed,
			&d.DockDwell.Count, &d.DockDwell.P50, &d.DockDwell.P95,
			&d2s.count, &d2s.p50, &d2s.p90, &d2s.under, &adv.count, &adv.within); err != nil {
			return nil, fmt.Errorf("analyticsstore: scan performance day: %w", err)
		}
		d.DockToStock = report.NewDockToStock(d2s.count, d2s.p50, d2s.p90, d2s.under)
		d.Advice = report.NewAdviceSummary(adv.count, adv.within)
		y, m, dd := d.Day.Date()
		d.Day = time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: performance days rows: %w", err)
	}
	if err := r.readTimeliness(ctx, rg, out); err != nil {
		return nil, err
	}
	return out, nil
}

const currentSQL = `
	SELECT (SELECT count(*) FROM receipt_facts WHERE opened_at IS NOT NULL AND closed_at IS NULL),
	       count(*) FILTER (WHERE a.cancelled_at IS NULL AND a.expected_arrival >= $1 AND a.expected_arrival < $2),
	       count(*) FILTER (WHERE a.cancelled_at IS NULL AND a.expected_arrival >= $1 AND a.expected_arrival < $2
	                          AND a.receipt_opened_at IS NOT NULL)
	FROM asn_facts a`

// Current implements report.Reader.
func (r *Reader) Current(ctx context.Context, today report.DayWindow) (report.CurrentCounts, error) {
	var c report.CurrentCounts
	if err := r.pool.QueryRow(ctx, currentSQL, today.From.UTC(), today.To.UTC()).Scan(&c.OpenReceipts, &c.ExpectedToday, &c.ExpectedTodayStarted); err != nil {
		return report.CurrentCounts{}, fmt.Errorf("analyticsstore: current: %w", err)
	}
	return c, nil
}

// LastEventAt implements report.Reader: the newest CloudEvents time among the
// applied events, nil while none was applied.
func (r *Reader) LastEventAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT max(occurred_at) FROM analytics_processed_events`).Scan(&at); err != nil {
		return nil, fmt.Errorf("analyticsstore: last event: %w", err)
	}
	if at != nil {
		utc := at.UTC()
		at = &utc
	}
	return at, nil
}
