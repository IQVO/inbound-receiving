//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	ssccA    = "003456789012345670"
	ssccB    = "003456789012345687"
	ssccC    = "003456789012345694"
	ssccPal  = "003456789099999995"
	eachGTIN = "10001234567892"
	caseGTIN = "20001234567851"
	typeBase = "com.warehouse.wms.inbound-receiving.receipt."
)

func gtin(t *testing.T, s string) gs1.GTIN {
	t.Helper()
	g, err := gs1.ParseGTIN(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func cartonAsn(number string, ssccs ...string) usecases.RegisterAsnCommand {
	cartons := make([]asn.CartonInput, 0, len(ssccs))
	for _, s := range ssccs {
		cartons = append(cartons, asn.CartonInput{
			SSCC: s, ParentSSCC: ssccPal, Kind: "HOMOGENEOUS_CASE", GrossWeightG: 12000,
			Dims:     &asn.DimsInput{LengthMm: 400, WidthMm: 300, HeightMm: 200},
			Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN, Qty: 20, Lot: "L1", Expiry: "2027-10-31"}},
		})
	}
	return usecases.RegisterAsnCommand{
		AsnNumber: number, SupplierRef: "ACME", OwnerRef: "tenant-7f3a",
		Lines:   []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: int64(20 * len(ssccs))}},
		Cartons: cartons,
	}
}

type cartonWorld struct {
	pool    *pgxpool.Pool
	w       usecases.Writer
	view    *postgres.PackagingView
	declare *usecases.ApplyPackLevelDeclared
	retire  *usecases.ApplyPackLevelRetired
	scan    *usecases.ReceiveScan
}

func newCartonWorld(t *testing.T) *cartonWorld {
	t.Helper()
	pool := startPostgresPool(t)
	w, view, processed := newWriter(pool), postgres.NewPackagingView(pool), postgres.NewProcessedEventRepo(pool)
	cw := &cartonWorld{
		pool: pool, w: w, view: view,
		declare: &usecases.ApplyPackLevelDeclared{UoW: w.UoW, ProcessedEvents: processed, Packaging: view},
		retire:  &usecases.ApplyPackLevelRetired{UoW: w.UoW, ProcessedEvents: processed, Packaging: view},
		scan:    &usecases.ReceiveScan{Writer: w, Packaging: view},
	}
	from := time.Now().Add(-time.Hour).UTC()
	for i, l := range []usecases.PackLevelDeclared{
		{SKU: "SKU-1", Level: "EACH", GTIN: eachGTIN, UnitsPerPack: 1, EffectiveFrom: from, Version: 1},
		{SKU: "SKU-1", Level: "CASE", GTIN: caseGTIN, ChildGTIN: eachGTIN, QtyOfChild: 20, UnitsPerPack: 20, EffectiveFrom: from, Version: 1},
	} {
		if _, err := cw.declare.Handle(context.Background(), "evt-"+string(rune('a'+i)), l); err != nil {
			t.Fatal(err)
		}
	}
	return cw
}

func (cw *cartonWorld) register(t *testing.T, cmd usecases.RegisterAsnCommand) {
	t.Helper()
	if _, err := (&usecases.RegisterAsn{Writer: cw.w}).Handle(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
}

func (cw *cartonWorld) open(t *testing.T, number string) string {
	t.Helper()
	r, err := (&usecases.OpenReceipt{Writer: cw.w}).Handle(context.Background(), usecases.OpenReceiptCommand{AsnNumber: number})
	if err != nil {
		t.Fatal(err)
	}
	return string(r.ID())
}

func (cw *cartonWorld) scanSSCC(t *testing.T, receiptID, sscc string) usecases.ScanOutcome {
	t.Helper()
	out, err := cw.scan.Handle(context.Background(), usecases.ReceiveScanCommand{ReceiptID: receiptID, Kind: "SSCC", ID: sscc})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPackagingViewLookupsAndVersionRules(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	g := gtin(t, caseGTIN)
	l, err := cw.view.ByGTIN(ctx, g)
	if err != nil || l.SKU != "SKU-1" || l.Level != shared.PackCase || l.UnitsPerPack != 20 || l.QtyOfChild != 20 || l.ChildGTIN != gtin(t, eachGTIN) || l.Version != 1 {
		t.Fatalf("by gtin = %+v, %v", l, err)
	}
	if _, err := cw.view.ByGTIN(ctx, gtin(t, "6291041500213")); !errors.Is(err, repository.ErrPackLevelNotFound) {
		t.Fatalf("unknown gtin: %v", err)
	}
	now := time.Now()
	if a, err := cw.view.ActiveLevel(ctx, "SKU-1", shared.PackCase, now); err != nil || a.GTIN != g {
		t.Fatalf("active level = %+v, %v", a, err)
	}
	if _, err := cw.view.ActiveLevel(ctx, "SKU-1", shared.PackCase, now.Add(-24*time.Hour)); !errors.Is(err, repository.ErrPackLevelNotFound) {
		t.Fatalf("before effective_from: %v", err)
	}
	if _, err := cw.view.ActiveLevel(ctx, "SKU-1", shared.PackPallet, now); !errors.Is(err, repository.ErrPackLevelNotFound) {
		t.Fatalf("no pallet level: %v", err)
	}

	// A stale or repeated version is ignored; a newer one overwrites.
	stale := usecases.PackLevelDeclared{SKU: "SKU-1", Level: "CASE", GTIN: caseGTIN, ChildGTIN: eachGTIN, QtyOfChild: 24, UnitsPerPack: 24, EffectiveFrom: now.Add(-time.Hour), Version: 1}
	if out, err := cw.declare.Handle(ctx, "evt-stale", stale); err != nil || out != usecases.Ignored {
		t.Fatalf("stale = %s, %v", out, err)
	}
	stale.Version = 2
	if out, err := cw.declare.Handle(ctx, "evt-newer", stale); err != nil || out != usecases.Applied {
		t.Fatalf("newer = %s, %v", out, err)
	}
	if l, _ = cw.view.ByGTIN(ctx, g); l.UnitsPerPack != 24 || l.Version != 2 {
		t.Fatalf("after newer = %+v", l)
	}
	if out, err := cw.declare.Handle(ctx, "evt-newer", stale); err != nil || out != usecases.Duplicate {
		t.Fatalf("redelivery = %s, %v", out, err)
	}

	// Retirement end-dates the level: no longer active afterwards.
	to := now.Add(time.Minute).UTC().Truncate(time.Microsecond)
	if out, err := cw.retire.Handle(ctx, "evt-retire", caseGTIN, to, 3); err != nil || out != usecases.Applied {
		t.Fatalf("retire = %s, %v", out, err)
	}
	if _, err := cw.view.ActiveLevel(ctx, "SKU-1", shared.PackCase, to.Add(time.Second)); !errors.Is(err, repository.ErrPackLevelNotFound) {
		t.Fatalf("after retirement: %v", err)
	}
	if l, _ = cw.view.ByGTIN(ctx, g); !l.EffectiveTo.Equal(to) || l.Version != 3 {
		t.Fatalf("retired = %+v", l)
	}
	if out, err := cw.retire.Handle(ctx, "evt-retire-unknown", "6291041500213", to, 1); err != nil || out != usecases.Ignored {
		t.Fatalf("retire unknown = %s, %v", out, err)
	}
}

func TestCartonAsnRoundTripsAndFlatAsnStoresNothingNew(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA, ssccB))
	cw.register(t, usecases.RegisterAsnCommand{AsnNumber: "ASN-FLAT", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 5}}})

	asns := postgres.NewAsnRepo(cw.pool)
	got, err := asns.Get(ctx, "ASN-C")
	if err != nil || got.OwnerRef() != "tenant-7f3a" || len(got.Cartons()) != 2 {
		t.Fatalf("carton asn = %+v, %v", got, err)
	}
	c := got.Cartons()[0]
	d, hasDims := c.Dims()
	x := c.Contents()[0]
	if c.SSCC().String() != ssccA || c.ParentSSCC().String() != ssccPal || c.Kind() != asn.CartonHomogeneousCase || c.GrossWeightG() != 12000 ||
		!hasDims || d.LengthMm() != 400 || d.WidthMm() != 300 || d.HeightMm() != 200 ||
		x.LineNo() != 1 || x.SKU() != "SKU-1" || x.GTIN().String() != eachGTIN || x.Qty() != 20 || x.Lot() != "L1" || x.Expiry() != "2027-10-31" {
		t.Fatalf("carton = %+v / %+v", c, x)
	}
	if got.Cartons()[1].SSCC().String() != ssccB {
		t.Fatalf("announced order lost: %v", got.Cartons())
	}

	flat, err := asns.Get(ctx, "ASN-FLAT")
	if err != nil || flat.OwnerRef() != "" || len(flat.Cartons()) != 0 {
		t.Fatalf("flat asn = %+v, %v", flat, err)
	}
	var cartons, contents int
	if err := cw.pool.QueryRow(ctx, `SELECT count(*) FROM asn_cartons WHERE asn_number = 'ASN-FLAT'`).Scan(&cartons); err != nil {
		t.Fatal(err)
	}
	if err := cw.pool.QueryRow(ctx, `SELECT count(*) FROM asn_carton_contents WHERE asn_number = 'ASN-FLAT'`).Scan(&contents); err != nil {
		t.Fatal(err)
	}
	var owner *string
	if err := cw.pool.QueryRow(ctx, `SELECT owner_ref FROM asns WHERE asn_number = 'ASN-FLAT'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if cartons != 0 || contents != 0 || owner != nil {
		t.Fatalf("a flat ASN stored cartons=%d contents=%d owner=%v", cartons, contents, owner)
	}

	// The roll-up must not be bypassed: the pallet SSCC cannot be a carton elsewhere.
	_, err = (&usecases.RegisterAsn{Writer: cw.w}).Handle(ctx, cartonAsn("ASN-DUP", ssccA))
	wantErr(t, err, asn.ErrDuplicateSscc)
	pallet := cartonAsn("ASN-PAL", ssccPal)
	pallet.Cartons[0].ParentSSCC = ""
	_, err = (&usecases.RegisterAsn{Writer: cw.w}).Handle(ctx, pallet)
	wantErr(t, err, asn.ErrDuplicateSscc)
}

func TestACancelledAsnReleasesItsSsccs(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-1", ssccA))
	_, err := (&usecases.RegisterAsn{Writer: cw.w}).Handle(ctx, cartonAsn("ASN-2", ssccA))
	wantErr(t, err, asn.ErrDuplicateSscc)
	if _, err := (&usecases.CancelAsn{Writer: cw.w}).Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1"}); err != nil {
		t.Fatal(err)
	}
	cw.register(t, cartonAsn("ASN-2", ssccA))
	got, err := postgres.NewAsnRepo(cw.pool).Get(ctx, "ASN-1")
	if err != nil || len(got.Cartons()) != 1 || got.State() != asn.Cancelled {
		t.Fatalf("the cancelled ASN keeps its history: %+v, %v", got, err)
	}
}

func TestScanBySsccPersistsTheLedgerAndWritesFullTypedOutboxRows(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA, ssccB, ssccC))
	id := cw.open(t, "ASN-C")

	if out := cw.scanSSCC(t, id, ssccA); !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("scan A = %+v", out.Result)
	}
	dup := cw.scanSSCC(t, id, ssccA)
	if dup.Result.Accepted || dup.Result.Discrepancies[0].Kind != receipt.KindDuplicateSscc {
		t.Fatalf("duplicate = %+v", dup.Result)
	}
	if out := cw.scanSSCC(t, id, "003456789012345717"); out.Result.Accepted || out.Result.Discrepancies[0].Kind != receipt.KindUnexpectedCarton {
		t.Fatalf("unexpected = %+v", out.Result)
	}
	if _, err := (&usecases.CloseReceipt{Writer: cw.w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id}); err != nil {
		t.Fatal(err)
	}

	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil || r.OwnerRef() != "tenant-7f3a" {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
	status := map[string]receipt.CartonStatus{}
	for _, c := range r.Cartons() {
		status[c.SSCC().String()] = c.Status()
	}
	if status[ssccA] != receipt.CartonAccepted || status[ssccB] != receipt.CartonExpected || status[ssccC] != receipt.CartonExpected {
		t.Fatalf("ledger = %v", status)
	}

	// Every receipt event is written once per topic it is published on: the
	// carton event count fixes that factor, the rest must be a multiple of it.
	counts := map[string]int{}
	for _, ty := range outboxTypes(t, cw.pool) {
		counts[ty]++
	}
	k := counts[typeBase+"ReceiptCartonReceived"]
	if k == 0 || counts[typeBase+"ReceiptLineReceived"] != k || counts[typeBase+"ReceiptOpened"] != k || counts[typeBase+"ReceiptClosed"] != k ||
		counts[typeBase+"CartonDiscrepancyRaised"] != 4*k { // duplicate, unexpected, and B and C missing at close
		t.Fatalf("outbox = %v", counts)
	}
	assertEventTypesFull(t, cw.pool)
}

// assertEventTypesFull proves every carton event row stores the FULL
// CloudEvents type and a payload without siteId.
func assertEventTypesFull(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT event_type, value FROM outbox_events WHERE event_type LIKE '%ReceiptCartonReceived' OR event_type LIKE '%CartonDiscrepancyRaised'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var ty string
		var value []byte
		if err := rows.Scan(&ty, &value); err != nil {
			t.Fatal(err)
		}
		if ty != typeBase+"ReceiptCartonReceived" && ty != typeBase+"CartonDiscrepancyRaised" {
			t.Fatalf("event_type %q is not a full CloudEvents type", ty)
		}
		var env struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(value, &env); err != nil {
			t.Fatal(err)
		}
		if _, has := env.Data["site_id"]; has || env.Type != ty {
			t.Fatalf("envelope = %s", value)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no carton events in the outbox")
	}
}

func TestScanByCaseGTINAndEachGTINPersist(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-C", ssccA, ssccB))
	id := cw.open(t, "ASN-C")

	out, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Kind: "CASE_GTIN", ID: caseGTIN, Qty: 2, Lot: "L9"})
	if err != nil || !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 40 {
		t.Fatalf("case scan = %+v, %v", out.Result, err)
	}
	out, err = cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Kind: "EACH_GTIN", ID: eachGTIN, Qty: 3})
	if err != nil || out.Receipt.Lines()[0].ReceivedGood() != 43 {
		t.Fatalf("each scan = %+v, %v", out.Result, err)
	}
	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil || r.Lines()[0].ReceivedGood() != 43 {
		t.Fatalf("stored receipt = %+v, %v", r, err)
	}
	internal := 0
	for _, c := range r.Cartons() {
		if c.Status() == receipt.CartonAccepted && c.SSCC().IsZero() {
			internal++
			if len(c.LPN()) != len("LPN-")+26 || c.Contents()[0].Lot != "L9" || !c.Sealed() {
				t.Fatalf("internal carton = %+v", c)
			}
		}
	}
	if internal != 2 {
		t.Fatalf("internal LPN cartons = %d, want 2", internal)
	}
}

// TestConcurrentScansOfTheSameSsccAcceptItOnce fires the same SSCC scan
// several times at once: exactly one acceptance is stored, the losers are
// refused by the version guard (or the accepted-SSCC unique index).
func TestConcurrentScansOfTheSameSsccAcceptItOnce(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-1", ssccA))
	id := cw.open(t, "ASN-1")
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := cw.scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Kind: "SSCC", ID: ssccA})
			switch {
			case err == nil && out.Result.Accepted:
				accepted.Add(1)
			case err == nil || errors.Is(err, repository.ErrConcurrentModification):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := cw.pool.QueryRow(ctx, `SELECT count(*) FROM receipt_cartons WHERE sscc = $1 AND status = 'Accepted'`, ssccA).Scan(&n); err != nil || n != 1 || accepted.Load() != 1 {
		t.Fatalf("accepted rows = %d (%v), acceptances reported = %d", n, err, accepted.Load())
	}
	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil || r.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("a duplicate acceptance must not double-count: %+v, %v", r, err)
	}
}
