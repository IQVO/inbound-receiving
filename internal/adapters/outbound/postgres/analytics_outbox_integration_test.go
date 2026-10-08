//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/application/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// ADR 0006: every domain event is enqueued TWICE in the same unit of work, on
// the integration topic and on the analytics topic, under ONE CloudEvents id.

type analyticsOutboxRow struct {
	eventID, topic, eventType, dataschema, value string
}

func analyticsOutboxRows(t *testing.T, pool *pgxpool.Pool) []analyticsOutboxRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT event_id, topic, event_type, dataschema, convert_from(value, 'UTF8') FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []analyticsOutboxRow
	for rows.Next() {
		var r analyticsOutboxRow
		if err := rows.Scan(&r.eventID, &r.topic, &r.eventType, &r.dataschema, &r.value); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// failingOutbox inserts for real, then fails: the surrounding unit of work
// must roll BOTH rows back.
type failingOutbox struct {
	inner ports.OutboxRepository
	err   error
}

func (f failingOutbox) Insert(ctx context.Context, msgs ...outbox.Message) error {
	if err := f.inner.Insert(ctx, msgs...); err != nil {
		return err
	}
	return f.err
}

func TestFanout_Postgres_EachEventLandsOnBothTopicsUnderOneIDInOneTransaction(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)

	registerAsn(t, w, "ASN-1")
	rcpt, err := (&usecases.OpenReceipt{Writer: w}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"})
	if err != nil {
		t.Fatal(err)
	}
	rcpt, err = (&usecases.ReceiveLine{Writer: w}).Handle(ctx, usecases.ReceiveLineCommand{
		ReceiptID: string(rcpt.ID()), LineNo: 1, Quantity: 8, Condition: "Good", ExpectedVersion: rcpt.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&usecases.CloseReceipt{Writer: w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: string(rcpt.ID()), ExpectedVersion: rcpt.Version()}); err != nil {
		t.Fatal(err)
	}

	rows := analyticsOutboxRows(t, pool)
	events := []string{"asn.ASNRegistered", "receipt.ReceiptOpened", "receipt.ReceiptLineReceived", "receipt.ReceiptClosed"}
	if len(rows) != 2*len(events) {
		t.Fatalf("outbox rows = %d, want %d (four events x two topics)", len(rows), 2*len(events))
	}
	seen := map[string]bool{}
	for i, ev := range events {
		in, an := rows[2*i], rows[2*i+1]
		if seen[in.eventID] {
			t.Errorf("%s: occurrence id %q reused", ev, in.eventID)
		}
		seen[in.eventID] = true
		assertFanoutPair(t, ev, in, an)
	}
}

// assertFanoutPair checks one occurrence's integration and analytics rows:
// topics, one shared id carried in the bytes, one type, the two dataschemas,
// and bytes that differ only in the dataschema (ADR 0006 section 2). ev is
// "<entity>.<EventName>".
func assertFanoutPair(t *testing.T, ev string, in, an analyticsOutboxRow) {
	t.Helper()
	name := ev[strings.Index(ev, ".")+1:]
	if in.topic != outboundkafka.Topic || an.topic != outboundkafka.AnalyticsTopic {
		t.Fatalf("%s topics = %q, %q", ev, in.topic, an.topic)
	}
	if in.eventID == "" || in.eventID != an.eventID {
		t.Errorf("%s: ids %q vs %q must be the SAME per occurrence", ev, in.eventID, an.eventID)
	}
	if in.eventType != "com.warehouse.wms.inbound-receiving."+ev || an.eventType != in.eventType {
		t.Errorf("%s: types %q / %q", ev, in.eventType, an.eventType)
	}
	if in.dataschema != "urn:warehouse:inbound-receiving:events:"+name+":v1" || an.dataschema != "urn:warehouse:inbound-receiving:analytics:"+name+":v1" {
		t.Errorf("%s: dataschemas %q / %q", ev, in.dataschema, an.dataschema)
	}
	if !strings.Contains(an.value, `"id":"`+an.eventID+`"`) || !strings.Contains(in.value, `"id":"`+in.eventID+`"`) {
		t.Errorf("%s: the persisted bytes do not carry the row's CloudEvents id", ev)
	}
	if strings.Replace(in.value, ":events:", ":analytics:", 1) != an.value {
		t.Errorf("%s: analytics bytes differ from the integration bytes beyond the dataschema:\n%s\n%s", ev, in.value, an.value)
	}
}

func TestFanout_Postgres_AFailureWritesNeitherTopicsRows(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	w := newWriter(pool)
	registerAsn(t, w, "ASN-1")
	boom := errors.New("injected failure after the real outbox write")
	failing := w
	failing.Outbox = failingOutbox{inner: w.Outbox, err: boom}
	if _, err := (&usecases.OpenReceipt{Writer: failing}).Handle(ctx, usecases.OpenReceiptCommand{AsnNumber: "ASN-1"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	rows := analyticsOutboxRows(t, pool)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2 (only the ASNRegistered pair survives)", len(rows))
	}
	if rows[1].topic != outboundkafka.AnalyticsTopic || !strings.HasSuffix(rows[1].eventType, "ASNRegistered") {
		t.Fatalf("surviving rows = %+v", rows)
	}
}
