package main_test

// Dock-to-stock step definitions (ADR 0021). yard-management's VisitCheckedIn
// and inventory-storage's ContainerStowed arrive over Kafka, so the steps drive
// the same consumer use cases the Kafka adapters call; the effect is asserted
// on the outbox, where ReceiptStowCompleted lands.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

const stowEventType = eventTypePrefix + "receipt.ReceiptStowCompleted"

// stowFeed is the last yard arrival and the last carton stow fed to the
// copies, kept so the same CloudEvents id can be redelivered.
type stowFeed struct {
	arrivalID, stowID string
	arrival           usecases.VisitCheckedIn
	stow              usecases.ContainerStowed
}

func (w *world) arrivalConsumer() *usecases.ApplyVisitCheckedIn {
	return &usecases.ApplyVisitCheckedIn{Writer: w.writer, ProcessedEvents: w.processed}
}

func (w *world) stowConsumer() *usecases.ApplyContainerStowed {
	return &usecases.ApplyContainerStowed{Writer: w.writer, ProcessedEvents: w.processed}
}

func (w *world) feedArrival(ctx context.Context, appointment, at string) error {
	arrived, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return err
	}
	w.stowFeed.arrival = usecases.VisitCheckedIn{VisitID: "visit-bdd", AppointmentID: appointment, ArrivedAt: arrived, Version: 1}
	w.stowFeed.arrivalID = w.nextEventID("bdd-visit")
	w.wave2.outcome, w.wave2.err = w.arrivalConsumer().Handle(ctx, w.stowFeed.arrivalID, w.stowFeed.arrival)
	return w.wave2.err
}

func (w *world) theYardChecksInTheAppointment(ctx context.Context, at string) error {
	return w.feedArrival(ctx, w.appointment, at)
}

func (w *world) theYardChecksInAWalkIn(ctx context.Context, at string) error {
	return w.feedArrival(ctx, "", at)
}

func (w *world) theYardRedeliversTheArrival(ctx context.Context) error {
	w.wave2.outcome, w.wave2.err = w.arrivalConsumer().Handle(ctx, w.stowFeed.arrivalID, w.stowFeed.arrival)
	return w.wave2.err
}

func (w *world) inventoryStowsTheCarton(ctx context.Context, lpn, at string) error {
	stowed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return err
	}
	w.stowFeed.stow = usecases.ContainerStowed{LPN: lpn, StowedAt: stowed}
	w.stowFeed.stowID = w.nextEventID("bdd-stow")
	w.wave2.outcome, w.wave2.err = w.stowConsumer().Handle(ctx, w.stowFeed.stowID, w.stowFeed.stow)
	return w.wave2.err
}

func (w *world) inventoryRedeliversTheStow(ctx context.Context) error {
	w.wave2.outcome, w.wave2.err = w.stowConsumer().Handle(ctx, w.stowFeed.stowID, w.stowFeed.stow)
	return w.wave2.err
}

func (w *world) stowCompletions() []json.RawMessage {
	var out []json.RawMessage
	for _, m := range w.outbox.Messages() {
		if m.EventType == stowEventType { // the BDD world encodes the integration stream only
			out = append(out, m.Value)
		}
	}
	return out
}

func (w *world) theOutboxHoldsStowCompletions(n int) error {
	if got := len(w.stowCompletions()); got != n {
		return fmt.Errorf("ReceiptStowCompleted events = %d, want %d (outbox %v)", got, n, w.outboxTypes())
	}
	return nil
}

func (w *world) outboxTypes() []string {
	var got []string
	for _, m := range w.outbox.Messages() {
		got = append(got, m.EventType)
	}
	return got
}

func (w *world) theStowCompletionCarries(seconds int64, cartons int) error {
	all := w.stowCompletions()
	if len(all) != 1 {
		return fmt.Errorf("want exactly one ReceiptStowCompleted, got %d", len(all))
	}
	var env struct {
		Subject string `json:"subject"`
		Data    struct {
			ReceiptID          string `json:"receipt_id"`
			AppointmentID      string `json:"appointment_id"`
			DockToStockSeconds int64  `json:"dock_to_stock_seconds"`
			CartonCount        int    `json:"carton_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(all[0], &env); err != nil {
		return err
	}
	d := env.Data
	if d.DockToStockSeconds != seconds || d.CartonCount != cartons || d.ReceiptID != w.receipt || env.Subject != w.receipt || d.AppointmentID != w.appointment {
		return fmt.Errorf("ReceiptStowCompleted = %+v subject %q, want %d s, %d cartons, receipt %s, appointment %s",
			d, env.Subject, seconds, cartons, w.receipt, w.appointment)
	}
	return nil
}

func (w *world) registerStows(sc *godog.ScenarioContext) {
	sc.Step(`^the yard checks the appointment's trailer in at "([^"]*)"$`, w.theYardChecksInTheAppointment)
	sc.Step(`^the yard checks a walk-in trailer in at "([^"]*)"$`, w.theYardChecksInAWalkIn)
	sc.Step(`^the yard redelivers the last arrival$`, w.theYardRedeliversTheArrival)
	sc.Step(`^inventory stows the carton "([^"]*)" at "([^"]*)"$`, w.inventoryStowsTheCarton)
	sc.Step(`^inventory redelivers the last stow$`, w.inventoryRedeliversTheStow)
	sc.Step(`^the outbox holds (\d+) ReceiptStowCompleted events?$`, w.theOutboxHoldsStowCompletions)
	sc.Step(`^the dock-to-stock fact is (\d+) seconds over (\d+) cartons?$`, w.theStowCompletionCarries)
}
