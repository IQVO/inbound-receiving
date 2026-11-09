//go:build integration

// Package usecases_test proves this context's main write use cases against a
// REAL Postgres (testcontainers): the real Postgres repositories, the real
// UnitOfWork (pgx transactions carried by pgtx), the real Kafka CloudEvents
// Encoder writing the transactional outbox, wired exactly like cmd/api's
// composition root. These are integration tests in the fleet's sense: they
// execute the real cross-component contracts — aggregate lifecycle (register
// -> state change -> read model), optimistic versioning, the door-overlap
// policy, the atomic save+publish bracket — against real infrastructure,
// with no in-memory repo fakes anywhere in the path.
//
// The package boots its own throwaway Postgres via testcontainers in
// TestMain: one container for the whole package, migrated once into a
// template database, one private database per test (CREATE DATABASE ...
// TEMPLATE, a file-level copy that costs milliseconds). Never an external
// DATABASE_URL, never t.Skip.
package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// One Postgres container serves the whole package (containers are slow to
// boot). TestMain starts it, applies the embedded migrations ONCE into a
// template database, and each test then gets its own database cloned from
// that template. Isolation is therefore total — no TRUNCATE bookkeeping, no
// dependence on test order — and tests that assert on whole-table contents
// (the outbox) still start pristine.
const templateDB = "usecases_migrated_template"

var (
	sharedBaseURL string // connection URL of the container's default database
	dbSeq         atomic.Uint64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_usecases"),
		tcpostgres.WithUsername("inbound"),
		tcpostgres.WithPassword("inbound"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}()

	sharedBaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		return 1
	}

	// Migrate a template database once; every test clones it.
	if err := createDatabase(ctx, templateDB); err != nil {
		fmt.Fprintf(os.Stderr, "create template database: %v\n", err)
		return 1
	}
	tmplDB := withDB(sharedBaseURL, templateDB)
	if err := postgres.RunMigrations(tmplDB); err != nil {
		fmt.Fprintf(os.Stderr, "migrate template: %v\n", err)
		return 1
	}
	// Boot runs the migrations on every start; prove they are idempotent.
	if err := postgres.RunMigrations(tmplDB); err != nil {
		fmt.Fprintf(os.Stderr, "second migration run: %v\n", err)
		return 1
	}

	return m.Run()
}

// withDB rewrites the path of a connection URL to the named database.
func withDB(baseURL, name string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// createDatabase creates an empty database inside the shared container.
func createDatabase(ctx context.Context, name string) error {
	conn, err := pgx.Connect(ctx, sharedBaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// migratedDB hands the test a connection URL to its own private database,
// cloned from the migrated template (a file-level copy: milliseconds).
func migratedDB(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("usecases_%d", dbSeq.Add(1))
	conn, err := pgx.Connect(context.Background(), sharedBaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		"CREATE DATABASE %q WITH TEMPLATE %q", name, templateDB)); err != nil {
		t.Fatalf("clone database: %v", err)
	}
	return withDB(sharedBaseURL, name)
}

// wiredClock is the fixed instant every wired use case runs at, so the
// appointment windows below are deterministic (no flaky near-boundary maths).
var wiredClock = fixedClock{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}

// fixedClock is the ports.Clock the use cases already accept.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// wired is the real adapter stack a test drives: every use case over one
// private migrated database, with the real Postgres repos, the real
// UnitOfWork and the real CloudEvents encoder feeding the transactional
// outbox — exactly cmd/api's composition root minus the network.
type wired struct {
	register     *usecases.RegisterAsn
	cancelAsn    *usecases.CancelAsn
	apptBooked   *usecases.ApplyDockAppointmentBooked
	apptCancel   *usecases.ApplyDockAppointmentCancelled
	apptResched  *usecases.ApplyDockAppointmentRescheduled
	apptDoor     *usecases.ApplyDockAppointmentDoorReassigned
	getAppt      *usecases.GetAppointment
	openReceipt  *usecases.OpenReceipt
	receiveLine  *usecases.ReceiveLine
	closeReceipt *usecases.CloseReceipt
	getAsn       *usecases.GetAsn
	pool         *pgxpool.Pool
}

// newWiredUsecases wires the real stack over a private migrated database.
func newWiredUsecases(t *testing.T) *wired {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, migratedDB(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	w := usecases.Writer{
		Asns:         postgres.NewAsnRepo(pool),
		Appointments: postgres.NewAppointmentRepo(pool),
		Receipts:     postgres.NewReceiptRepo(pool),
		Outbox:       postgres.NewOutboxRepo(pool),
		Encoder:      outboundkafka.NewEncoder(),
		UoW:          postgres.NewUnitOfWork(pool),
		Clock:        wiredClock,
		IDs:          idgen.UUID{},
	}
	return &wired{
		register:  &usecases.RegisterAsn{Writer: w},
		cancelAsn: &usecases.CancelAsn{Writer: w},
		apptBooked: &usecases.ApplyDockAppointmentBooked{
			UoW: w.UoW, ProcessedEvents: postgres.NewProcessedEventRepo(pool), Appointments: postgres.NewAppointmentRepo(pool)},
		apptCancel: &usecases.ApplyDockAppointmentCancelled{
			UoW: w.UoW, ProcessedEvents: postgres.NewProcessedEventRepo(pool), Appointments: postgres.NewAppointmentRepo(pool)},
		apptResched: &usecases.ApplyDockAppointmentRescheduled{
			UoW: w.UoW, ProcessedEvents: postgres.NewProcessedEventRepo(pool), Appointments: postgres.NewAppointmentRepo(pool)},
		apptDoor: &usecases.ApplyDockAppointmentDoorReassigned{
			UoW: w.UoW, ProcessedEvents: postgres.NewProcessedEventRepo(pool), Appointments: postgres.NewAppointmentRepo(pool)},
		getAppt:      &usecases.GetAppointment{Appointments: postgres.NewAppointmentRepo(pool)},
		openReceipt:  &usecases.OpenReceipt{Writer: w},
		receiveLine:  &usecases.ReceiveLine{Writer: w},
		closeReceipt: &usecases.CloseReceipt{Writer: w},
		getAsn:       &usecases.GetAsn{Asns: postgres.NewAsnRepo(pool)},
		pool:         pool,
	}
}

// outboxEventTypes returns the event_type column of every outbox row, in
// insertion order — exactly what the relay would publish, in that order.
func (wd *wired) outboxEventTypes(t *testing.T) []string {
	t.Helper()
	var all string
	if err := wd.pool.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(event_type, ',' ORDER BY id), '') FROM outbox_events`).Scan(&all); err != nil {
		t.Fatalf("read outbox event types: %v", err)
	}
	if all == "" {
		return nil
	}
	return strings.Split(all, ",")
}

// mustRegisterAsn registers one ASN or fails the test.
func mustRegisterAsn(t *testing.T, wd *wired, number string, lines ...usecases.AsnLineInput) {
	t.Helper()
	if _, err := wd.register.Handle(context.Background(), usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", Lines: lines,
	}); err != nil {
		t.Fatalf("register %s: %v", number, err)
	}
}

// TestUsecasesAsnLifecycle drives the ASN aggregate end to end: register
// (Registered, v1), read it back through the read model, cancel (Cancelled,
// v2), and proves both transitions reached the transactional outbox as
// CloudEvents with this context's type namespace.
func TestUsecasesAsnLifecycle(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-1",
		usecases.AsnLineInput{LineNo: 1, SKU: "SKU-A", ExpectedQty: 10},
		usecases.AsnLineInput{LineNo: 2, SKU: "SKU-B", ExpectedQty: 5})

	a, err := wd.getAsn.Handle(ctx, "ASN-ITCOV-1")
	if err != nil {
		t.Fatalf("get asn: %v", err)
	}
	if a.State() != "Registered" || a.Version() != 1 || len(a.Lines()) != 2 {
		t.Fatalf("after register: state %s v%d lines %d", a.State(), a.Version(), len(a.Lines()))
	}

	if _, err := wd.cancelAsn.Handle(ctx, usecases.CancelAsnCommand{
		AsnNumber: "ASN-ITCOV-1", Reason: "supplier mix-up", ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("cancel asn: %v", err)
	}
	a, err = wd.getAsn.Handle(ctx, "ASN-ITCOV-1")
	if err != nil {
		t.Fatalf("get asn after cancel: %v", err)
	}
	if a.State() != "Cancelled" || a.Version() != 2 {
		t.Fatalf("after cancel: state %s v%d, want Cancelled v2", a.State(), a.Version())
	}

	// Both transitions were enqueued, in order, with the CloudEvents type
	// the fleet contract pins (com.warehouse.wms.inbound-receiving.<entity>.<Event>).
	got := wd.outboxEventTypes(t)
	want := []string{
		cloudevents.Type(outboundkafka.EntityAsn, "ASNRegistered"),
		cloudevents.Type(outboundkafka.EntityAsn, "ASNCancelled"),
	}
	if len(got) != len(want) {
		t.Fatalf("outbox events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outbox event %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}

	// A stale If-Match is rejected and persists nothing new.
	before := len(wd.outboxEventTypes(t))
	if _, err := wd.cancelAsn.Handle(ctx, usecases.CancelAsnCommand{
		AsnNumber: "ASN-ITCOV-1", ExpectedVersion: 1,
	}); !errors.Is(err, usecases.ErrVersionMismatch) {
		t.Fatalf("stale cancel = %v, want ErrVersionMismatch", err)
	}
	if after := len(wd.outboxEventTypes(t)); after != before {
		t.Fatalf("rejected cancel enqueued %d event(s)", after-before)
	}
}

const itApptID = "appt-00000000-0000-4000-8000-0000000000a1"

// dockSchedulingBooked is the DockAppointmentBooked dock-scheduling publishes,
// as the consumer hands it to the use case.
func dockSchedulingBooked(door string, version int64, asns ...string) usecases.DockAppointmentBooked {
	start := wiredClock.now.Add(15 * time.Minute)
	return usecases.DockAppointmentBooked{
		AppointmentID: itApptID, Direction: "INBOUND", DoorCode: door, Carrier: "ACME Freight",
		WindowStart: start, WindowEnd: start.Add(45 * time.Minute), AsnNumbers: asns, Version: version,
	}
}

// TestUsecasesAppointmentCopyToClosedReceipt walks the lifecycle the service
// exists for since the DockAppointment moved to dock-scheduling (ADR 0019):
// register -> dock-scheduling's booking reaches the local copy -> open the
// receipt against it (no check-in is needed or seen) -> receive a short line
// -> close. Every state change is persisted and published, the close completes
// the ASN only, and neither applying the booking nor closing the receipt
// raises an appointment event.
func TestUsecasesAppointmentCopyToClosedReceipt(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-2",
		usecases.AsnLineInput{LineNo: 1, SKU: "SKU-C", ExpectedQty: 10})

	if out, err := wd.apptBooked.Handle(ctx, "ev-book-1", dockSchedulingBooked("DOOR-1", 1, "ASN-ITCOV-2")); err != nil || out != usecases.Applied {
		t.Fatalf("apply booked = %s, %v", out, err)
	}
	appt, err := wd.getAppt.Handle(ctx, itApptID)
	if err != nil || appt.State() != "Booked" || appt.Version() != 1 || appt.DoorCode() != "DOOR-1" {
		t.Fatalf("after the booking: %+v, %v", appt, err)
	}

	rcpt, err := wd.openReceipt.Handle(ctx, usecases.OpenReceiptCommand{
		AsnNumber: "ASN-ITCOV-2", AppointmentID: itApptID})
	if err != nil {
		t.Fatalf("open receipt: %v", err)
	}
	if rcpt.DoorCode() != "DOOR-1" || rcpt.State() != "Open" {
		t.Fatalf("receipt opened without the appointment's door: %+v", rcpt)
	}

	// Opening the receipt moved the ASN to Receiving.
	a, err := wd.getAsn.Handle(ctx, "ASN-ITCOV-2")
	if err != nil {
		t.Fatalf("get asn: %v", err)
	}
	if a.State() != "Receiving" {
		t.Fatalf("asn state after open receipt = %s, want Receiving", a.State())
	}

	if _, err := wd.receiveLine.Handle(ctx, usecases.ReceiveLineCommand{
		ReceiptID: string(rcpt.ID()), LineNo: 1, Quantity: 8, Condition: "Good",
	}); err != nil {
		t.Fatalf("receive line: %v", err)
	}

	closed, err := wd.closeReceipt.Handle(ctx, usecases.CloseReceiptCommand{
		ReceiptID: string(rcpt.ID())})
	if err != nil {
		t.Fatalf("close receipt: %v", err)
	}
	if closed.State() != "Closed" {
		t.Fatalf("receipt state = %s, want Closed", closed.State())
	}
	if len(closed.Discrepancies()) != 1 || closed.Discrepancies()[0].Kind != "Short" {
		t.Fatalf("discrepancies = %+v, want one Short (received 8 of 10)", closed.Discrepancies())
	}

	// The close completed the ASN; the appointment is dock-scheduling's.
	a, err = wd.getAsn.Handle(ctx, "ASN-ITCOV-2")
	if err != nil {
		t.Fatalf("get asn after close: %v", err)
	}
	if a.State() != "Closed" {
		t.Fatalf("asn state after close = %s, want Closed", a.State())
	}
	appt, _ = wd.getAppt.Handle(ctx, itApptID)
	if appt.State() != "Booked" || appt.Version() != 1 {
		t.Fatalf("appointment after the close = %s v%d, want untouched (Booked v1)", appt.State(), appt.Version())
	}

	got := wd.outboxEventTypes(t)
	want := []string{
		cloudevents.Type(outboundkafka.EntityAsn, "ASNRegistered"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptOpened"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptLineReceived"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptClosed"),
	}
	if len(got) != len(want) {
		t.Fatalf("outbox events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outbox event %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestUsecasesAppointmentConsumersAgainstRealPostgres pins the consumer rules
// on the real stack: id claim and effect in one transaction, version guard
// (legacy rows are version 0), a cancelled appointment refuses a receipt, an
// outbound appointment is not copied, and NOTHING reaches the outbox (no loop).
func TestUsecasesAppointmentConsumersAgainstRealPostgres(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-3",
		usecases.AsnLineInput{LineNo: 1, SKU: "SKU-D", ExpectedQty: 1})
	outboxBefore := len(wd.outboxEventTypes(t))

	// A legacy row (version 0) is overwritten by dock-scheduling's version 1.
	if _, err := wd.pool.Exec(ctx, `INSERT INTO appointments (id, door_code, carrier, window_start, window_end, state, version)
		VALUES ($1, 'DOOR-OLD', 'Old Carrier', $2, $3, 'Booked', 0)`, itApptID, wiredClock.now, wiredClock.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := wd.pool.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, 1, 'ASN-OLD')`, itApptID); err != nil {
		t.Fatal(err)
	}
	if out, err := wd.apptBooked.Handle(ctx, "ev-1", dockSchedulingBooked("DOOR-2", 1, "ASN-ITCOV-3")); err != nil || out != usecases.Applied {
		t.Fatalf("version 1 over a legacy row = %s, %v", out, err)
	}
	appt, _ := wd.getAppt.Handle(ctx, itApptID)
	if appt.DoorCode() != "DOOR-2" || appt.Carrier() != "ACME Freight" || !appt.Covers("ASN-ITCOV-3") || appt.Covers("ASN-OLD") || appt.Version() != 1 {
		t.Fatalf("copy = %s %s v%d %v", appt.DoorCode(), appt.Carrier(), appt.Version(), appt.AsnNumbers())
	}

	// Redelivery of the same CloudEvents id and an older version change nothing.
	if out, _ := wd.apptBooked.Handle(ctx, "ev-1", dockSchedulingBooked("DOOR-X", 9, "ASN-ITCOV-3")); out != usecases.Duplicate {
		t.Fatalf("redelivery = %s, want duplicate", out)
	}
	stale := dockSchedulingBooked("DOOR-STALE", 1, "ASN-ITCOV-3")
	if out, _ := wd.apptBooked.Handle(ctx, "ev-2", stale); out != usecases.Ignored {
		t.Fatalf("same version = %s, want ignored", out)
	}

	// Outbound appointments are not copied.
	outbound := dockSchedulingBooked("DOOR-9", 1)
	outbound.AppointmentID, outbound.Direction = "appt-00000000-0000-4000-8000-0000000000b2", "OUTBOUND"
	if out, _ := wd.apptBooked.Handle(ctx, "ev-3", outbound); out != usecases.Ignored {
		t.Fatalf("outbound = %s, want ignored", out)
	}
	if _, err := wd.getAppt.Handle(ctx, outbound.AppointmentID); !errors.Is(err, repository.ErrAppointmentNotFound) {
		t.Fatalf("an outbound appointment must not be stored: %v", err)
	}

	// Cancelled by dock-scheduling: the copy is cancelled and refuses a receipt.
	if out, err := wd.apptCancel.Handle(ctx, "ev-4", itApptID, 2); err != nil || out != usecases.Applied {
		t.Fatalf("cancel = %s, %v", out, err)
	}
	if _, err := wd.openReceipt.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-ITCOV-3", AppointmentID: itApptID}); !errors.Is(err, usecases.ErrAppointmentCancelled) {
		t.Fatalf("open receipt on a cancelled appointment = %v, want ErrAppointmentCancelled", err)
	}

	// NO-LOOP: none of this raised an event.
	if after := len(wd.outboxEventTypes(t)); after != outboxBefore {
		t.Fatalf("applying dock-scheduling's events enqueued %d message(s) in the outbox", after-outboxBefore)
	}
}

// TestUsecasesAppointmentCopyFollowsReschedulesAndDoorReassignments walks ADR
// 0020 on the real stack: a receipt opened before a door reassignment keeps
// its door (a physical fact), a receipt opened after a reschedule takes the
// new door, the claim and the update share one transaction, an unknown
// appointment is ignored but claimed, and nothing reaches the outbox.
func TestUsecasesAppointmentCopyFollowsReschedulesAndDoorReassignments(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-4", usecases.AsnLineInput{LineNo: 1, SKU: "SKU-E", ExpectedQty: 1})
	mustRegisterAsn(t, wd, "ASN-ITCOV-5", usecases.AsnLineInput{LineNo: 1, SKU: "SKU-E", ExpectedQty: 1})
	if out, err := wd.apptBooked.Handle(ctx, "ev-f1", dockSchedulingBooked("DOOR-1", 1, "ASN-ITCOV-4", "ASN-ITCOV-5")); err != nil || out != usecases.Applied {
		t.Fatalf("apply booked = %s, %v", out, err)
	}
	outboxBefore := wd.outboxEventTypes(t)
	first, err := wd.openReceipt.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-ITCOV-4", AppointmentID: itApptID})
	if err != nil || first.DoorCode() != "DOOR-1" {
		t.Fatalf("first receipt = %v, %v", first, err)
	}

	// The yard reassigns the door: the open receipt keeps DOOR-1.
	if out, err := wd.apptDoor.Handle(ctx, "ev-f2", usecases.DockAppointmentDoorReassigned{
		AppointmentID: itApptID, FromDoorCode: "DOOR-1", ToDoorCode: "DOOR-7", Version: 2}); err != nil || out != usecases.Applied {
		t.Fatalf("door reassigned = %s, %v", out, err)
	}
	var openDoor string
	if err := wd.pool.QueryRow(ctx, `SELECT door_code FROM receipts WHERE id = $1`, string(first.ID())).Scan(&openDoor); err != nil || openDoor != "DOOR-1" {
		t.Fatalf("open receipt door = %q, %v; want DOOR-1", openDoor, err)
	}

	// dock-scheduling reschedules: new window and door; a stale one is ignored.
	newStart := wiredClock.now.Add(3 * time.Hour)
	if out, err := wd.apptResched.Handle(ctx, "ev-f3", usecases.DockAppointmentRescheduled{
		AppointmentID: itApptID, DoorCode: "DOOR-4", WindowStart: newStart, WindowEnd: newStart.Add(time.Hour), Version: 3}); err != nil || out != usecases.Applied {
		t.Fatalf("rescheduled = %s, %v", out, err)
	}
	if out, _ := wd.apptResched.Handle(ctx, "ev-f4", usecases.DockAppointmentRescheduled{
		AppointmentID: itApptID, DoorCode: "DOOR-STALE", WindowStart: newStart, WindowEnd: newStart.Add(time.Hour), Version: 3}); out != usecases.Ignored {
		t.Fatalf("stale reschedule = %s, want ignored", out)
	}
	if out, _ := wd.apptResched.Handle(ctx, "ev-f3", usecases.DockAppointmentRescheduled{
		AppointmentID: itApptID, DoorCode: "DOOR-X", WindowStart: newStart, WindowEnd: newStart.Add(time.Hour), Version: 9}); out != usecases.Duplicate {
		t.Fatalf("redelivery = %s, want duplicate", out)
	}
	appt, err := wd.getAppt.Handle(ctx, itApptID)
	if err != nil || appt.DoorCode() != "DOOR-4" || appt.Version() != 3 || !appt.Window().Start().Equal(newStart) || appt.State() != "Booked" {
		t.Fatalf("copy after the reschedule = %+v, %v", appt, err)
	}
	second, err := wd.openReceipt.Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-ITCOV-5", AppointmentID: itApptID})
	if err != nil || second.DoorCode() != "DOOR-4" {
		t.Fatalf("second receipt = %v, %v; want DOOR-4", second, err)
	}

	// An appointment this context never copied: ignored, claimed, not created.
	ghost := "appt-00000000-0000-4000-8000-0000000000c3"
	if out, err := wd.apptDoor.Handle(ctx, "ev-f5", usecases.DockAppointmentDoorReassigned{AppointmentID: ghost, ToDoorCode: "DOOR-7", Version: 2}); err != nil || out != usecases.Ignored {
		t.Fatalf("unknown appointment = %s, %v", out, err)
	}
	var claims int
	if err := wd.pool.QueryRow(ctx, `SELECT count(*) FROM processed_events WHERE consumer = $1`, usecases.DockAppointmentConsumer).Scan(&claims); err != nil || claims != 5 {
		t.Fatalf("claims = %d, %v; want 5 (f1, f2, f3, f4, f5)", claims, err)
	}

	// NO-LOOP: only the two ReceiptOpened rows were added.
	got := wd.outboxEventTypes(t)
	want := append(append([]string{}, outboxBefore...),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptOpened"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptOpened"))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("outbox = %v, want %v", got, want)
	}
}
