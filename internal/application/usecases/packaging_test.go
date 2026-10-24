package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

func packagingUCs(w *world, p *fakePackaging) (*usecases.ApplyPackLevelDeclared, *usecases.ApplyPackLevelRetired) {
	return &usecases.ApplyPackLevelDeclared{UoW: w.uow, ProcessedEvents: w.processed, Packaging: p},
		&usecases.ApplyPackLevelRetired{UoW: w.uow, ProcessedEvents: w.processed, Packaging: p}
}

func declared() usecases.PackLevelDeclared {
	return usecases.PackLevelDeclared{
		SKU: "SKU-1", Level: "CASE", GTIN: "20001234567851", ChildGTIN: "10001234567892", QtyOfChild: 12, UnitsPerPack: 12,
		EffectiveFrom: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Version: 3,
	}
}

func TestPackLevelDeclaredFeedsThePackagingView(t *testing.T) {
	ctx := context.Background()
	w, p := newWorld(), newFakePackaging()
	declare, _ := packagingUCs(w, p)
	got, err := declare.Handle(ctx, "evt-1", declared())
	wantNoErr(t, err)
	l := p.levels[mustGTIN(t, "2000123456785")]
	if got != usecases.Applied || l.SKU != "SKU-1" || l.Level != shared.PackCase || l.UnitsPerPack != 12 || l.QtyOfChild != 12 ||
		l.ChildGTIN.String() != "10001234567892" || l.Version != 3 || !l.EffectiveTo.IsZero() {
		t.Fatalf("outcome %s level %+v", got, l)
	}
	again, err := declare.Handle(ctx, "evt-1", declared())
	wantNoErr(t, err)
	if again != usecases.Duplicate {
		t.Fatalf("redelivery = %s", again)
	}
	stale := declared()
	stale.Version = 2
	got, err = declare.Handle(ctx, "evt-2", stale)
	wantNoErr(t, err)
	if got != usecases.Ignored || p.levels[mustGTIN(t, "2000123456785")].Version != 3 {
		t.Fatalf("a stale event must be ignored: %s", got)
	}
}

func TestPackLevelDeclaredAcceptsAnEachLevelWithoutAChild(t *testing.T) {
	w, p := newWorld(), newFakePackaging()
	declare, _ := packagingUCs(w, p)
	d := declared()
	d.Level, d.GTIN, d.ChildGTIN, d.QtyOfChild, d.UnitsPerPack = "EACH", "10001234567892", "", 0, 1
	got, err := declare.Handle(context.Background(), "evt-1", d)
	wantNoErr(t, err)
	if l := p.levels[mustGTIN(t, "1000123456789")]; got != usecases.Applied || !l.ChildGTIN.IsZero() || l.Level != shared.PackEach {
		t.Fatalf("outcome %s level %+v", got, l)
	}
}

func TestPackLevelDeclaredRejectsWhatItCanNeverApply(t *testing.T) {
	cases := map[string]func(d *usecases.PackLevelDeclared){
		"bad sku":            func(d *usecases.PackLevelDeclared) { d.SKU = "bad sku" },
		"unknown level":      func(d *usecases.PackLevelDeclared) { d.Level = "BOX" },
		"bad gtin":           func(d *usecases.PackLevelDeclared) { d.GTIN = "123" },
		"zero units":         func(d *usecases.PackLevelDeclared) { d.UnitsPerPack = 0 },
		"no effective_from":  func(d *usecases.PackLevelDeclared) { d.EffectiveFrom = time.Time{} },
		"no version":         func(d *usecases.PackLevelDeclared) { d.Version = 0 },
		"case without child": func(d *usecases.PackLevelDeclared) { d.ChildGTIN = "" },
		"bad child gtin":     func(d *usecases.PackLevelDeclared) { d.ChildGTIN = "123" },
	}
	for name, mutate := range cases {
		w, p := newWorld(), newFakePackaging()
		declare, _ := packagingUCs(w, p)
		d := declared()
		mutate(&d)
		if _, err := declare.Handle(context.Background(), "evt-1", d); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(p.levels) != 0 || w.processed.seen["evt-1"] {
			t.Errorf("%s: an invalid event must claim nothing and store nothing", name)
		}
	}
}

func TestPackLevelDeclaredFailuresAreTransient(t *testing.T) {
	boom := errors.New("boom")
	w, p := newWorld(), newFakePackaging()
	declare, _ := packagingUCs(w, p)
	w.processed.err = boom
	if _, err := declare.Handle(context.Background(), "evt-1", declared()); !errors.Is(err, boom) {
		t.Fatalf("claim: %v", err)
	}
	w.processed.err, p.storeErr = nil, boom
	if _, err := declare.Handle(context.Background(), "evt-2", declared()); !errors.Is(err, boom) {
		t.Fatalf("store: %v", err)
	}
}

func TestPackLevelRetiredEndDatesTheLevel(t *testing.T) {
	ctx := context.Background()
	w, p := newWorld(), newFakePackaging()
	declare, retire := packagingUCs(w, p)
	_, err := declare.Handle(ctx, "evt-1", declared())
	wantNoErr(t, err)
	to := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	got, err := retire.Handle(ctx, "evt-2", "20001234567851", to, 4)
	wantNoErr(t, err)
	l := p.levels[mustGTIN(t, "2000123456785")]
	if got != usecases.Applied || !l.EffectiveTo.Equal(to) || l.Version != 4 {
		t.Fatalf("outcome %s level %+v", got, l)
	}
	if l.ActiveAt(to) || !l.ActiveAt(to.Add(-time.Second)) {
		t.Fatal("a retired level is active until its end date, not after")
	}
	again, err := retire.Handle(ctx, "evt-2", "20001234567851", to, 4)
	wantNoErr(t, err)
	if again != usecases.Duplicate {
		t.Fatalf("redelivery = %s", again)
	}
	unknown, err := retire.Handle(ctx, "evt-3", "10001234567892", to, 5)
	wantNoErr(t, err)
	if unknown != usecases.Ignored {
		t.Fatalf("a GTIN the view never saw = %s", unknown)
	}
}

func TestPackLevelRetiredRejectsWhatItCanNeverApply(t *testing.T) {
	w, p := newWorld(), newFakePackaging()
	_, retire := packagingUCs(w, p)
	to := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if _, err := retire.Handle(context.Background(), "evt-1", "123", to, 1); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("bad gtin: %v", err)
	}
	if _, err := retire.Handle(context.Background(), "evt-1", "20001234567851", time.Time{}, 1); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("no effective_to: %v", err)
	}
	boom := errors.New("boom")
	w.processed.err = boom
	if _, err := retire.Handle(context.Background(), "evt-1", "20001234567851", to, 1); !errors.Is(err, boom) {
		t.Fatalf("claim: %v", err)
	}
	w.processed.err, p.storeErr = nil, boom
	if _, err := retire.Handle(context.Background(), "evt-2", "20001234567851", to, 1); !errors.Is(err, boom) {
		t.Fatalf("store: %v", err)
	}
}
