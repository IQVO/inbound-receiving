package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// stowedReceipt is a Closed receipt opened from aID(1) with one accepted
// Good carton; it returns the receipt and the carton's LPN.
func stowedReceipt(t *testing.T) (*receipt.Receipt, string) {
	t.Helper()
	sscc, err := gs1.NewSSCC("00345678901234567")
	if err != nil {
		t.Fatal(err)
	}
	g, err := gs1.NewGTIN("1000123456789")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := asn.RegisterDraft(asn.Draft{
		Number: "ASN-1", SupplierRef: "ACME", Lines: []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 20}},
		Cartons: []asn.CartonInput{{SSCC: sscc.String(), Kind: "HOMOGENEOUS_CASE", Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g.String(), Qty: 20}}}},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := receipt.Open(rcptID(1), a.Snapshot(), apptID(1), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(receipt.CartonScan{SSCC: sscc, Condition: receipt.ConditionGood}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return r, sscc.String()
}

func TestStowStoreKeepsTheEarliestFactPerKey(t *testing.T) {
	ctx := context.Background()
	s := memory.NewStowStore(memory.NewReceiptRepo())
	for i, tc := range []struct {
		at   time.Time
		want bool
	}{{t0, true}, {t0.Add(time.Minute), false}, {t0, false}, {t0.Add(-time.Minute), true}} {
		if got, _ := s.ApplyArrival(ctx, aID(1), "v", tc.at); got != tc.want {
			t.Fatalf("arrival %d applied = %v, want %v", i, got, tc.want)
		}
		if got, _ := s.ApplyCartonStow(ctx, "LPN-1", tc.at); got != tc.want {
			t.Fatalf("stow %d applied = %v, want %v", i, got, tc.want)
		}
	}
	if first, _ := s.Complete(ctx, rID(1), t0); !first {
		t.Fatal("the first marker must be reported")
	}
	if first, _ := s.Complete(ctx, rID(1), t0); first {
		t.Fatal("the second marker must not be reported")
	}
}

func TestStowStoreLooksUpReceiptsAndReadsEvidence(t *testing.T) {
	ctx := context.Background()
	receipts := memory.NewReceiptRepo()
	s := memory.NewStowStore(receipts)
	r, lpn := stowedReceipt(t)
	if err := receipts.Save(ctx, r, 0); err != nil {
		t.Fatal(err)
	}
	byAppt, _ := s.ReceiptsOfAppointment(ctx, aID(1))
	byCarton, _ := s.ReceiptsOfCarton(ctx, lpn)
	none, _ := s.ReceiptsOfCarton(ctx, "LPN-nobody")
	other, _ := s.ReceiptsOfAppointment(ctx, aID(2))
	if len(byAppt) != 1 || len(byCarton) != 1 || len(none) != 0 || len(other) != 0 {
		t.Fatalf("lookups = %d %d %d %d", len(byAppt), len(byCarton), len(none), len(other))
	}
	if ev, _ := s.Evidence(ctx, r); !ev.ArrivedAt.IsZero() || len(ev.Stows) != 0 {
		t.Fatalf("evidence before any fact = %+v", ev)
	}
	_, _ = s.ApplyArrival(ctx, aID(1), "v", t0)
	_, _ = s.ApplyCartonStow(ctx, lpn, t0.Add(2*time.Hour))
	_, _ = s.ApplyCartonStow(ctx, "LPN-other", t0)
	ev, _ := s.Evidence(ctx, r)
	if !ev.ArrivedAt.Equal(t0) || len(ev.Stows) != 1 || !ev.Stows[lpn].Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("evidence = %+v", ev)
	}
}

func TestStowStoreSnapshotRestoresEveryTable(t *testing.T) {
	ctx := context.Background()
	s := memory.NewStowStore(memory.NewReceiptRepo())
	_, _ = s.ApplyArrival(ctx, aID(1), "v", t0)
	_, _ = s.ApplyCartonStow(ctx, "LPN-1", t0)
	_, _ = s.Complete(ctx, rID(1), t0)
	restore := s.Snapshot()
	_, _ = s.ApplyArrival(ctx, aID(2), "v", t0)
	_, _ = s.ApplyCartonStow(ctx, "LPN-2", t0)
	_, _ = s.Complete(ctx, rID(2), t0)
	restore()
	if got, _ := s.ApplyArrival(ctx, aID(2), "v", t0); !got {
		t.Fatal("the arrival added after the snapshot survived the restore")
	}
	if got, _ := s.ApplyCartonStow(ctx, "LPN-2", t0); !got {
		t.Fatal("the stow added after the snapshot survived the restore")
	}
	if first, _ := s.Complete(ctx, rID(2), t0); !first {
		t.Fatal("the marker added after the snapshot survived the restore")
	}
	if first, _ := s.Complete(ctx, rID(1), t0); first {
		t.Fatal("the marker present at the snapshot was lost")
	}
}

func aID(n int) appointment.ID { return appointment.ID(apptID(n)) }
func rID(n int) receipt.ID     { return receipt.ID(rcptID(n)) }
