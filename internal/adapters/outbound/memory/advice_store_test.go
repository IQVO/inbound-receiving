package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func openReceiptAt(t *testing.T, n int, openedAt time.Time) *receipt.Receipt {
	t.Helper()
	a, _, err := asn.Register("ASN-"+string(rune('0'+n)), "ACME", time.Time{}, []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 5}}, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := receipt.Open(rcptID(n), a.Snapshot(), "", "", openedAt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdviceStoreMarkersAreOnceAndShareOneEntry(t *testing.T) {
	ctx := context.Background()
	s := memory.NewAdviceStore(memory.NewReceiptRepo())
	id := receipt.ID(rcptID(1))
	if first, _ := s.MarkOverdue(ctx, id, t0); !first {
		t.Fatal("first overdue marker")
	}
	if first, _ := s.MarkOverdue(ctx, id, t0); first {
		t.Fatal("second overdue marker")
	}
	if first, _ := s.MarkIssued(ctx, id, t0, false); !first {
		t.Fatal("the advice of an overdue receipt is issued once")
	}
	if first, _ := s.MarkIssued(ctx, id, t0, true); first {
		t.Fatal("second issued marker")
	}
}

func TestAdviceStoreOverdueCandidates(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewReceiptRepo()
	s := memory.NewAdviceStore(repo)
	old, fresh, closed := openReceiptAt(t, 1, t0), openReceiptAt(t, 2, t0.Add(10*time.Hour)), openReceiptAt(t, 3, t0)
	for _, r := range []*receipt.Receipt{old, fresh, closed} {
		if err := repo.Save(ctx, r, 0); err != nil {
			t.Fatal(err)
		}
	}
	loaded := closed.Version()
	if _, err := closed.Close(t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, closed, loaded); err != nil {
		t.Fatal(err)
	}

	cutoff := t0.Add(5 * time.Hour) // strictly before: only the one opened at t0 that is still open
	got, err := s.OverdueCandidates(ctx, cutoff, 10)
	if err != nil || len(got) != 1 || got[0].ID() != old.ID() {
		t.Fatalf("candidates = %v, %v", got, err)
	}
	if got, _ := s.OverdueCandidates(ctx, t0, 10); len(got) != 0 {
		t.Fatalf("the cutoff is exclusive: %v", got)
	}
	if got, _ := s.OverdueCandidates(ctx, t0.Add(24*time.Hour), 1); len(got) != 1 {
		t.Fatalf("the limit applies: %v", got)
	}
	if _, err := s.MarkOverdue(ctx, old.ID(), cutoff); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OverdueCandidates(ctx, cutoff, 10); len(got) != 0 {
		t.Fatalf("a marked receipt is not a candidate again: %v", got)
	}
}

func TestAdviceStoreSnapshotRestoresTheMarkers(t *testing.T) {
	ctx := context.Background()
	s := memory.NewAdviceStore(memory.NewReceiptRepo())
	restore := s.Snapshot()
	if first, _ := s.MarkIssued(ctx, receipt.ID(rcptID(1)), t0, true); !first {
		t.Fatal("marker")
	}
	restore()
	if first, _ := s.MarkIssued(ctx, receipt.ID(rcptID(1)), t0, true); !first {
		t.Fatal("a rolled back unit of work must forget its markers")
	}
}
