package main_test

// Wave-2 step definitions: the facility-layout and product-master event feeds
// (consumer use cases, driven directly because the events arrive over Kafka)
// and a few list assertions. Everything else goes over REST and is asserted
// from the response or the outbox.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

type wave2State struct {
	outcome usecases.Outcome
	err     error
	// lastSlot is the most recent slot announcement, kept so the same
	// CloudEvents id can be redelivered.
	lastEventID string
	lastSlot    usecases.LocationSlot
}

func (w *world) nextEventID(prefix string) string {
	w.events++
	return fmt.Sprintf("%s-%d", prefix, w.events)
}

func (w *world) slotRegistry() *usecases.ApplyLocationSlotRegistered {
	return &usecases.ApplyLocationSlotRegistered{UoW: w.uow, ProcessedEvents: w.processed, Doors: w.doors}
}

func (w *world) facilityLayoutAnnouncesSlot(ctx context.Context, code, role, flow string) error {
	w.wave2.lastEventID = w.nextEventID("bdd-slot")
	w.wave2.lastSlot = usecases.LocationSlot{LocationCode: code, Role: role, DockFlow: flow}
	w.wave2.outcome, w.wave2.err = w.slotRegistry().Handle(ctx, w.wave2.lastEventID, w.wave2.lastSlot)
	return nil
}

func (w *world) facilityLayoutRedeliversTheLastSlot(ctx context.Context) error {
	w.wave2.outcome, w.wave2.err = w.slotRegistry().Handle(ctx, w.wave2.lastEventID, w.wave2.lastSlot)
	return nil
}

func (w *world) facilityLayoutDecommissionsSlot(ctx context.Context, code string) error {
	uc := &usecases.ApplyLocationSlotDecommissioned{UoW: w.uow, ProcessedEvents: w.processed, Doors: w.doors}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, w.nextEventID("bdd-decom"), code)
	return nil
}

func (w *world) productMasterRegistersSKU(ctx context.Context, eventID, sku string) error {
	uc := &usecases.ApplyProductRegistered{UoW: w.uow, ProcessedEvents: w.processed, Skus: w.skus}
	w.wave2.outcome, w.wave2.err = uc.Handle(ctx, eventID, sku)
	return nil
}

func (w *world) theConsumerReports(want string) error {
	if w.wave2.err != nil {
		return fmt.Errorf("expected outcome %q, got error %w", want, w.wave2.err)
	}
	if string(w.wave2.outcome) != want {
		return fmt.Errorf("expected outcome %q, got %q", want, w.wave2.outcome)
	}
	return nil
}

func (w *world) theConsumerRefusesTheEvent() error {
	if !errors.Is(w.wave2.err, usecases.ErrInvalidEvent) {
		return fmt.Errorf("expected an invalid-event refusal, got outcome %q err %v", w.wave2.outcome, w.wave2.err)
	}
	return nil
}

// theListedValuesAreAscending asserts the listed values are in ascending order
// (the receipt ids are minted UUIDs, so their exact values are not known).
func (w *world) theListedValuesAreAscending(key string) error {
	got, err := w.listedValues(key)
	if err != nil {
		return err
	}
	if !sort.StringsAreSorted(got) {
		return fmt.Errorf("listed %q values %v are not in ascending order", key, got)
	}
	return nil
}

func (w *world) theListHasItems(n int) error {
	got, err := w.listedValues("")
	if err != nil {
		return err
	}
	if len(got) != n {
		return fmt.Errorf("expected %d listed items, got %d: %s", n, len(got), w.body)
	}
	return nil
}

func (w *world) listedValues(key string) ([]string, error) {
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.body, &page); err != nil {
		return nil, err
	}
	out := make([]string, len(page.Items))
	for i, it := range page.Items {
		out[i] = fmt.Sprint(it[key])
	}
	return out, nil
}

func (w *world) registerWave2(sc *godog.ScenarioContext) {
	sc.Step(`^facility-layout announces slot "([^"]*)" with role "([^"]*)" and dock flow "([^"]*)"$`, w.facilityLayoutAnnouncesSlot)
	sc.Step(`^facility-layout redelivers the last slot announcement$`, w.facilityLayoutRedeliversTheLastSlot)
	sc.Step(`^facility-layout announces slot "([^"]*)" decommissioned$`, w.facilityLayoutDecommissionsSlot)
	sc.Step(`^product-master announces event "([^"]*)" registering SKU "([^"]*)"$`, w.productMasterRegistersSKU)
	sc.Step(`^the consumer reports "(applied|ignored|duplicate)"$`, w.theConsumerReports)
	sc.Step(`^the consumer refuses the event as invalid$`, w.theConsumerRefusesTheEvent)
	sc.Step(`^the listed "([^"]*)" values are in ascending order$`, w.theListedValuesAreAscending)
	sc.Step(`^the list has (\d+) items?$`, w.theListHasItems)
}
