package main_test

// Receiving advice step definitions (ADR 0023). The advice is raised by the
// close over HTTP and the overdue alarm by the housekeeping sweep use case, so
// the steps drive those and read the outbox, where both events land.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

const (
	adviceIssuedType  = eventTypePrefix + "receipt.ReceivingAdviceIssued"
	adviceOverdueType = eventTypePrefix + "receipt.ReceivingAdviceOverdue"
)

// adviceSLA is the committed window the BDD world runs with (the default).
const adviceSLA = 24 * time.Hour

type adviceDiscrepancy struct {
	Kind           string `json:"kind"`
	Quantity       int64  `json:"quantity"`
	W13Code        string `json:"w13_code"`
	CBVDisposition string `json:"cbv_disposition"`
}

type adviceLine struct {
	LineNo        int                 `json:"line_no"`
	ReceivedQty   int64               `json:"received_qty"`
	AcceptedQty   int64               `json:"accepted_qty"`
	RejectedQty   int64               `json:"rejected_qty"`
	HeldQty       int64               `json:"held_qty"`
	Discrepancies []adviceDiscrepancy `json:"discrepancies"`
}

type advicePayload struct {
	AsnNumber         string       `json:"asn_number"`
	DespatchAdviceRef string       `json:"despatch_advice_ref"`
	SupplierRef       string       `json:"supplier_ref"`
	WithinSLA         bool         `json:"within_sla"`
	SLASeconds        int64        `json:"sla_seconds"`
	Lines             []adviceLine `json:"lines"`
}

func (w *world) messagesOf(eventType string) []json.RawMessage {
	var out []json.RawMessage
	for _, m := range w.outbox.Messages() {
		if m.EventType == eventType {
			out = append(out, m.Value)
		}
	}
	return out
}

func (w *world) theAdvice() (advicePayload, error) {
	all := w.messagesOf(adviceIssuedType)
	if len(all) != 1 {
		return advicePayload{}, fmt.Errorf("want exactly one ReceivingAdviceIssued, got %d (outbox %v)", len(all), w.outboxTypes())
	}
	var env struct {
		Subject string        `json:"subject"`
		Data    advicePayload `json:"data"`
	}
	if err := json.Unmarshal(all[0], &env); err != nil {
		return advicePayload{}, err
	}
	if env.Subject != w.receipt {
		return advicePayload{}, fmt.Errorf("advice subject %q, want the receipt %q", env.Subject, w.receipt)
	}
	return env.Data, nil
}

func (w *world) theAdviceIsFor(asn, supplier, window string) error {
	a, err := w.theAdvice()
	if err != nil {
		return err
	}
	inside := window == "inside"
	if a.AsnNumber != asn || a.DespatchAdviceRef != asn || a.SupplierRef != supplier || a.WithinSLA != inside || a.SLASeconds != int64(adviceSLA/time.Second) {
		return fmt.Errorf("advice = %+v, want ASN %s from %s, %s the %d s SLA", a, asn, supplier, window, int64(adviceSLA/time.Second))
	}
	return nil
}

func (w *world) adviceLineByNo(no int) (adviceLine, error) {
	a, err := w.theAdvice()
	if err != nil {
		return adviceLine{}, err
	}
	for _, l := range a.Lines {
		if l.LineNo == no {
			return l, nil
		}
	}
	return adviceLine{}, fmt.Errorf("the advice has no line %d: %+v", no, a.Lines)
}

func (w *world) theAdviceLineQuantities(no int, received, accepted, rejected, held int64) error {
	l, err := w.adviceLineByNo(no)
	if err != nil {
		return err
	}
	if l.ReceivedQty != received || l.AcceptedQty != accepted || l.RejectedQty != rejected || l.HeldQty != held {
		return fmt.Errorf("advice line %d = %+v, want received %d accepted %d rejected %d held %d", no, l, received, accepted, rejected, held)
	}
	return nil
}

func (w *world) theAdviceLineHasDiscrepancy(no int, kind string, qty int64, w13, cbv string) error {
	l, err := w.adviceLineByNo(no)
	if err != nil {
		return err
	}
	for _, d := range l.Discrepancies {
		if d.Kind == kind {
			if d.Quantity != qty || d.W13Code != w13 || d.CBVDisposition != cbv {
				return fmt.Errorf("advice line %d %s = %+v, want %d coded %s / %s", no, kind, d, qty, w13, cbv)
			}
			return nil
		}
	}
	return fmt.Errorf("advice line %d has no %s discrepancy: %+v", no, kind, l.Discrepancies)
}

func (w *world) theAdviceLineHasNoDiscrepancy(no int) error {
	l, err := w.adviceLineByNo(no)
	if err != nil {
		return err
	}
	if len(l.Discrepancies) != 0 {
		return fmt.Errorf("advice line %d has discrepancies: %+v", no, l.Discrepancies)
	}
	return nil
}

func (w *world) theOperatorHolds(ctx context.Context, qty int64, line int) error {
	_, err := (&usecases.HoldReceiptLine{Writer: w.writer}).Handle(ctx, usecases.HoldLineCommand{ReceiptID: w.receipt, LineNo: line, Quantity: qty})
	w.holdErr = err
	return nil
}

func (w *world) theHoldIsRefused() error {
	if !errors.Is(w.holdErr, receipt.ErrHoldExceedsGood) {
		if w.holdErr == nil {
			return errors.New("the hold was accepted, want ErrHoldExceedsGood")
		}
		return fmt.Errorf("want ErrHoldExceedsGood, got: %w", w.holdErr)
	}
	return nil
}

func (w *world) theHousekeepingSweepRuns(ctx context.Context) error {
	_, err := (&usecases.SweepAdviceOverdue{Writer: w.writer}).Handle(ctx)
	return err
}

func (w *world) theOutboxHoldsOfType(n int, eventType string) error {
	if got := len(w.messagesOf(eventType)); got != n {
		return fmt.Errorf("%s events = %d, want %d (outbox %v)", eventType, got, n, w.outboxTypes())
	}
	return nil
}

func (w *world) theOverdueAlarmSaysDueAt(due string) error {
	all := w.messagesOf(adviceOverdueType)
	if len(all) != 1 {
		return fmt.Errorf("want exactly one ReceivingAdviceOverdue, got %d", len(all))
	}
	var env struct {
		Subject string `json:"subject"`
		Data    struct {
			DueAt      string `json:"due_at"`
			SLASeconds int64  `json:"sla_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(all[0], &env); err != nil {
		return err
	}
	if env.Subject != w.receipt || env.Data.DueAt != due || env.Data.SLASeconds != int64(adviceSLA/time.Second) {
		return fmt.Errorf("overdue = %+v subject %q, want receipt %s due %s", env.Data, env.Subject, w.receipt, due)
	}
	return nil
}

func (w *world) registerAdvice(sc *godog.ScenarioContext) {
	sc.Step(`^the receiving advice is for the ASN "([^"]*)" from supplier "([^"]*)" and is (inside|outside) the SLA$`, w.theAdviceIsFor)
	sc.Step(`^the advice line (\d+) received (\d+), accepted (\d+), rejected (\d+) and held (\d+)$`, w.theAdviceLineQuantities)
	sc.Step(`^the advice line (\d+) has the discrepancy "([^"]*)" of (\d+) coded "([^"]*)" and "([^"]*)"$`, w.theAdviceLineHasDiscrepancy)
	sc.Step(`^the advice line (\d+) has no discrepancy$`, w.theAdviceLineHasNoDiscrepancy)
	sc.Step(`^the operator holds (\d+) units of line (\d+)$`, func(ctx context.Context, qty int64, line int) error {
		return w.theOperatorHolds(ctx, qty, line)
	})
	sc.Step(`^the hold is refused$`, w.theHoldIsRefused)
	sc.Step(`^the housekeeping sweep runs$`, w.theHousekeepingSweepRuns)
	sc.Step(`^the outbox holds (\d+) ReceivingAdviceIssued events?$`, func(n int) error { return w.theOutboxHoldsOfType(n, adviceIssuedType) })
	sc.Step(`^the outbox holds (\d+) ReceivingAdviceOverdue events?$`, func(n int) error { return w.theOutboxHoldsOfType(n, adviceOverdueType) })
	sc.Step(`^the overdue alarm says the advice was due at "([^"]*)"$`, w.theOverdueAlarmSaysDueAt)
}
