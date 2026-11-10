package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// completeStow evaluates the dock-to-stock fact of each receipt and, for the
// ones that just became complete, raises ReceiptStowCompleted through the
// outbox (ADR 0021). It runs inside the caller's unit of work, after the
// caller wrote its own fact and took the receipt rows' lock, so two
// evaluations of one receipt serialise and the marker makes the event
// exactly-once. A Writer without a StowStore evaluates nothing.
func (w Writer) completeStow(ctx context.Context, receipts ...*receipt.Receipt) error {
	if w.Stows == nil {
		return nil
	}
	for _, r := range receipts {
		if err := w.completeOne(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) completeOne(ctx context.Context, r *receipt.Receipt) error {
	ev, err := w.Stows.Evidence(ctx, r)
	if err != nil {
		return fmt.Errorf("stow evidence of %s: %w", r.ID(), err)
	}
	event, ok := r.StowCompleted(ev.ArrivedAt, ev.Stows, w.now())
	if !ok {
		return nil
	}
	first, err := w.Stows.Complete(ctx, r.ID(), event.StowedAt)
	if err != nil {
		return fmt.Errorf("mark stow completed of %s: %w", r.ID(), err)
	}
	if !first {
		return nil
	}
	msgs, err := w.Encoder.EncodeReceipt(event)
	return w.enqueue(ctx, msgs, err)
}
