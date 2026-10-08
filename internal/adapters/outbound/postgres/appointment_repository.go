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

// AppointmentRepo is the pgx-backed ports.AppointmentRepository over
// appointments and appointment_asns. Save is version-guarded like the other
// repositories. Everything but the state is immutable after booking, so an
// update touches the header row only.
type AppointmentRepo struct {
	pool *pgxpool.Pool
}

// NewAppointmentRepo constructs an AppointmentRepo over pool.
func NewAppointmentRepo(pool *pgxpool.Pool) *AppointmentRepo { return &AppointmentRepo{pool: pool} }

var _ ports.AppointmentRepository = (*AppointmentRepo)(nil)

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

// Save implements ports.AppointmentRepository (see AsnRepo.Save for the
// version-guard contract).
func (r *AppointmentRepo) Save(ctx context.Context, d *appointment.DockAppointment, loadedVersion int64) error {
	return inTx(ctx, r.pool, func(q querier) error {
		if loadedVersion == 0 {
			return insertAppointment(ctx, q, d)
		}
		tag, err := q.Exec(ctx, `UPDATE appointments SET state = $2, version = $3, updated_at = now()
			WHERE id = $1 AND version = $4`, string(d.ID()), string(d.State()), d.Version(), loadedVersion)
		if err != nil {
			return fmt.Errorf("update appointment %s: %w", d.ID(), err)
		}
		if tag.RowsAffected() != 1 {
			return repository.ErrConcurrentModification
		}
		return nil
	})
}

func insertAppointment(ctx context.Context, q querier, d *appointment.DockAppointment) error {
	w := d.Window()
	tag, err := q.Exec(ctx, `INSERT INTO appointments (`+appointmentColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO NOTHING`,
		string(d.ID()), string(d.DoorCode()), d.Carrier(), w.Start(), w.End(), string(d.State()), d.Version())
	if err != nil {
		return fmt.Errorf("insert appointment %s: %w", d.ID(), err)
	}
	if tag.RowsAffected() != 1 {
		return repository.ErrConcurrentModification
	}
	for i, n := range d.AsnNumbers() {
		if _, err := q.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, $2, $3)`,
			string(d.ID()), i+1, string(n)); err != nil {
			return fmt.Errorf("insert appointment asn %s: %w", n, err)
		}
	}
	return nil
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

// ActiveOnDoor implements ports.AppointmentRepository.
func (r *AppointmentRepo) ActiveOnDoor(ctx context.Context, door appointment.DoorCode) ([]*appointment.DockAppointment, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+appointmentColumns+` FROM appointments
		WHERE door_code = $1 AND state IN ('Booked', 'CheckedIn') ORDER BY window_start, id`, string(door))
	if err != nil {
		return nil, fmt.Errorf("active appointments on door %s: %w", door, err)
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

// LockDoor implements ports.AppointmentRepository with a transaction-scoped
// advisory lock keyed by the door: concurrent bookings of one door queue up
// until the surrounding transaction ends, so the overlap check and the
// insert are atomic per door. It is a no-op outside a transaction, where the
// lock would release at once; use cases always call it inside a UnitOfWork.
func (r *AppointmentRepo) LockDoor(ctx context.Context, door appointment.DoorCode) error {
	if _, err := queryFor(ctx, r.pool).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('dock-door:' || $1::text, 0))`, string(door)); err != nil {
		return fmt.Errorf("lock door %s: %w", door, err)
	}
	return nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
