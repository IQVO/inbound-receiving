//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func findingsOf(t *testing.T, r *receipt.Receipt) []receipt.CartonFinding {
	t.Helper()
	return r.CartonDiscrepancies()
}

func rowsOf(t *testing.T, cw *cartonWorld, id string) int {
	t.Helper()
	var n int
	if err := cw.pool.QueryRow(context.Background(), `SELECT count(*) FROM receipt_carton_discrepancies WHERE receipt_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ADR 0015: the carton discrepancies raised by scans and by the close are
// stored with the receipt and come back, in raise order, from Get and List.
func TestCartonDiscrepanciesArePersistedWithTheReceipt(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-F", ssccA, ssccB, ssccC))
	id := cw.open(t, "ASN-F")

	cw.scanSSCC(t, id, ssccA)
	cw.scanSSCC(t, id, ssccA)                // DuplicateSscc
	cw.scanSSCC(t, id, "003456789012345717") // UnexpectedCarton
	if got := rowsOf(t, cw, id); got != 2 {
		t.Fatalf("rows after the scans = %d, want 2", got)
	}
	closed, err := (&usecases.CloseReceipt{Writer: cw.w}).Handle(ctx, usecases.CloseReceiptCommand{ReceiptID: id})
	if err != nil {
		t.Fatal(err)
	}

	repo := postgres.NewReceiptRepo(cw.pool)
	got, err := repo.Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	want := findingsOf(t, closed)
	if len(want) != 4 {
		t.Fatalf("aggregate findings = %+v, want duplicate, unexpected and two missing", want)
	}
	gotFindings := findingsOf(t, got)
	if len(gotFindings) != len(want) {
		t.Fatalf("stored findings = %+v, want %+v", gotFindings, want)
	}
	for i := range want {
		if gotFindings[i].CartonDiscrepancy != want[i].CartonDiscrepancy || !gotFindings[i].At.Equal(want[i].At.Truncate(time.Microsecond)) {
			t.Fatalf("finding %d = %+v, want %+v", i, gotFindings[i], want[i])
		}
	}
	kinds := []receipt.Kind{}
	for _, f := range gotFindings {
		kinds = append(kinds, f.Kind)
	}
	if !reflect.DeepEqual(kinds, []receipt.Kind{receipt.KindDuplicateSscc, receipt.KindUnexpectedCarton, receipt.KindMissingCarton, receipt.KindMissingCarton}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if gotFindings[2].Expected != ssccB || gotFindings[3].Expected != ssccC || gotFindings[2].At.Before(gotFindings[1].At) {
		t.Fatalf("missing findings = %+v", gotFindings[2:])
	}

	// A receipt with nothing raised reads back empty, and List scopes findings per receipt.
	cw.register(t, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-G", SupplierRef: "ACME", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 5}},
	})
	other := cw.open(t, "ASN-G")
	page, err := repo.List(ctx, repository.ReceiptFilter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, x := range page {
		counts[string(x.ID())] = len(x.CartonDiscrepancies())
	}
	if counts[id] != 4 || counts[other] != 0 || len(counts) != 2 {
		t.Fatalf("findings per receipt = %v", counts)
	}
}

// The findings are written in the receipt's own transaction: a save refused by
// the version guard stores none of them, and a later save never duplicates the
// ones already stored.
func TestCartonDiscrepanciesFollowTheReceiptTransaction(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-T", ssccA))
	id := cw.open(t, "ASN-T")
	repo := postgres.NewReceiptRepo(cw.pool)

	winner, err := repo.Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	loser, err := repo.Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := winner.RejectUnknownGtin(gtin(t, eachGTIN), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, winner, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := loser.RejectUnknownGtin(gtin(t, caseGTIN), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, loser, 1); !errors.Is(err, repository.ErrConcurrentModification) {
		t.Fatalf("stale save err = %v, want ErrConcurrentModification", err)
	}
	if got := rowsOf(t, cw, id); got != 1 {
		t.Fatalf("rows = %d, want only the winner's finding", got)
	}

	// Saving the same receipt again with one more finding appends exactly one row.
	again, err := repo.Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.RejectUnknownGtin(gtin(t, caseGTIN), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, again, again.Version()-1); err != nil {
		t.Fatal(err)
	}
	if got := rowsOf(t, cw, id); got != 2 {
		t.Fatalf("rows = %d, want 2 (no duplicate of the first)", got)
	}
	final, err := repo.Get(ctx, receipt.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	f := final.CartonDiscrepancies()
	if len(f) != 2 || f[0].Actual != eachGTIN || f[1].Actual != caseGTIN {
		t.Fatalf("findings = %+v", f)
	}
}

// The finding, the receipt and the outbox rows commit or roll back together:
// when the outbox write fails after the receipt was saved, no finding row and
// no version bump survive.
func TestCartonDiscrepanciesRollBackWithAFailedOutboxWrite(t *testing.T) {
	ctx := context.Background()
	cw := newCartonWorld(t)
	cw.register(t, cartonAsn("ASN-R", ssccA))
	id := cw.open(t, "ASN-R")

	broken := cw.w
	broken.Outbox = failingOutbox{inner: broken.Outbox, err: errors.New("outbox is down")}
	scan := &usecases.ReceiveScan{Writer: broken, Packaging: cw.view}
	_, err := scan.Handle(ctx, usecases.ReceiveScanCommand{ReceiptID: id, Kind: "SSCC", ID: "003456789012345717"})
	if err == nil {
		t.Fatal("the scan must fail when its events cannot be enqueued")
	}
	if got := rowsOf(t, cw, id); got != 0 {
		t.Fatalf("finding rows = %d after a rolled-back scan, want 0", got)
	}
	r, err := postgres.NewReceiptRepo(cw.pool).Get(ctx, receipt.ID(id))
	if err != nil || r.Version() != 1 || len(r.CartonDiscrepancies()) != 0 {
		t.Fatalf("receipt = version %v, %d findings, err %v: want the untouched version 1", r.Version(), len(r.CartonDiscrepancies()), err)
	}

	// The same scan through the healthy writer stores exactly one.
	cw.scanSSCC(t, id, "003456789012345717")
	if got := rowsOf(t, cw, id); got != 1 {
		t.Fatal(fmt.Sprintf("finding rows = %d, want 1", got))
	}
}
