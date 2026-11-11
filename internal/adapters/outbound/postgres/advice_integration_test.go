//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	adviceIssuedType  = typeBase + "ReceivingAdviceIssued"
	adviceOverdueType = typeBase + "ReceivingAdviceOverdue"
)

const adviceSLA = 24 * time.Hour

type adviceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *adviceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *adviceClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// adviceWorld is a cartonWorld whose writer issues the receiving advice with a
// clock the test moves.
type adviceWorld struct {
	*cartonWorld
	advice *postgres.AdviceRepo
	clock  *adviceClock
}

func newAdviceWorld(t *testing.T) *adviceWorld {
	t.Helper()
	cw := newCartonWorld(t)
	aw := &adviceWorld{cartonWorld: cw, advice: postgres.NewAdviceRepo(cw.pool), clock: &adviceClock{now: time.Now().UTC().Truncate(time.Microsecond)}}
	cw.w.Advice, cw.w.AdviceSLA, cw.w.Clock = aw.advice, adviceSLA, aw.clock
	cw.scan.Writer = cw.w
	return aw
}

func (aw *adviceWorld) closeReceipt(t *testing.T, id string) error {
	t.Helper()
	_, err := (&usecases.CloseReceipt{Writer: aw.w}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: id})
	return err
}

func (aw *adviceWorld) sweep(t *testing.T) int {
	t.Helper()
	n, err := (&usecases.SweepAdviceOverdue{Writer: aw.w, Batch: 2}).Handle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func advicePayload(t *testing.T, pool *pgxpool.Pool, eventType, topicSuffix string) map[string]any {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(), `SELECT value FROM outbox_events WHERE event_type = $1 AND topic LIKE '%' || $2 ORDER BY id LIMIT 1`, eventType, topicSuffix).Scan(&raw)
	if err != nil {
		t.Fatalf("outbox row %s on %s: %v", eventType, topicSuffix, err)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

// uniqueSSCC is a valid SSCC no other test ASN of the same database uses.
func uniqueSSCC(t *testing.T, n int) string {
	t.Helper()
	s, err := gs1.NewSSCC(fmt.Sprintf("00345678%04d%05d", 77, n))
	if err != nil {
		t.Fatal(err)
	}
	return s.String()
}

// --- the migration -----------------------------------------------------------

func TestMigration0007AddsTheAdviceStateAndKeepsOldReceipts(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t) // the writer has no AdviceStore: it closes like before the migration
	cw.register(t, cartonAsn("ASN-OLD", ssccA))
	id := cw.open(t, "ASN-OLD")
	cw.scanSSCC(t, id, ssccA)
	if _, err := (&usecases.CloseReceipt{Writer: cw.w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id}); err != nil {
		t.Fatal(err)
	}
	// An old receipt has no advice marker: nothing is issued retroactively.
	if n := countWhere(t, cw.pool, "receipt_advices", "true"); n != 0 {
		t.Fatalf("receipt_advices rows = %d, want 0 (no backfill)", n)
	}
	// Simulate a receipt written before the migration: no last_received_at.
	if _, err := cw.pool.Exec(ctx, `UPDATE receipts SET last_received_at = NULL WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	got, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatalf("a receipt without last_received_at must still load: %v", err)
	}
	if !got.LastReceivedAt().IsZero() || got.AdviceClock().IsZero() {
		t.Fatalf("last = %v clock = %v", got.LastReceivedAt(), got.AdviceClock())
	}
	// Nothing was lost: the closed receipt, its line and its carton are all there.
	if n := countWhere(t, cw.pool, "receipts", "state = 'Closed'"); n != 1 {
		t.Fatalf("closed receipts = %d", n)
	}
	if n := countWhere(t, cw.pool, "receipt_lines", "held_qty = 0"); n != 1 {
		t.Fatalf("lines with the default held_qty = %d", n)
	}
	// The sweep never raises an overdue for a closed receipt.
	aw := &adviceWorld{cartonWorld: cw, advice: postgres.NewAdviceRepo(cw.pool), clock: &adviceClock{now: time.Now().UTC().Add(100 * adviceSLA)}}
	cw.w.Advice, cw.w.AdviceSLA, cw.w.Clock = aw.advice, adviceSLA, aw.clock
	if n := aw.sweep(t); n != 0 {
		t.Fatalf("overdue raised for an old closed receipt: %d", n)
	}
}

func TestReceiptKeepsLastReceivedAndHeldAcrossSaves(t *testing.T) {
	ctx := context.Background()
	aw := newAdviceWorld(t)
	aw.register(t, cartonAsn("ASN-H", ssccA, ssccB))
	id := aw.open(t, "ASN-H")
	aw.clock.advance(time.Minute)
	out := aw.scanSSCC(t, id, ssccA)
	want := out.Receipt.LastReceivedAt()
	if want.IsZero() {
		t.Fatal("the scan must stamp last_received_at")
	}
	hold := &usecases.HoldReceiptLine{Writer: aw.w}
	if _, err := hold.Handle(ctx, usecases.HoldLineCommand{ReceiptID: id, LineNo: 1, Quantity: 5}); err != nil {
		t.Fatal(err)
	}
	got, err := postgres.NewReceiptRepo(aw.pool).Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastReceivedAt().Equal(want) || got.Lines()[0].Held() != 5 {
		t.Fatalf("last = %v (want %v) held = %d", got.LastReceivedAt(), want, got.Lines()[0].Held())
	}
}

// --- the markers -----------------------------------------------------------

func TestAdviceMarkersAreExactlyOnceAndShareOneRow(t *testing.T) {
	ctx := context.Background()
	aw := newAdviceWorld(t)
	aw.register(t, cartonAsn("ASN-M", ssccA))
	id := receipt.ID(aw.open(t, "ASN-M"))
	now := time.Now().UTC()

	if first, err := aw.advice.MarkOverdue(ctx, id, now); err != nil || !first {
		t.Fatalf("first overdue = %v, %v", first, err)
	}
	if first, err := aw.advice.MarkOverdue(ctx, id, now); err != nil || first {
		t.Fatalf("second overdue = %v, %v", first, err)
	}
	if first, err := aw.advice.MarkIssued(ctx, id, now, false); err != nil || !first {
		t.Fatalf("an overdue receipt's advice is still issued once: %v, %v", first, err)
	}
	if first, err := aw.advice.MarkIssued(ctx, id, now, true); err != nil || first {
		t.Fatalf("second issued = %v, %v", first, err)
	}
	if n := countWhere(t, aw.pool, "receipt_advices", "overdue_at IS NOT NULL AND issued_at IS NOT NULL AND within_sla = false"); n != 1 {
		t.Fatalf("the two facts of one receipt must share one row: %d", n)
	}
	if n := countWhere(t, aw.pool, "receipt_advices", "true"); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	// Concurrent markers: exactly one winner.
	aw.register(t, usecases.RegisterAsnCommand{AsnNumber: "ASN-M2", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 5}}})
	other := receipt.ID(aw.open(t, "ASN-M2"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first, err := aw.advice.MarkIssued(ctx, other, now, true)
			if err != nil {
				t.Error(err)
				return
			}
			if first {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// --- at close ----------------------------------------------------------------

func TestCloseIssuesTheAdviceOnceInTheSameTransaction(t *testing.T) {
	aw := newAdviceWorld(t)
	aw.register(t, cartonAsn("ASN-A", ssccA, ssccB))
	id := aw.open(t, "ASN-A")
	aw.scanSSCC(t, id, ssccA)
	aw.clock.advance(time.Hour)
	if err := aw.closeReceipt(t, id); err != nil {
		t.Fatal(err)
	}

	types := outboxTypes(t, aw.pool)
	if countType(types, adviceIssuedType) != 2 {
		t.Fatalf("advice rows = %d, want 2 (integration + analytics): %v", countType(types, adviceIssuedType), types)
	}
	var ids int
	if err := aw.pool.QueryRow(context.Background(), `SELECT count(DISTINCT event_id) FROM outbox_events WHERE event_type = $1`, adviceIssuedType).Scan(&ids); err != nil || ids != 1 {
		t.Fatalf("distinct ids = %d, %v", ids, err)
	}
	closedAt, adviceAt := -1, -1
	for i, ty := range types {
		switch ty {
		case typeBase + "ReceiptClosed":
			if closedAt < 0 {
				closedAt = i
			}
		case adviceIssuedType:
			if adviceAt < 0 {
				adviceAt = i
			}
		}
	}
	if closedAt < 0 || adviceAt < closedAt {
		t.Fatalf("the advice must follow ReceiptClosed: %v", types)
	}
	data := advicePayload(t, aw.pool, adviceIssuedType, "events")
	if data["despatch_advice_ref"] != "ASN-A" || data["supplier_ref"] != "ACME" || data["owner_ref"] != "tenant-7f3a" || data["within_sla"] != true {
		t.Fatalf("payload = %v", data)
	}
	lines := data["lines"].([]any)
	line := lines[0].(map[string]any)
	if line["received_qty"].(float64) != 20 || line["sscc_count"].(float64) != 1 || line["ssccs"].([]any)[0] != ssccA {
		t.Fatalf("line = %v", line)
	}
	findings := data["carton_findings"].([]any)
	if len(findings) != 1 || findings[0].(map[string]any)["kind"] != "MissingCarton" || findings[0].(map[string]any)["w13_code"] != "02" {
		t.Fatalf("findings = %v", findings)
	}
	if n := countWhere(t, aw.pool, "receipt_advices", "issued_at IS NOT NULL AND within_sla = true"); n != 1 {
		t.Fatalf("marker rows = %d", n)
	}
	// A replayed close adds nothing.
	if err := aw.closeReceipt(t, id); err == nil {
		t.Fatal("a second close must be refused")
	}
	if got := countType(outboxTypes(t, aw.pool), adviceIssuedType); got != 2 {
		t.Fatalf("advice rows after a replay = %d, want 2", got)
	}
}

func TestALateCloseRecordsAWithinSLAFalseMarker(t *testing.T) {
	aw := newAdviceWorld(t)
	aw.register(t, cartonAsn("ASN-L", ssccA))
	id := aw.open(t, "ASN-L")
	aw.scanSSCC(t, id, ssccA)
	aw.clock.advance(adviceSLA + time.Minute)
	if err := aw.closeReceipt(t, id); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, aw.pool, "receipt_advices", "within_sla = false"); n != 1 {
		t.Fatalf("late markers = %d", n)
	}
	if data := advicePayload(t, aw.pool, adviceIssuedType, "events"); data["within_sla"] != false {
		t.Fatalf("payload = %v", data)
	}
}

// --- the overdue sweep -------------------------------------------------------

func TestTheOverdueSweepRaisesOnceEvenWithConcurrentSweepers(t *testing.T) {
	aw := newAdviceWorld(t)
	for i := 0; i < 3; i++ {
		number := fmt.Sprintf("ASN-O%d", i)
		cmd := cartonAsn(number, uniqueSSCC(t, i))
		cmd.Cartons[0].ParentSSCC = "" // the shared pallet SSCC is unique per site
		aw.register(t, cmd)
		aw.open(t, number)
	}
	aw.clock.advance(adviceSLA - time.Second)
	if n := aw.sweep(t); n != 0 {
		t.Fatalf("not overdue yet: %d", n)
	}
	aw.clock.advance(2 * time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := (&usecases.SweepAdviceOverdue{Writer: aw.w, Batch: 2}).Handle(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 3 {
		t.Fatalf("raised %d across four concurrent sweepers, want 3 (once per receipt)", total)
	}
	if got := countType(outboxTypes(t, aw.pool), adviceOverdueType); got != 6 {
		t.Fatalf("overdue rows = %d, want 6 (3 receipts x 2 topics)", got)
	}
	if n := countWhere(t, aw.pool, "receipt_advices", "overdue_at IS NOT NULL AND issued_at IS NULL"); n != 3 {
		t.Fatalf("overdue markers = %d", n)
	}
	if n := aw.sweep(t); n != 0 {
		t.Fatalf("a later sweep must be silent: %d", n)
	}
	data := advicePayload(t, aw.pool, adviceOverdueType, "events")
	if data["sla_seconds"].(float64) != 86400 || data["last_carton_at"] == nil || data["due_at"] == nil {
		t.Fatalf("payload = %v", data)
	}
}

func TestTheHousekeepingSweeperRunsTheOverdueSweepOnItsTick(t *testing.T) {
	aw := newAdviceWorld(t)
	aw.register(t, cartonAsn("ASN-T", ssccA))
	aw.open(t, "ASN-T")
	aw.clock.advance(2 * adviceSLA)
	uc := &usecases.SweepAdviceOverdue{Writer: aw.w}
	s := postgres.NewSweeper(aw.pool, postgres.WithAdviceOverdue(uc.Handle))
	res, err := s.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.AdviceOverdue != 1 {
		t.Fatalf("result = %+v, want one overdue receipt", res)
	}
	res, err = s.SweepOnce(context.Background())
	if err != nil || res.AdviceOverdue != 0 {
		t.Fatalf("second tick = %+v, %v", res, err)
	}
}
