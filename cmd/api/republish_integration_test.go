//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
)

// The stage B backfill (ADR 0019) against a real, migrated Postgres, driven
// through the same entry point the binary uses (runCommand), with nothing but
// DATABASE_URL set: no broker, no HTTP server, no consumer.

const (
	legacyPrefix = typePrefix + "dockappointment."
	t0Republish  = "2026-10-09T07:00:00Z"
)

func republishApptID(n int) string {
	return fmt.Sprintf("appt-00000000-0000-4000-8000-%012d", n)
}

// seedLegacyAppointments inserts one appointment per legacy state, the way a
// pre-transfer database holds them: version 1+ (the migration resets it to 0
// only when it runs on existing rows, so the seed uses the reset value).
func seedLegacyAppointments(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	t0, err := time.Parse(time.RFC3339, t0Republish)
	if err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"Booked", "CheckedIn", "Completed", "Cancelled"} {
		id := republishApptID(i + 1)
		if _, err := pool.Exec(ctx, `INSERT INTO appointments (id, door_code, carrier, window_start, window_end, state, version, created_at, updated_at)
			VALUES ($1, 'DOOR-1', 'ACME Freight', $2, $3, $4, 0, $5, $6)`,
			id, t0, t0.Add(time.Hour), state, t0.Add(-48*time.Hour), t0.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatalf("seed %s: %v", state, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO appointment_asns (appointment_id, position, asn_number) VALUES ($1, 1, $2)`,
			id, fmt.Sprintf("ASN-%d", i+1)); err != nil {
			t.Fatalf("seed asn %s: %v", state, err)
		}
	}
}

// outboxByAppointment returns, per appointment id, the sorted short event
// names enqueued for it.
func outboxByAppointment(t *testing.T, pool *pgxpool.Pool) map[string][]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT subject, event_type, topic FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][]string{}
	for rows.Next() {
		var subject, typ, topic string
		if err := rows.Scan(&subject, &typ, &topic); err != nil {
			t.Fatal(err)
		}
		if topic != topicEvents {
			t.Fatalf("a backfill row went to %q, want only %q", topic, topicEvents)
		}
		got[subject] = append(got[subject], strings.TrimPrefix(typ, legacyPrefix))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, names := range got {
		sort.Strings(names)
	}
	return got
}

func TestRepublishAppointmentsEnqueuesEveryStoredAppointment(t *testing.T) {
	_, url := startPostgres(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	pool := newPGView(t, url).pool
	seedLegacyAppointments(t, pool)
	t.Setenv("DATABASE_URL", url)

	run := func() (int, string, string) {
		var out, errOut bytes.Buffer
		code := runCommand(context.Background(), []string{republishCommand}, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	code, out, errOut := run()
	if code != 0 || out != "republished 4 dock appointments\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	want := map[string][]string{
		republishApptID(1): {"DockAppointmentBooked"},
		republishApptID(2): {"DockAppointmentBooked", "DockAppointmentCheckedIn"},
		republishApptID(3): {"DockAppointmentBooked", "DockAppointmentCheckedIn", "DockAppointmentCompleted"},
		republishApptID(4): {"DockAppointmentBooked", "DockAppointmentCancelled"},
	}
	first := outboxByAppointment(t, pool)
	if !equalHistories(first, want) {
		t.Fatalf("enqueued = %v, want %v", first, want)
	}

	// The wire payload is the legacy contract dock-scheduling's importer reads.
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT value FROM outbox_events WHERE subject = $1 AND event_type = $2`,
		republishApptID(1), legacyPrefix+"DockAppointmentBooked").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ev envelope
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Source != source || ev.Subject != republishApptID(1) {
		t.Fatalf("envelope = %+v, %v", ev, err)
	}
	var data map[string]any
	if err := json.Unmarshal(ev.Data, &data); err != nil || data["door_code"] != "DOOR-1" || data["carrier"] != "ACME Freight" {
		t.Fatalf("booked data = %v, %v", data, err)
	}

	// Running it again is safe: the appointments are untouched and the same
	// history is enqueued again (the importer imports an id once).
	code, out, errOut = run()
	if code != 0 || out != "republished 4 dock appointments\n" {
		t.Fatalf("second run: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	var rows, versions int
	if err := pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(version), 0) FROM appointments`).Scan(&rows, &versions); err != nil || rows != 4 || versions != 0 {
		t.Fatalf("appointments after two runs = %d rows, version sum %d, %v", rows, versions, err)
	}
	var total int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events`).Scan(&total); err != nil || total != 16 {
		t.Fatalf("outbox rows after two runs = %d, %v, want 16", total, err)
	}
}

func equalHistories(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv := b[k]
		if strings.Join(av, ",") != strings.Join(bv, ",") {
			return false
		}
	}
	return true
}

func TestRepublishAppointmentsOnAnEmptyTablePrintsZero(t *testing.T) {
	_, url := startPostgres(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", url)
	var out, errOut bytes.Buffer
	if code := runCommand(context.Background(), []string{republishCommand}, &out, &errOut); code != 0 || out.String() != "republished 0 dock appointments\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRepublishAppointmentsNeedsDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var out, errOut bytes.Buffer
	if code := runCommand(context.Background(), []string{republishCommand}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "DATABASE_URL is required") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if code := runCommand(context.Background(), []string{"nope"}, &out, &errOut); code != 2 {
		t.Fatalf("an unknown command exited %d, want 2", code)
	}
}
