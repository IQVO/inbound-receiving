package analyticsstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/analytics/report"
)

// Projection is the Postgres WRITER (report.Projection). Apply claims the
// event id and folds the event into the fact tables in ONE transaction: the
// id is recorded if and only if its effect is, so a failure anywhere leaves
// nothing behind and the redelivered event is applied afresh, while a replay
// of an applied id is a no-op.
type Projection struct {
	pool *pgxpool.Pool
}

// NewProjection constructs a Projection over the writer pool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

var _ report.Projection = (*Projection)(nil)

const claimSQL = `
	INSERT INTO analytics_processed_events (event_id, event_type, occurred_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (event_id) DO NOTHING`

// Every fact upsert keeps the EARLIEST candidate of each milestone: LEAST
// ignores NULLs, so a NULL parameter (the event does not mark that milestone)
// keeps the stored value, and the row converges whatever order the events
// arrive in.

const upsertAsnSQL = `
	INSERT INTO asn_facts (asn_number, expected_arrival, registered_at, cancelled_at, receipt_opened_at)
	VALUES ($1, $2::timestamptz, $3::timestamptz, $4::timestamptz, $5::timestamptz)
	ON CONFLICT (asn_number) DO UPDATE SET
		expected_arrival  = COALESCE(asn_facts.expected_arrival, EXCLUDED.expected_arrival),
		registered_at     = LEAST(asn_facts.registered_at, EXCLUDED.registered_at),
		cancelled_at      = LEAST(asn_facts.cancelled_at, EXCLUDED.cancelled_at),
		receipt_opened_at = LEAST(asn_facts.receipt_opened_at, EXCLUDED.receipt_opened_at)`

const upsertAppointmentSQL = `
	INSERT INTO appointment_facts (appointment_id, booked_at, checked_in_at, cancelled_at, completed_at)
	VALUES ($1, $2::timestamptz, $3::timestamptz, $4::timestamptz, $5::timestamptz)
	ON CONFLICT (appointment_id) DO UPDATE SET
		booked_at     = LEAST(appointment_facts.booked_at, EXCLUDED.booked_at),
		checked_in_at = LEAST(appointment_facts.checked_in_at, EXCLUDED.checked_in_at),
		cancelled_at  = LEAST(appointment_facts.cancelled_at, EXCLUDED.cancelled_at),
		completed_at  = LEAST(appointment_facts.completed_at, EXCLUDED.completed_at)`

const upsertReceiptSQL = `
	INSERT INTO receipt_facts (receipt_id, asn_number, opened_at, closed_at)
	VALUES ($1, $2, $3::timestamptz, $4::timestamptz)
	ON CONFLICT (receipt_id) DO UPDATE SET
		opened_at = LEAST(receipt_facts.opened_at, EXCLUDED.opened_at),
		closed_at = LEAST(receipt_facts.closed_at, EXCLUDED.closed_at)`

const insertLineSQL = `
	INSERT INTO receipt_line_events (event_id, receipt_id, line_no, sku, quantity, condition, occurred_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	ON CONFLICT (event_id) DO NOTHING`

const insertDiscrepancySQL = `
	INSERT INTO receipt_discrepancies (receipt_id, line_no, kind, sku, closed_at)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (receipt_id, line_no, kind) DO NOTHING`

// Apply implements report.Projection.
func (p *Projection) Apply(ctx context.Context, e report.Event) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analyticsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, claimSQL, e.EventID, string(e.Kind), e.At.UTC())
	if err != nil {
		return false, classify(fmt.Errorf("analyticsstore: claim event %s: %w", e.EventID, err))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already applied: the deferred rollback ends the empty tx
	}
	if err := fold(ctx, tx, e); err != nil {
		return false, classify(fmt.Errorf("analyticsstore: project %s %s: %w", e.Kind, e.EventID, err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analyticsstore: commit: %w", err)
	}
	return true, nil
}

// fold writes the facts of one event inside tx.
func fold(ctx context.Context, tx pgx.Tx, e report.Event) error {
	at := e.At.UTC()
	switch {
	case e.Kind.IsAsn():
		var registered, cancelled *time.Time
		var expected *time.Time
		if e.Kind == report.KindAsnRegistered {
			registered = &at
			if e.ExpectedArrival != nil {
				u := e.ExpectedArrival.UTC()
				expected = &u
			}
		} else {
			cancelled = &at
		}
		_, err := tx.Exec(ctx, upsertAsnSQL, e.AsnNumber, expected, registered, cancelled, nil)
		return err
	case e.Kind.IsAppointment():
		t := report.AppointmentTimesOf(e)
		_, err := tx.Exec(ctx, upsertAppointmentSQL, e.AppointmentID, t.Booked, t.CheckedIn, t.Cancelled, t.Completed)
		return err
	default:
		return foldReceipt(ctx, tx, e)
	}
}

func foldReceipt(ctx context.Context, tx pgx.Tx, e report.Event) error {
	t := report.ReceiptTimesOf(e)
	if _, err := tx.Exec(ctx, upsertReceiptSQL, e.ReceiptID, e.AsnNumber, t.Opened, t.Closed); err != nil {
		return err
	}
	switch e.Kind {
	case report.KindReceiptOpened:
		_, err := tx.Exec(ctx, upsertAsnSQL, e.AsnNumber, nil, nil, nil, t.Opened)
		return err
	case report.KindReceiptLineReceived:
		l := e.Line
		_, err := tx.Exec(ctx, insertLineSQL, e.EventID, e.ReceiptID, l.LineNo, l.SKU, l.Quantity, string(l.Condition), e.At.UTC())
		return err
	case report.KindReceiptClosed:
		for _, d := range e.Discrepancies {
			if _, err := tx.Exec(ctx, insertDiscrepancySQL, e.ReceiptID, d.LineNo, string(d.Kind), d.SKU, e.At.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

// classify wraps a Postgres data-exception (SQLSTATE class 22) or
// integrity-violation (class 23) error in report.ErrRejected: the same event
// can never succeed, so the consumer dead-letters it instead of retrying
// forever. Everything else (connection loss, timeouts, locks, failovers)
// stays transient.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
		return fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	return err
}
