//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const labelPrefix = "0614141"

func newAssign(cw *cartonWorld, prefix string) (*usecases.AssignSscc, *postgres.SsccSerials) {
	p, err := receipt.ParseCompanyPrefix(prefix)
	if err != nil {
		panic(err)
	}
	serials := postgres.NewSsccSerials(cw.pool)
	return &usecases.AssignSscc{Writer: cw.w, Packaging: cw.view, Prefix: p, Serials: serials}, serials
}

func TestSsccSerialsComeFromASequencePerPrefixAndNeverRepeat(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	serials := postgres.NewSsccSerials(pool)
	seven, _ := receipt.ParseCompanyPrefix(labelPrefix)
	other, _ := receipt.ParseCompanyPrefix("0614142")

	// The sequence is created on demand: no EnsureSequence call before the first serial.
	for want := int64(1); want <= 3; want++ {
		if got, err := serials.NextSerial(ctx, seven); err != nil || got != want {
			t.Fatalf("NextSerial = %d, %v; want %d", got, err, want)
		}
	}
	if got, err := serials.NextSerial(ctx, other); err != nil || got != 1 {
		t.Fatalf("a second prefix has its own sequence: %d, %v", got, err)
	}
	if err := serials.EnsureSequence(ctx, seven); err != nil {
		t.Fatalf("EnsureSequence on an existing sequence: %v", err)
	}
	if got, _ := serials.NextSerial(ctx, seven); got != 4 {
		t.Fatalf("EnsureSequence must not reset the sequence, got %d", got)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT sequencename FROM pg_sequences WHERE sequencename = 'sscc_serial_0614141'`).Scan(&name); err != nil {
		t.Fatalf("the sequence is not a Postgres SEQUENCE: %v", err)
	}
}

func TestSsccSerialsAreDistinctUnderConcurrencyEvenWhenTheFirstCallsRace(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	serials := postgres.NewSsccSerials(pool)
	prefix, _ := receipt.ParseCompanyPrefix("0614143")
	var mu sync.Mutex
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := serials.NextSerial(ctx, prefix)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || seen[n] || n < 1 || n > 24 {
				t.Errorf("serial %d (err %v), seen before: %v", n, err, seen[n])
			}
			seen[n] = true
		}()
	}
	wg.Wait()
	if len(seen) != 24 {
		t.Fatalf("%d distinct serials, want 24", len(seen))
	}
}

func TestSsccSerialsRunOutAtTheLargestSerialThatFitsAndNeverCycle(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	serials := postgres.NewSsccSerials(pool)
	ten, _ := receipt.ParseCompanyPrefix("0614141123")
	if err := serials.EnsureSequence(ctx, ten); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT setval('sscc_serial_0614141123', 999998)`); err != nil {
		t.Fatal(err)
	}
	if got, err := serials.NextSerial(ctx, ten); err != nil || got != 999999 {
		t.Fatalf("the last serial = %d, %v", got, err)
	}
	for range 2 {
		if _, err := serials.NextSerial(ctx, ten); !errors.Is(err, repository.ErrSsccSerialsExhausted) {
			t.Fatalf("past the last serial: %v, want ErrSsccSerialsExhausted (the sequence must not cycle)", err)
		}
	}
}

func TestAssignSsccPersistsTheReceiverCartonAndItsEventsCarrySsccSource(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA, ssccB))
	id := cw.open(t, "ASN-C")
	assign, _ := newAssign(cw, labelPrefix)

	out, err := assign.Handle(ctx, usecases.AssignSsccCommand{ReceiptID: id, GTIN: caseGTIN})
	if err != nil {
		t.Fatal(err)
	}
	if out.SSCC.String() != "006141410000000012" {
		t.Fatalf("sscc = %s", out.SSCC)
	}
	again, err := assign.Handle(ctx, usecases.AssignSsccCommand{ReceiptID: id, GTIN: caseGTIN})
	if err != nil || again.SSCC.String() != "006141410000000029" {
		t.Fatalf("second = %s, %v", again.SSCC, err)
	}

	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil || r.Lines()[0].ReceivedGood() != 40 {
		t.Fatalf("stored receipt = %+v, %v", r, err)
	}
	c, ok := r.Carton(out.SSCC)
	if !ok || c.SSCCSource() != receipt.SsccReceiver || c.LPN() != out.SSCC.String() || c.Status() != receipt.CartonAccepted {
		t.Fatalf("stored carton = %+v, %v", c, ok)
	}
	var source *string
	if err := cw.pool.QueryRow(ctx, `SELECT sscc_source FROM receipt_cartons WHERE receipt_id = $1 AND sscc = $2`, id, out.SSCC.String()).Scan(&source); err != nil || source == nil || *source != "receiver" {
		t.Fatalf("sscc_source column = %v, %v", source, err)
	}
	// A manifest carton (supplier SSCC) has no stored source and still reads as supplier.
	if err := cw.pool.QueryRow(ctx, `SELECT sscc_source FROM receipt_cartons WHERE receipt_id = $1 AND sscc = $2`, id, ssccA).Scan(&source); err != nil || source != nil {
		t.Fatalf("manifest carton sscc_source = %v, %v", source, err)
	}
	if m, _ := r.Carton(gtinSSCC(t, ssccA)); m.SSCCSource() != receipt.SsccSupplier {
		t.Fatalf("manifest carton source = %q", m.SSCCSource())
	}
	if got := countFindings(t, cw, id, "UnlabelledCarton"); got != 2 {
		t.Fatalf("UnlabelledCarton findings = %d, want one per assignment", got)
	}
	payloads := cartonPayloads(t, cw)
	if !strings.Contains(payloads, `"sscc_source":"receiver"`) || !strings.Contains(payloads, `"sscc":"006141410000000012"`) {
		t.Fatalf("ReceiptCartonReceived payloads = %s", payloads)
	}
}

func TestTheAcceptedSsccIndexRefusesAnAssignedSSCCReceivedTwice(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA))
	cw.register(t, usecases.RegisterAsnCommand{AsnNumber: "ASN-D", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}}})
	idC, idD := cw.open(t, "ASN-C"), cw.open(t, "ASN-D")
	assign, _ := newAssign(cw, labelPrefix)
	out, err := assign.Handle(ctx, usecases.AssignSsccCommand{ReceiptID: idC, GTIN: caseGTIN})
	if err != nil {
		t.Fatal(err)
	}
	// The printed label is scanned on ANOTHER receipt: the site-wide check sees it as received.
	dup, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: idD, Kind: "SSCC", ID: out.SSCC.String()})
	if err != nil || dup.Result.Accepted || dup.Result.Discrepancies[0].Kind != receipt.KindDuplicateSscc {
		t.Fatalf("second acceptance = %+v, %v", dup.Result, err)
	}
	var n int
	if err := cw.pool.QueryRow(ctx, `SELECT count(*) FROM receipt_cartons WHERE sscc = $1 AND status = 'Accepted'`, out.SSCC.String()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("accepted rows for the assigned SSCC = %d, %v", n, err)
	}
}

func TestLabelDataAndTheLabelFindingsRoundTripThroughPostgres(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA, ssccB))
	id := cw.open(t, "ASN-C")
	sites, err := receipt.ParseSiteGLNs("4012345000009")
	if err != nil {
		t.Fatal(err)
	}
	cw.scan.SiteGLNs = sites

	raw := "]C100" + ssccA + "11260901" + "13260902" + "15270300" + "17270630" + "4104012345000009" + "413" + "7612345000008" + "21SER1\x1d" + "400PO77"
	if out, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Scan: raw}); err != nil || !out.Result.Accepted {
		t.Fatalf("label scan = %+v, %v", out.Result, err)
	}
	// AI 01 with AI 02: refused and recorded, with the supplier.
	if out, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Scan: "]C101" + eachGTIN + "02" + eachGTIN + "3720"}); err != nil || out.Result.Accepted {
		t.Fatalf("non compliant label = %+v, %v", out.Result, err)
	}
	// A label for another GLN.
	if out, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Scan: "]C100" + ssccB + "4107612345000008"}); err != nil || out.Result.Accepted {
		t.Fatalf("wrong destination = %+v, %v", out.Result, err)
	}

	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	want := receipt.LabelData{
		BestBefore: "2027-03-31", Expiry: "2027-06-30", ProductionDate: "2026-09-01", PackagingDate: "2026-09-02",
		Serial: "SER1", PurchaseOrder: "PO77", ShipToGLN: "4012345000009", ShipForGLN: "7612345000008",
	}
	c, _ := r.Carton(gtinSSCC(t, ssccA))
	if c.Label() != want {
		t.Fatalf("stored label = %+v, want %+v", c.Label(), want)
	}
	if d := r.LineDates(1); d.BestBefore != "2027-03-31" || d.Expiry != "2027-06-30" {
		t.Fatalf("earliest after reload = %+v", d)
	}
	if p := r.Pallets(); len(p) != 1 || p[0].CartonCount != 1 || p[0].Earliest.BestBefore != "2027-03-31" {
		t.Fatalf("pallets = %+v", p)
	}
	findings := r.CartonDiscrepancies()
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	if f := findings[0]; f.Kind != receipt.KindLabelNonCompliant || f.Expected != "conflicting_ais" || f.SupplierRef != "ACME" || !f.SSCC.IsZero() {
		t.Fatalf("label finding = %+v", f)
	}
	if f := findings[1]; f.Kind != receipt.KindWrongDestination || f.Actual != "7612345000008" || f.SupplierRef != "ACME" || f.SSCC.String() != ssccB {
		t.Fatalf("destination finding = %+v", f)
	}
}

func TestMigration0008IsAdditiveAndWidensTheKindCheck(t *testing.T) {
	ctx := context.Background()
	pool := startPostgresPool(t)
	for _, col := range [][2]string{
		{"receipt_cartons", "sscc_source"}, {"receipt_cartons", "label"}, {"receipt_carton_discrepancies", "supplier_ref"},
	} {
		var nullable string
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, col[0], col[1]).Scan(&nullable); err != nil || nullable != "YES" {
			t.Fatalf("%s.%s nullable = %q, %v: the migration must add a nullable column", col[0], col[1], nullable, err)
		}
	}
	var def string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'receipt_carton_discrepancies_kind_check'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"LabelNonCompliant", "WrongDestination", "UnexpectedCarton", "MixedCartonContentMismatch"} {
		if !strings.Contains(def, kind) {
			t.Errorf("the kind check does not list %s: %s", kind, def)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO receipt_cartons (receipt_id, position, lpn, kind, status, contents, sscc_source) VALUES ('x', 1, 'l', 'MIXED', 'Expected', '[]', 'somebody')`); err == nil {
		t.Fatal("an unknown sscc_source was accepted by the database")
	}
}

func gtinSSCC(t *testing.T, s string) gs1.SSCC {
	t.Helper()
	v, err := gs1.ParseSSCC(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func countFindings(t *testing.T, cw *cartonWorld, id, kind string) int {
	t.Helper()
	var n int
	if err := cw.pool.QueryRow(context.Background(), `SELECT count(*) FROM receipt_carton_discrepancies WHERE receipt_id = $1 AND kind = $2`, id, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// cartonPayloads concatenates the ReceiptCartonReceived values of the outbox.
func cartonPayloads(t *testing.T, cw *cartonWorld) string {
	t.Helper()
	rows, err := cw.pool.Query(context.Background(), `SELECT convert_from(value, 'UTF8') FROM outbox_events WHERE event_type = $1 ORDER BY id`, typeBase+"ReceiptCartonReceived")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s)
	}
	return b.String()
}
