package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
)

// AppointmentRepo is the pgx-backed appointments local copy over
// appointments and appointment_asns (ADR 0019): the read side
// (ports.AppointmentRepository), the consumer's write side
// (ports.AppointmentStore) and the stage B backfill's reader
// (ports.AppointmentHistory). Every write is version-guarded: a change
// applies only when its version is greater than the stored one.
type AppointmentRepo struct {
	pool *pgxpool.Pool
}

// NewAppointmentRepo constructs an AppointmentRepo over pool.
func NewAppointmentRepo(pool *pgxpool.Pool) *AppointmentRepo { return &AppointmentRepo{pool: pool} }

var (
	_ ports.AppointmentRepository = (*AppointmentRepo)(nil)
	_ ports.AppointmentStore      = (*AppointmentRepo)(nil)
	_ ports.AppointmentHistory    = (*AppointmentRepo)(nil)
)

const appointmentColumns = `id, door_code, carrier, window_start, window_end, state, version`

type appointmentRow struct {
	id          string
	doorCode    string
	carrier     string
	windowStart time.Time
	windowEnd   time.Time
	state       string
	version     int64
}

func (r *appointmentRow) targets() []any {
	return []any{&r.id, &r.doorCode, &r.carrier, &r.windowStart, &r.windowEnd, &r.state, &r.version}
}

func (r appointmentRow) toAppointment(asnNumbers []string) (*appointment.DockAppointment, error) {
	d, err := appointment.Rehydrate(r.id, r.doorCode, r.carrier, r.windowStart, r.windowEnd, asnNumbers, appointment.State(r.state), r.version)
	if err != nil {
		return nil, fmt.Errorf("rehydrate appointment %s: %w", r.id, err)
	}
	return d, nil
}

// Get implements ports.AppointmentRepository.
func (r *AppointmentRepo) Get(ctx context.Context, id appointment.ID) (*appointment.DockAppointment, error) {
	q := queryFor(ctx, r.pool)
	var row appointmentRow
	err := q.QueryRow(ctx, `SELECT `+appointmentColumns+` FROM appointments WHERE id = $1`, string(id)).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrAppointmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get appointment %s: %w", id, err)
	}
	numbers, err := loadAppointmentAsns(ctx, q, []string{row.id})
	if err != nil {
		return nil, err
	}
	return row.toAppointment(numbers[row.id])
}

func loadAppointmentAsns(ctx context.Context, q querier, ids []string) (map[string][]string, error) {
	rows, err := q.Query(ctx, `SELECT appointment_id, asn_number FROM appointment_asns
		WHERE appointment_id = ANY($1) ORDER BY appointment_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("load appointment asns: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]string, len(ids))
	for rows.Next() {
		var id, number string
		if err := rows.Scan(&id, &number); err != nil {
			return nil, fmt.Errorf("scan appointment asn: %w", err)
		}
		out[id] = append(out[id], number)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load appointment asns: %w", err)
	}
	return out, nil
}

// ApplyBooked implements ports.AppointmentStore. A new id is inserted as
// Booked; a stored one gets its door, carrier, window, ASNs and version
// refreshed ONLY when the version is greater, and keeps its state, so a
// Booked event can never revive a Cancelled row or downgrade a legacy
// CheckedIn / Completed one.
func (r *AppointmentRepo) ApplyBooked(ctx context.Context, d *appointment.DockAppointment) (bool, error) {
	applied := false
	err := inTx(ctx, r.pool, func(q querier) error {
		w := d.Window()
		tag, err := q.Exec(ctx, `INSERT INTO appointments (`+appointmentColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET door_code = EXCLUDED.door_code, carrier = EXCLUDED.carrier,
				window_start = EXCLUDED.window_start, window_end = EXCLUDED.window_end,
				version = EXCLUDED.version, updated_at = now()
			WHERE appointments.version < EXCLUDED.version`,
			string(d.ID()), string(d.DoorCode()), d.Carrier(), w.Start(), w.End(), string(d.State()), d.Version())
		if err != nil {
			return fmt.Errorf("upsert appointment %s: %w", d.ID(), err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		applied = true
		if _, err := q.Exec(ctx, `DELETE FROM appointment_asns WHERE appointment_id = $1`, string(d.ID())); err != nil {
			return fmt.Errorf("replace appointment asns %s: %w", d.ID(), err)
		}
		for i, n := range d.AsnNumbers() {
			if _, err := q.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, $2, $3)`,
				string(d.ID()), i+1, string(n)); err != nil {
				return fmt.Errorf("insert appointment asn %s: %w", n, err)
			}
		}
		return nil
	})
	return applied, err
}

// ApplyCancelled implements ports.AppointmentStore.
func (r *AppointmentRepo) ApplyCancelled(ctx context.Context, id appointment.ID, version int64) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `UPDATE appointments SET state = 'Cancelled', version = $2, updated_at = now()
		WHERE id = $1 AND version < $2`, string(id), version)
	if err != nil {
		return false, fmt.Errorf("cancel appointment %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ApplyRescheduled implements ports.AppointmentStore (ADR 0020): door and
// window follow a newer version; state, carrier and ASNs are kept, and an
// unknown id matches no row.
func (r *AppointmentRepo) ApplyRescheduled(ctx context.Context, id appointment.ID, door appointment.DoorCode, window appointment.Window, version int64) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `UPDATE appointments
		SET door_code = $2, window_start = $3, window_end = $4, version = $5, updated_at = now()
		WHERE id = $1 AND version < $5`, string(id), string(door), window.Start(), window.End(), version)
	if err != nil {
		return false, fmt.Errorf("reschedule appointment %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ApplyDoorReassigned implements ports.AppointmentStore (ADR 0020): only the
// door follows a newer version.
func (r *AppointmentRepo) ApplyDoorReassigned(ctx context.Context, id appointment.ID, door appointment.DoorCode, version int64) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `UPDATE appointments SET door_code = $2, version = $3, updated_at = now()
		WHERE id = $1 AND version < $3`, string(id), string(door), version)
	if err != nil {
		return false, fmt.Errorf("reassign appointment door %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListRecords implements ports.AppointmentHistory. The check-in moment of a
// Completed row is the opening of its first receipt (a receipt could only be
// opened on a CheckedIn appointment); NULL when it has none.
func (r *AppointmentRepo) ListRecords(ctx context.Context, after appointment.ID, limit int) ([]repository.AppointmentRecord, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT a.id, a.door_code, a.carrier, a.window_start, a.window_end, a.state, a.version,
			a.created_at, a.updated_at,
			(SELECT min(rc.opened_at) FROM receipts rc WHERE rc.appointment_id = a.id)
		FROM appointments a WHERE a.id > $1::text ORDER BY a.id LIMIT $2`, string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("list appointment records: %w", err)
	}
	defer rows.Close()
	type record struct {
		row                  appointmentRow
		created, updated     time.Time
		firstReceiptOpenedAt *time.Time
	}
	var page []record
	ids := make([]string, 0)
	for rows.Next() {
		var rec record
		if err := rows.Scan(append(rec.row.targets(), &rec.created, &rec.updated, &rec.firstReceiptOpenedAt)...); err != nil {
			return nil, fmt.Errorf("scan appointment record: %w", err)
		}
		page = append(page, rec)
		ids = append(ids, rec.row.id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read appointment records: %w", err)
	}
	rows.Close()
	numbers, err := loadAppointmentAsns(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]repository.AppointmentRecord, 0, len(page))
	for _, rec := range page {
		d, err := rec.row.toAppointment(numbers[rec.row.id])
		if err != nil {
			return nil, err
		}
		tl := appointment.Timeline{BookedAt: rec.created, ChangedAt: rec.updated}
		if rec.firstReceiptOpenedAt != nil {
			tl.CheckedInAt = *rec.firstReceiptOpenedAt
		}
		out = append(out, repository.AppointmentRecord{Appointment: d, Timeline: tl})
	}
	return out, nil
}

// List implements ports.AppointmentRepository: ascending (window_start, id),
// strictly after the cursor; from/to keep appointments whose window overlaps
// [from, to).
func (r *AppointmentRepo) List(ctx context.Context, filter repository.AppointmentFilter, after repository.AppointmentCursor, limit int) ([]*appointment.DockAppointment, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+appointmentColumns+` FROM appointments
		WHERE ($1::timestamptz IS NULL OR (window_start, id) > ($1::timestamptz, $2::text))
		  AND ($3 = '' OR door_code = $3)
		  AND ($4 = '' OR state = $4)
		  AND ($5::timestamptz IS NULL OR window_end > $5)
		  AND ($6::timestamptz IS NULL OR window_start < $6)
		ORDER BY window_start, id LIMIT $7`,
		nullTime(after.WindowStart), string(after.ID), string(filter.Door), string(filter.State),
		nullTime(filter.From), nullTime(filter.To), limit)
	if err != nil {
		return nil, fmt.Errorf("list appointments: %w", err)
	}
	return r.collect(ctx, q, rows)
}

// collect drains rows and loads their ASN numbers in one more query.
func (r *AppointmentRepo) collect(ctx context.Context, q querier, rows pgx.Rows) ([]*appointment.DockAppointment, error) {
	defer rows.Close()
	var page []appointmentRow
	ids := make([]string, 0)
	for rows.Next() {
		var row appointmentRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan appointment: %w", err)
		}
		page = append(page, row)
		ids = append(ids, row.id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read appointments: %w", err)
	}
	rows.Close()
	numbers, err := loadAppointmentAsns(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*appointment.DockAppointment, 0, len(page))
	for _, row := range page {
		d, err := row.toAppointment(numbers[row.id])
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
