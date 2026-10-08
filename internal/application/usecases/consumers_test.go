package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

func productConsumer(w *world) *usecases.ApplyProductRegistered {
	return &usecases.ApplyProductRegistered{UoW: w.uow, ProcessedEvents: w.processed, Skus: w.skus}
}

func registeredConsumer(w *world) *usecases.ApplyLocationSlotRegistered {
	return &usecases.ApplyLocationSlotRegistered{UoW: w.uow, ProcessedEvents: w.processed, Doors: w.doors}
}

func decommissionedConsumer(w *world) *usecases.ApplyLocationSlotDecommissioned {
	return &usecases.ApplyLocationSlotDecommissioned{UoW: w.uow, ProcessedEvents: w.processed, Doors: w.doors}
}

func TestProductRegisteredUpsertsOnce(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := productConsumer(w)
	got, err := uc.Handle(ctx, "evt-1", "SKU-1")
	wantNoErr(t, err)
	if got != usecases.Applied || !w.skus.known["SKU-1"] {
		t.Fatalf("outcome = %s", got)
	}
	got, err = uc.Handle(ctx, "evt-1", "SKU-1")
	wantNoErr(t, err)
	if got != usecases.Duplicate {
		t.Fatalf("outcome = %s", got)
	}
}

func TestProductRegisteredFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := productConsumer(w)
	_, err := uc.Handle(ctx, "evt-2", "bad sku")
	wantErr(t, err, usecases.ErrInvalidEvent)
	w.processed.err = errors.New("db down")
	_, err = uc.Handle(ctx, "evt-3", "SKU-3")
	wantErr(t, err, w.processed.err)
	w.processed.err = nil
	w.skus.err = errors.New("upsert failed")
	_, err = uc.Handle(ctx, "evt-4", "SKU-4")
	wantErr(t, err, w.skus.err)
}

func TestLocationSlotRegisteredIgnoresWhatIsNotAnInboundDock(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := registeredConsumer(w)
	slots := []usecases.LocationSlot{
		{LocationCode: "STOR-1"},
		{LocationCode: "OUT-1", Role: "Dock", DockFlow: "Outbound"},
		{LocationCode: "YARD-1", Role: "Yard"},
		{LocationCode: "NOFLOW", Role: "Dock"},
	}
	for i, slot := range slots {
		got, err := uc.Handle(ctx, "ignored-"+string(rune('a'+i)), slot)
		wantNoErr(t, err)
		if got != usecases.Ignored {
			t.Fatalf("%+v outcome = %s", slot, got)
		}
	}
	if len(w.doors.doors) != 0 || len(w.processed.seen) != 0 {
		t.Fatal("ignored events must change and claim nothing")
	}
}

func TestLocationSlotRegisteredKeepsInboundAndBothDocks(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := registeredConsumer(w)
	got, err := uc.Handle(ctx, "e1", usecases.LocationSlot{LocationCode: "IN-1", Role: "Dock", DockFlow: "Inbound"})
	wantNoErr(t, err)
	if got != usecases.Applied || w.doors.doors["IN-1"].Flow != repository.DockFlowInbound {
		t.Fatalf("outcome = %s doors = %v", got, w.doors.doors)
	}
	_, err = uc.Handle(ctx, "e2", usecases.LocationSlot{LocationCode: "IO-1", Role: "Dock", DockFlow: "Both"})
	wantNoErr(t, err)
	if w.doors.doors["IO-1"].Flow != repository.DockFlowBoth {
		t.Fatalf("doors = %v", w.doors.doors)
	}
	got, err = uc.Handle(ctx, "e1", usecases.LocationSlot{LocationCode: "IN-1", Role: "Dock", DockFlow: "Inbound"})
	wantNoErr(t, err)
	if got != usecases.Duplicate {
		t.Fatalf("outcome = %s", got)
	}
}

func TestLocationSlotRegisteredFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	uc := registeredConsumer(w)
	_, err := uc.Handle(ctx, "e3", usecases.LocationSlot{LocationCode: "a/b", Role: "Dock", DockFlow: "Both"})
	wantErr(t, err, usecases.ErrInvalidEvent)
	w.processed.err = errors.New("db down")
	_, err = uc.Handle(ctx, "e9", usecases.LocationSlot{LocationCode: "IN-9", Role: "Dock", DockFlow: "Both"})
	wantErr(t, err, w.processed.err)
}

func TestLocationSlotDecommissionedRemovesTheDoor(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	_, err := registeredConsumer(w).Handle(ctx, "e1", usecases.LocationSlot{LocationCode: "IN-1", Role: "Dock", DockFlow: "Inbound"})
	wantNoErr(t, err)
	dec := decommissionedConsumer(w)

	got, err := dec.Handle(ctx, "d1", "IN-1")
	wantNoErr(t, err)
	if got != usecases.Applied || len(w.doors.doors) != 0 {
		t.Fatalf("outcome = %s doors = %v", got, w.doors.doors)
	}
	got, err = dec.Handle(ctx, "d2", "NEVER-A-DOOR")
	wantNoErr(t, err)
	if got != usecases.Applied {
		t.Fatalf("unknown code outcome = %s", got)
	}
	got, err = dec.Handle(ctx, "d1", "IN-1")
	wantNoErr(t, err)
	if got != usecases.Duplicate {
		t.Fatalf("outcome = %s", got)
	}
}

func TestLocationSlotDecommissionedFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	dec := decommissionedConsumer(w)
	_, err := dec.Handle(ctx, "d3", "bad code")
	wantErr(t, err, usecases.ErrInvalidEvent)
	w.processed.err = errors.New("db down")
	_, err = dec.Handle(ctx, "d9", "IN-9")
	wantErr(t, err, w.processed.err)
}
