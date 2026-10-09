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
	book         *usecases.BookAppointment
	checkIn      *usecases.CheckInAppointment
	cancelAppt   *usecases.CancelAppointment
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
		register:     &usecases.RegisterAsn{Writer: w},
		cancelAsn:    &usecases.CancelAsn{Writer: w},
		book:         &usecases.BookAppointment{Writer: w},
		checkIn:      &usecases.CheckInAppointment{Writer: w},
		cancelAppt:   &usecases.CancelAppointment{Writer: w},
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

// TestUsecasesAppointmentToClosedReceipt walks the aggregate lifecycle the
// service exists for: register -> book a door window -> check in -> open the
// receipt against the appointment -> receive a short line -> close. Every
// state change must be persisted (read model) and published (outbox), and
// the close must complete the ASN and the appointment in the SAME unit of
// work (their events land together).
func TestUsecasesAppointmentToClosedReceipt(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-2",
		usecases.AsnLineInput{LineNo: 1, SKU: "SKU-C", ExpectedQty: 10})

	// Window 12:15-13:00 at fixed now 12:00: bookable (starts after now) and
	// check-in-able (from 30 minutes before the window starts).
	windowStart := wiredClock.now.Add(15 * time.Minute)
	appt, err := wd.book.Handle(ctx, usecases.BookAppointmentCommand{
		DoorCode: "DOOR-1", Carrier: "ACME Freight",
		WindowStart: windowStart, WindowEnd: windowStart.Add(45 * time.Minute),
		AsnNumbers: []string{"ASN-ITCOV-2"},
	})
	if err != nil {
		t.Fatalf("book appointment: %v", err)
	}
	if appt.State() != "Booked" || appt.Version() != 1 || appt.DoorCode() != "DOOR-1" {
		t.Fatalf("after book: %+v", appt)
	}

	if _, err := wd.checkIn.Handle(ctx, usecases.AppointmentActionCommand{
		AppointmentID: string(appt.ID())}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	rcpt, err := wd.openReceipt.Handle(ctx, usecases.OpenReceiptCommand{
		AsnNumber: "ASN-ITCOV-2", AppointmentID: string(appt.ID())})
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

	// The close completed the whole chain: ASN Closed, appointment Completed.
	a, err = wd.getAsn.Handle(ctx, "ASN-ITCOV-2")
	if err != nil {
		t.Fatalf("get asn after close: %v", err)
	}
	if a.State() != "Closed" {
		t.Fatalf("asn state after close = %s, want Closed", a.State())
	}

	got := wd.outboxEventTypes(t)
	want := []string{
		cloudevents.Type(outboundkafka.EntityAsn, "ASNRegistered"),
		cloudevents.Type(outboundkafka.EntityAppointment, "DockAppointmentBooked"),
		cloudevents.Type(outboundkafka.EntityAppointment, "DockAppointmentCheckedIn"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptOpened"),
		cloudevents.Type(outboundkafka.EntityReceipt, "ReceiptLineReceived"),
		cloudevents.Type(outboundkafka.EntityAppointment, "DockAppointmentCompleted"),
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

// TestUsecasesBookAppointmentRejectsOverlapAndUnknownAsn pins the two
// domain policies a booking must enforce against real Postgres: a door is
// never double-booked in an overlapping window, and a covered ASN must
// exist and still be receivable.
func TestUsecasesBookAppointmentRejectsOverlapAndUnknownAsn(t *testing.T) {
	wd := newWiredUsecases(t)
	ctx := context.Background()
	mustRegisterAsn(t, wd, "ASN-ITCOV-3",
		usecases.AsnLineInput{LineNo: 1, SKU: "SKU-D", ExpectedQty: 1})

	windowStart := wiredClock.now.Add(2 * time.Hour)
	base := usecases.BookAppointmentCommand{
		DoorCode: "DOOR-2", Carrier: "ACME Freight",
		WindowStart: windowStart, WindowEnd: windowStart.Add(time.Hour),
		AsnNumbers: []string{"ASN-ITCOV-3"},
	}
	if _, err := wd.book.Handle(ctx, base); err != nil {
		t.Fatalf("first booking: %v", err)
	}

	// Same door, overlapping window: rejected, nothing persisted.
	overlap := base
	overlap.WindowStart = windowStart.Add(30 * time.Minute)
	overlap.WindowEnd = windowStart.Add(90 * time.Minute)
	if _, err := wd.book.Handle(ctx, overlap); err == nil {
		t.Fatal("overlapping booking of one door must be rejected")
	}

	// An unknown ASN is a domain rejection, not a 500.
	unknown := base
	unknown.DoorCode = "DOOR-3"
	unknown.AsnNumbers = []string{"ASN-DOES-NOT-EXIST"}
	if _, err := wd.book.Handle(ctx, unknown); !errors.Is(err, usecases.ErrUnknownAsn) {
		t.Fatalf("booking an unknown ASN = %v, want ErrUnknownAsn", err)
	}

	// Only the first booking (plus its ASN registration) hit the outbox.
	events := wd.outboxEventTypes(t)
	booked := 0
	for _, e := range events {
		if e == cloudevents.Type(outboundkafka.EntityAppointment, "DockAppointmentBooked") {
			booked++
		}
	}
	if booked != 1 {
		t.Fatalf("booked events = %d, want exactly 1 (events: %v)", booked, events)
	}

	// ...and the read model agrees: one appointment on DOOR-2.
	var count int
	if err := wd.pool.QueryRow(ctx,
		`SELECT count(*) FROM appointments WHERE door_code = 'DOOR-2'`).Scan(&count); err != nil {
		t.Fatalf("count appointments: %v", err)
	}
	if count != 1 {
		t.Fatalf("appointments on DOOR-2 = %d, want 1", count)
	}
}
