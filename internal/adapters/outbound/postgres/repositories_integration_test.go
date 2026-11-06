//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

var t0 = time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)

func newAsn(t *testing.T, number string, skus ...string) *asn.Asn {
	t.Helper()
	if len(skus) == 0 {
		skus = []string{"SKU-1", "SKU-2"}
	}
	lines := make([]asn.LineInput, 0, len(skus))
	for i, s := range skus {
		lines = append(lines, asn.LineInput{LineNo: i + 1, SKU: s, ExpectedQty: int64(10 * (i + 1))})
	}
	a, _, err := asn.Register(number, "ACME", t0.Add(24*time.Hour), lines, t0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func apptID(n int) string { return "appt-00000000-0000-4000-8000-" + pad12(n) }
func rcptID(n int) string { return "rcpt-00000000-0000-4000-8000-" + pad12(n) }

func pad12(n int) string {
	s := strings.Repeat("0", 11) + string(rune('0'+n))
	return s[len(s)-12:]
}

func newAppointment(t *testing.T, id, door string, start time.Time, asns ...string) *appointment.DockAppointment {
	t.Helper()
	if len(asns) == 0 {
		asns = []string{"ASN-1"}
	}
	d, err := appointment.Rehydrate(id, door, "ACME Freight", start, start.Add(time.Hour), asns, appointment.Booked, 1)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func openReceipt(t *testing.T, id string, a *asn.Asn, appt, door string) *receipt.Receipt {
	t.Helper()
	r, _, err := receipt.Open(id, a.Snapshot(), appt, door, t0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

// TestAsnRepoVersionGuardAndRoundTrip proves the two-branch Save against
// real Postgres: an insert never overwrites, an update applies only on the
// loaded version, the loser of a race gets ErrConcurrentModification, and
// every column maps back.
func TestAsnRepoVersionGuardAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewAsnRepo(startPostgresPool(t))
	a := newAsn(t, "ASN-1")
	if err := repo.Save(ctx, a, 0); err != nil {
		t.Fatalf("insert: %v", err)
	}
	wantErr(t, repo.Save(ctx, newAsn(t, "ASN-1"), 0), repository.ErrConcurrentModification)

	got, err := repo.Get(ctx, "ASN-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SupplierRef() != "ACME" || !got.ExpectedArrival().Equal(t0.Add(24*time.Hour)) || got.State() != asn.Registered ||
		got.Version() != 1 || len(got.Lines()) != 2 || got.Lines()[1].SKU() != "SKU-2" || got.Lines()[1].ExpectedQty() != 20 {
		t.Fatalf("round trip = %+v", got)
	}

	// Two writers load version 1; the first update wins, the second loses.
	first, _ := repo.Get(ctx, "ASN-1")
	second, _ := repo.Get(ctx, "ASN-1")
	if _, err := first.Cancel("a", t0); err != nil {
		t.Fatal(err)
	}
	if err := second.BeginReceiving(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, first, 1); err != nil {
		t.Fatalf("first update: %v", err)
	}
	wantErr(t, repo.Save(ctx, second, 1), repository.ErrConcurrentModification)
	wantErr(t, repo.Save(ctx, newAsn(t, "ASN-404"), 1), repository.ErrConcurrentModification)

	stored, _ := repo.Get(ctx, "ASN-1")
	if stored.State() != asn.Cancelled || stored.Version() != 2 {
		t.Fatalf("stored = %s v%d", stored.State(), stored.Version())
	}
	_, err = repo.Get(ctx, "ASN-404")
	wantErr(t, err, repository.ErrAsnNotFound)
}

func TestAsnRepoWithoutExpectedArrival(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewAsnRepo(startPostgresPool(t))
	a, _, err := asn.Register("ASN-1", "ACME", time.Time{}, []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, "ASN-1")
	if !got.ExpectedArrival().IsZero() {
		t.Fatalf("arrival = %v", got.ExpectedArrival())
	}
}

// TestAsnRepoListPagesInByteOrderWithFilters pages through real Postgres.
func TestAsnRepoListPagesInByteOrderWithFilters(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewAsnRepo(startPostgresPool(t))
	// "B" < "C" < "_x" < "a" in byte order (a linguistic collation would differ).
	for _, n := range []string{"a", "_x", "B", "C"} {
		if err := repo.Save(ctx, newAsn(t, n), 0); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := repo.Get(ctx, "C")
	if _, err := c.Cancel("", t0); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, c, 1); err != nil {
		t.Fatal(err)
	}

	numbers := func(items []*asn.Asn) string {
		var s []string
		for _, a := range items {
			s = append(s, string(a.Number()))
		}
		return strings.Join(s, ",")
	}
	all, err := repo.List(ctx, repository.AsnFilter{}, "", 10)
	if err != nil || numbers(all) != "B,C,_x,a" || len(all[0].Lines()) != 2 {
		t.Fatalf("all = %s %v", numbers(all), err)
	}
	page2, _ := repo.List(ctx, repository.AsnFilter{}, "C", 1)
	cancelled, _ := repo.List(ctx, repository.AsnFilter{State: asn.Cancelled}, "", 10)
	registeredAfter, _ := repo.List(ctx, repository.AsnFilter{State: asn.Registered}, "B", 10)
	if numbers(page2) != "_x" || numbers(cancelled) != "C" || numbers(registeredAfter) != "_x,a" {
		t.Fatalf("page2=%s cancelled=%s registeredAfter=%s", numbers(page2), numbers(cancelled), numbers(registeredAfter))
	}
}

func TestAppointmentRepoApplyRoundTripAndVersionGuard(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	repo := postgres.NewAppointmentRepo(pool)
	start := t0.Add(24 * time.Hour)
	d := newAppointment(t, apptID(1), "DOOR-1", start, "ASN-2", "ASN-1")

	if ok, err := repo.ApplyBooked(ctx, d); err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	if ok, err := repo.ApplyBooked(ctx, d); err != nil || ok {
		t.Fatalf("the same version must not apply again: %v %v", ok, err)
	}
	got, err := repo.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	numbers := got.AsnNumbers()
	if got.DoorCode() != "DOOR-1" || got.Carrier() != "ACME Freight" || !got.Window().Start().Equal(start) ||
		got.Window().End().Sub(start) != time.Hour || len(numbers) != 2 || numbers[0] != "ASN-2" || numbers[1] != "ASN-1" ||
		got.State() != appointment.Booked || got.Version() != 1 {
		t.Fatalf("round trip = %+v %v", got, numbers)
	}

	// A newer Booked refreshes door, carrier, window, ASNs and version.
	newer, err := appointment.Rehydrate(apptID(1), "DOOR-9", "Other Carrier", start.Add(time.Hour), start.Add(3*time.Hour), []string{"ASN-3"}, appointment.Booked, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ApplyBooked(ctx, newer); err != nil || !ok {
		t.Fatalf("newer: %v %v", ok, err)
	}
	older, _ := appointment.Rehydrate(apptID(1), "DOOR-OLD", "Old", start, start.Add(time.Hour), []string{"ASN-1"}, appointment.Booked, 2)
	if ok, err := repo.ApplyBooked(ctx, older); err != nil || ok {
		t.Fatalf("an older version must not apply: %v %v", ok, err)
	}
	got, _ = repo.Get(ctx, d.ID())
	numbers = got.AsnNumbers()
	if got.DoorCode() != "DOOR-9" || got.Carrier() != "Other Carrier" || !got.Window().Start().Equal(start.Add(time.Hour)) ||
		len(numbers) != 1 || numbers[0] != "ASN-3" || got.Version() != 4 {
		t.Fatalf("after the newer booking = %+v %v", got, numbers)
	}

	// A cancel needs a newer version and an existing appointment.
	if ok, err := repo.ApplyCancelled(ctx, d.ID(), 4); err != nil || ok {
		t.Fatalf("a cancel at the stored version must not apply: %v %v", ok, err)
	}
	if ok, err := repo.ApplyCancelled(ctx, appointment.ID(apptID(9)), 9); err != nil || ok {
		t.Fatalf("cancelling an unknown appointment must not apply: %v %v", ok, err)
	}
	if ok, err := repo.ApplyCancelled(ctx, d.ID(), 5); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	// A Booked event with a higher version refreshes the data but never revives it.
	revive, _ := appointment.Rehydrate(apptID(1), "DOOR-9", "Other Carrier", start, start.Add(time.Hour), []string{"ASN-3"}, appointment.Booked, 7)
	if ok, err := repo.ApplyBooked(ctx, revive); err != nil || !ok {
		t.Fatalf("later booked: %v %v", ok, err)
	}
	got, _ = repo.Get(ctx, d.ID())
	if got.State() != appointment.Cancelled || got.Version() != 7 {
		t.Fatalf("state = %s v%d, want Cancelled v7", got.State(), got.Version())
	}
	_, err = repo.Get(ctx, appointment.ID(apptID(9)))
	wantErr(t, err, repository.ErrAppointmentNotFound)

	// The copy keeps the ASN positions exactly 1..n.
	var positions []int
	rows, err := pool.Query(ctx, `SELECT position FROM appointment_asns WHERE appointment_id = $1 ORDER BY position`, apptID(1))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		positions = append(positions, p)
	}
	if len(positions) != 1 || positions[0] != 1 {
		t.Fatalf("asn positions = %v", positions)
	}
}

func TestAppointmentRepoListAndFilters(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewAppointmentRepo(startPostgresPool(t))
	h := time.Hour
	seed := []*appointment.DockAppointment{
		newAppointment(t, apptID(3), "DOOR-1", t0.Add(30*h)),
		newAppointment(t, apptID(2), "DOOR-2", t0.Add(24*h)),
		newAppointment(t, apptID(1), "DOOR-1", t0.Add(24*h)),
	}
	for _, d := range seed {
		if _, err := repo.ApplyBooked(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := repo.ApplyCancelled(ctx, appointment.ID(apptID(3)), 2); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}

	last := func(items []*appointment.DockAppointment) string {
		s := ""
		for _, d := range items {
			s += string(d.ID())[len(d.ID())-1:]
		}
		return s
	}
	none := repository.AppointmentCursor{}
	all, _ := repo.List(ctx, repository.AppointmentFilter{}, none, 10)
	after, _ := repo.List(ctx, repository.AppointmentFilter{}, repository.AppointmentCursor{WindowStart: t0.Add(24 * h), ID: all[0].ID()}, 10)
	door1, _ := repo.List(ctx, repository.AppointmentFilter{Door: "DOOR-1", State: appointment.Booked}, none, 10)
	cancelled, _ := repo.List(ctx, repository.AppointmentFilter{State: appointment.Cancelled}, none, 10)
	window, _ := repo.List(ctx, repository.AppointmentFilter{From: t0.Add(24*h + 30*time.Minute), To: t0.Add(25 * h)}, none, 10)
	limited, _ := repo.List(ctx, repository.AppointmentFilter{}, none, 2)
	if last(all) != "123" || last(after) != "23" || last(door1) != "1" || last(cancelled) != "3" || last(window) != "12" || last(limited) != "12" {
		t.Fatalf("all=%s after=%s door1=%s cancelled=%s window=%s limited=%s", last(all), last(after), last(door1), last(cancelled), last(window), last(limited))
	}
	if len(all[0].AsnNumbers()) != 1 {
		t.Fatal("list must load the covered ASN numbers")
	}
}

// Stage C migration (0005) on rows that exist today: the legacy aggregate
// version resets to 0, so ANY dock-scheduling version wins over a legacy row,
// and a legacy row keeps everything else.
func TestMigration0005ResetsLegacyVersionsSoAnyNewVersionWins(t *testing.T) {
	ctx := context.Background()
	pool, url := startUnmigratedPool(t)
	src, err := iofs.New(os.DirFS("."), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	if err := m.Migrate(4); err != nil {
		t.Fatalf("migrate to 4: %v", err)
	}
	// The legacy aggregate at its version 3 (booked, checked in, completed).
	if _, err := pool.Exec(ctx, `INSERT INTO appointments (id, door_code, carrier, window_start, window_end, state, version)
		VALUES ($1, 'DOOR-1', 'ACME', $2, $3, 'Completed', 3)`, apptID(1), t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, 1, 'ASN-1')`, apptID(1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	repo := postgres.NewAppointmentRepo(pool)
	legacy, err := repo.Get(ctx, appointment.ID(apptID(1)))
	if err != nil || legacy.Version() != 0 || legacy.State() != appointment.Completed || legacy.DoorCode() != "DOOR-1" {
		t.Fatalf("legacy row = %+v, %v; want version 0, state kept", legacy, err)
	}
	// The aggregate guard is gone: version 0 and a default are allowed, negatives are not.
	if _, err := pool.Exec(ctx, `INSERT INTO appointments (id, door_code, carrier, window_start, window_end, state)
		VALUES ($1, 'DOOR-2', 'ACME', $2, $3, 'Booked')`, apptID(2), t0, t0.Add(time.Hour)); err != nil {
		t.Fatalf("a row without a version must default to 0: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE appointments SET version = -1 WHERE id = $1`, apptID(2)); err == nil {
		t.Fatal("a negative version must be refused")
	}
	// The first dock-scheduling version wins over the legacy row.
	first, _ := appointment.Rehydrate(apptID(1), "DOOR-5", "ACME", t0, t0.Add(time.Hour), []string{"ASN-1"}, appointment.Booked, 1)
	if ok, err := repo.ApplyBooked(ctx, first); err != nil || !ok {
		t.Fatalf("version 1 over a legacy row: %v %v", ok, err)
	}
	if got, _ := repo.Get(ctx, appointment.ID(apptID(1))); got.DoorCode() != "DOOR-5" || got.Version() != 1 || got.State() != appointment.Completed {
		t.Fatalf("after = %s v%d %s; the legacy state must be kept", got.DoorCode(), got.Version(), got.State())
	}
	// The down migration restores the aggregate guard (every row restarts at 1).
	if err := m.Migrate(4); err != nil {
		t.Fatalf("down to 4: %v", err)
	}
	var v int64
	if err := pool.QueryRow(ctx, `SELECT min(version) FROM appointments`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("min version after the down migration = %d, %v", v, err)
	}
}

// ListRecords feeds the stage B backfill: ascending id, the row's own
// timestamps and, for a Completed row, the opening of its first receipt.
func TestAppointmentRepoListRecordsForTheBackfill(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	repo := postgres.NewAppointmentRepo(pool)
	asns, receipts := postgres.NewAsnRepo(pool), postgres.NewReceiptRepo(pool)
	for i, state := range []string{"Booked", "CheckedIn", "Completed", "Cancelled"} {
		if _, err := pool.Exec(ctx, `INSERT INTO appointments (id, door_code, carrier, window_start, window_end, state, version, created_at, updated_at)
			VALUES ($1, 'DOOR-1', 'ACME', $2, $3, $4, 0, $5, $6)`,
			apptID(i+1), t0, t0.Add(time.Hour), state, t0.Add(-48*time.Hour), t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, 1, $2)`,
			apptID(i+1), "ASN-"+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
	}
	// The Completed one (id 3) had a receipt opened at t0+20min.
	a := newAsn(t, "ASN-3")
	if err := asns.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	r, _, err := receipt.Open(rcptID(1), a.Snapshot(), apptID(3), "DOOR-1", t0.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := receipts.Save(ctx, r, 0); err != nil {
		t.Fatal(err)
	}

	all, err := repo.ListRecords(ctx, "", 10)
	if err != nil || len(all) != 4 {
		t.Fatalf("records = %d, %v", len(all), err)
	}
	for i, rec := range all {
		if string(rec.Appointment.ID()) != apptID(i+1) {
			t.Fatalf("record %d = %s, want ascending id", i, rec.Appointment.ID())
		}
		if !rec.Timeline.BookedAt.Equal(t0.Add(-48*time.Hour)) || !rec.Timeline.ChangedAt.Equal(t0.Add(time.Duration(i)*time.Hour)) {
			t.Fatalf("record %d timeline = %+v", i, rec.Timeline)
		}
		wantIn := time.Time{}
		if i == 2 {
			wantIn = t0.Add(20 * time.Minute)
		}
		if !rec.Timeline.CheckedInAt.Equal(wantIn) {
			t.Fatalf("record %d checked-in moment = %v, want %v", i, rec.Timeline.CheckedInAt, wantIn)
		}
	}
	page, err := repo.ListRecords(ctx, appointment.ID(apptID(2)), 1)
	if err != nil || len(page) != 1 || string(page[0].Appointment.ID()) != apptID(3) {
		t.Fatalf("page after #2 limit 1 = %v, %v", page, err)
	}
	if rest, err := repo.ListRecords(ctx, appointment.ID(apptID(4)), 10); err != nil || len(rest) != 0 {
		t.Fatalf("after the last = %v, %v", rest, err)
	}
}

func TestReceiptRepoVersionGuardRoundTripAndLines(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	asns, repo := postgres.NewAsnRepo(pool), postgres.NewReceiptRepo(pool)
	a := newAsn(t, "ASN-1")
	if err := asns.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	r := openReceipt(t, rcptID(1), a, "", "")
	if err := repo.Save(ctx, r, 0); err != nil {
		t.Fatalf("insert: %v", err)
	}
	wantErr(t, repo.Save(ctx, r, 0), repository.ErrConcurrentModification)

	loaded, err := repo.Get(ctx, r.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ReceiveLine(1, 6, receipt.ConditionGood, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ReceiveLine(1, 2, receipt.ConditionDamaged, t0); err != nil {
		t.Fatal(err)
	}
	stale, _ := repo.Get(ctx, r.ID())
	if _, err := stale.ReceiveLine(2, 1, receipt.ConditionGood, t0); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, loaded, 1); err != nil {
		t.Fatalf("update: %v", err)
	}
	wantErr(t, repo.Save(ctx, stale, 1), repository.ErrConcurrentModification)

	got, _ := repo.Get(ctx, r.ID())
	l1, l2 := got.Lines()[0], got.Lines()[1]
	if got.Version() != 3 || l1.ReceivedGood() != 6 || l1.ReceivedDamaged() != 2 || l2.ReceivedGood() != 0 || l1.ExpectedQty() != 10 || l2.SKU() != "SKU-2" {
		t.Fatalf("stored = v%d %+v %+v", got.Version(), l1, l2)
	}

}

func TestReceiptRepoCloseRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	asns, repo := postgres.NewAsnRepo(pool), postgres.NewReceiptRepo(pool)
	a := newAsn(t, "ASN-1")
	if err := asns.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	r := openReceipt(t, rcptID(1), a, "", "")
	if err := repo.Save(ctx, r, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, r.ID())
	if _, err := got.Close(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	closed, _ := repo.Get(ctx, r.ID())
	if closed.State() != receipt.StateClosed || !closed.ClosedAt().Equal(t0.Add(time.Hour)) {
		t.Fatalf("closed = %s %v", closed.State(), closed.ClosedAt())
	}
	_, err := repo.Get(ctx, receipt.ID(rcptID(9)))
	wantErr(t, err, repository.ErrReceiptNotFound)
}

// TestReceiptRepoAtMostOneOpenReceiptPerAsn proves the partial unique index
// and that a violation inside a unit of work leaves the transaction usable.
func TestReceiptRepoAtMostOneOpenReceiptPerAsn(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	asns, repo, uow := postgres.NewAsnRepo(pool), postgres.NewReceiptRepo(pool), postgres.NewUnitOfWork(pool)
	a := newAsn(t, "ASN-1")
	if err := asns.Save(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	first := openReceipt(t, rcptID(1), a, "", "")
	if err := repo.Save(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	wantErr(t, repo.Save(ctx, openReceipt(t, rcptID(2), a, "", ""), 0), repository.ErrReceiptAlreadyOpen)

	open, err := repo.OpenByAsn(ctx, "ASN-1")
	if err != nil || open.ID() != first.ID() {
		t.Fatalf("OpenByAsn = %v %v", open, err)
	}

	// Inside a unit of work the violation must not poison the transaction.
	err = uow.Do(ctx, func(ctx context.Context) error {
		if err := repo.Save(ctx, openReceipt(t, rcptID(3), a, "", ""), 0); !errors.Is(err, repository.ErrReceiptAlreadyOpen) {
			t.Errorf("in unit of work: %v", err)
		}
		_, err := asns.Get(ctx, "ASN-1")
		return err
	})
	if err != nil {
		t.Fatalf("the unit of work must stay usable after the violation: %v", err)
	}

	if _, err := open.Close(t0); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, open, 1); err != nil {
		t.Fatal(err)
	}
	_, err = repo.OpenByAsn(ctx, "ASN-1")
	wantErr(t, err, repository.ErrReceiptNotFound)
	if err := repo.Save(ctx, openReceipt(t, rcptID(4), a, "", ""), 0); err != nil {
		t.Fatalf("a new open receipt after the close: %v", err)
	}
}

func TestReceiptRepoListPagesAndFilters(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	asns, repo := postgres.NewAsnRepo(pool), postgres.NewReceiptRepo(pool)
	for i, n := range []string{"ASN-1", "ASN-2", "ASN-3"} {
		a := newAsn(t, n)
		if err := asns.Save(ctx, a, 0); err != nil {
			t.Fatal(err)
		}
		if err := repo.Save(ctx, openReceipt(t, rcptID(3-i), a, "", "DOOR-1"), 0); err != nil {
			t.Fatal(err)
		}
	}
	closed, _ := repo.Get(ctx, receipt.ID(rcptID(2)))
	if _, err := closed.Close(t0); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, closed, 1); err != nil {
		t.Fatal(err)
	}
	last := func(items []*receipt.Receipt) string {
		s := ""
		for _, x := range items {
			s += string(x.ID())[len(x.ID())-1:]
		}
		return s
	}
	all, _ := repo.List(ctx, repository.ReceiptFilter{}, "", 10)
	paged, _ := repo.List(ctx, repository.ReceiptFilter{}, receipt.ID(rcptID(1)), 1)
	byAsn, _ := repo.List(ctx, repository.ReceiptFilter{AsnNumber: "ASN-3"}, "", 10)
	byState, _ := repo.List(ctx, repository.ReceiptFilter{State: receipt.StateClosed}, "", 10)
	if last(all) != "123" || last(paged) != "2" || last(byAsn) != "1" || last(byState) != "2" {
		t.Fatalf("all=%s paged=%s byAsn=%s byState=%s", last(all), last(paged), last(byAsn), last(byState))
	}
	if all[0].DoorCode() != "DOOR-1" || len(all[0].Lines()) != 2 {
		t.Fatalf("door/lines not loaded: %+v", all[0])
	}
}

func TestKnownSkusRoundTrip(t *testing.T) {
	ctx := context.Background()
	skus := postgres.NewKnownSkus(startPostgresPool(t))
	if ok, err := skus.Exists(ctx, "SKU-1"); err != nil || ok {
		t.Fatalf("empty: %v %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if err := skus.Upsert(ctx, "SKU-1"); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := skus.Exists(ctx, "SKU-1"); !ok {
		t.Fatal("upserted sku is unknown")
	}
}

func TestDockDoorsRoundTrip(t *testing.T) {
	ctx := context.Background()
	doors := postgres.NewDockDoors(startPostgresPool(t))
	for _, d := range []repository.DockDoor{
		{Code: "b", Flow: repository.DockFlowBoth}, {Code: "A", Flow: repository.DockFlowInbound}, {Code: "A", Flow: repository.DockFlowBoth},
	} {
		if err := doors.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	list, err := doors.List(ctx)
	if err != nil || len(list) != 2 || list[0].Code != "A" || list[0].Flow != repository.DockFlowBoth || list[1].Code != "b" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := doors.Remove(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	if err := doors.Remove(ctx, "NEVER"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := doors.Exists(ctx, "A"); ok {
		t.Fatal("removed door still exists")
	}
	if ok, _ := doors.Exists(ctx, "b"); !ok {
		t.Fatal("kept door disappeared")
	}
}

func TestProcessedEventRepoClaimsOnceAndRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	uow, processed := postgres.NewUnitOfWork(pool), postgres.NewProcessedEventRepo(pool)
	if ok, err := processed.Claim(ctx, "c", "id-1"); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := processed.Claim(ctx, "c", "id-1"); err != nil || ok {
		t.Fatalf("second claim: %v %v", ok, err)
	}
	_ = uow.Do(ctx, func(ctx context.Context) error {
		_, _ = processed.Claim(ctx, "c", "id-2")
		return errors.New("boom")
	})
	if ok, _ := processed.Claim(ctx, "c", "id-2"); !ok {
		t.Fatal("a rolled-back claim must not count")
	}
}
