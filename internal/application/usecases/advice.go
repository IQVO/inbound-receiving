package usecases

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// DefaultAdviceSLA is the committed window of the receiving advice: it is due
// at most this long after the last unit was received (ADR 0023,
// RECEIVING_ADVICE_SLA).
const DefaultAdviceSLA = 24 * time.Hour

// defaultOverdueBatch bounds how many receipts one overdue pass takes, so a
// backlog is raised in short transactions.
const defaultOverdueBatch = 100

func (w Writer) adviceSLA() time.Duration {
	if w.AdviceSLA > 0 {
		return w.AdviceSLA
	}
	return DefaultAdviceSLA
}

// issueAdvice raises the receiving advice of the closed receipt r through the
// outbox, once: the marker is inserted in the caller's unit of work and only
// the inserter raises the event (ADR 0023). supplierRef comes from the ASN. A
// Writer without an AdviceStore issues nothing. It reports whether this call
// issued the advice and whether it was within the SLA.
func (w Writer) issueAdvice(ctx context.Context, r *receipt.Receipt, supplierRef string, at time.Time) (issued, withinSLA bool, err error) {
	if w.Advice == nil {
		return false, false, nil
	}
	event, err := r.IssueAdvice(supplierRef, w.adviceSLA(), at)
	if err != nil {
		return false, false, fmt.Errorf("receiving advice of %s: %w", r.ID(), err)
	}
	first, err := w.Advice.MarkIssued(ctx, r.ID(), at, event.WithinSLA)
	if err != nil {
		return false, false, fmt.Errorf("mark advice issued of %s: %w", r.ID(), err)
	}
	if !first {
		return false, false, nil
	}
	msgs, err := w.Encoder.EncodeReceipt(event)
	if err := w.enqueue(ctx, msgs, err); err != nil {
		return false, false, err
	}
	return true, event.WithinSLA, nil
}

// HoldLineCommand is the input of HoldReceiptLine. ExpectedVersion 0 means
// "not sent".
type HoldLineCommand struct {
	ReceiptID       string
	LineNo          int
	Quantity        int64
	ExpectedVersion int64
}

// HoldReceiptLine declares Quantity Good units of a line held (ADR 0023). It is
// an explicit command, never raised automatically. It is not naturally
// idempotent (a repeat holds more units, up to the Good units received), so a
// caller that retries sends ExpectedVersion. It raises no event: the hold
// appears on the receiving advice at close.
type HoldReceiptLine struct {
	Writer
}

// Handle runs the use case and returns the receipt after the change.
func (uc *HoldReceiptLine) Handle(ctx context.Context, cmd HoldLineCommand) (*receipt.Receipt, error) {
	id, err := receipt.NewID(cmd.ReceiptID)
	if err != nil {
		return nil, err
	}
	var out *receipt.Receipt
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.Receipts.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := r.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		if err := r.HoldLine(cmd.LineNo, cmd.Quantity); err != nil {
			return err
		}
		if err := uc.saveReceipt(ctx, r, loaded, nil); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SweepAdviceOverdue is the overdue half of the housekeeping tick (ADR 0023): it
// finds the OPEN receipts whose last unit was received more than the SLA ago and
// raises ReceivingAdviceOverdue once for each (the marker), with a counter and a
// WARN log. The housekeeping sweeper calls Handle on its interval.
type SweepAdviceOverdue struct {
	Writer
	// Batch bounds the receipts one pass takes (default 100).
	Batch   int
	Logger  *slog.Logger
	metrics ports.AdviceMetrics
}

// WithMetrics attaches the advice metrics port (optional).
func (uc *SweepAdviceOverdue) WithMetrics(m ports.AdviceMetrics) *SweepAdviceOverdue {
	uc.metrics = m
	return uc
}

// Handle runs passes until one raises fewer receipts than the batch and returns
// how many receipts were raised overdue in total. A Writer without an
// AdviceStore does nothing.
func (uc *SweepAdviceOverdue) Handle(ctx context.Context) (int, error) {
	if uc.Advice == nil {
		return 0, nil
	}
	batch := uc.Batch
	if batch <= 0 {
		batch = defaultOverdueBatch
	}
	total := 0
	for {
		raised, err := uc.pass(ctx, batch)
		total += len(raised)
		if err != nil {
			return total, err
		}
		for _, ev := range raised {
			uc.logger().WarnContext(ctx, "receiving advice overdue: the receipt is still open past the advice SLA",
				"receipt_id", ev.ReceiptID(), "asn_number", ev.AsnNumber(), "last_carton_at", ev.LastCartonAt,
				"due_at", ev.DueAt, "sla", ev.SLA.String())
			if uc.metrics != nil {
				uc.metrics.AdviceOverdue(ctx)
			}
		}
		if len(raised) < batch {
			return total, nil
		}
	}
}

// pass raises one batch inside one unit of work.
func (uc *SweepAdviceOverdue) pass(ctx context.Context, batch int) ([]receipt.ReceivingAdviceOverdue, error) {
	var raised []receipt.ReceivingAdviceOverdue
	err := uc.UoW.Do(ctx, func(ctx context.Context) error {
		raised = nil
		now, sla := uc.now(), uc.adviceSLA()
		candidates, err := uc.Advice.OverdueCandidates(ctx, now.Add(-sla), batch)
		if err != nil {
			return fmt.Errorf("overdue candidates: %w", err)
		}
		for _, r := range candidates {
			ev, overdue := r.AdviceOverdue(sla, now)
			if !overdue {
				continue
			}
			first, err := uc.Advice.MarkOverdue(ctx, r.ID(), now)
			if err != nil {
				return fmt.Errorf("mark advice overdue of %s: %w", r.ID(), err)
			}
			if !first {
				continue
			}
			msgs, err := uc.Encoder.EncodeReceipt(ev)
			if err := uc.enqueue(ctx, msgs, err); err != nil {
				return err
			}
			raised = append(raised, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return raised, nil
}

func (uc *SweepAdviceOverdue) logger() *slog.Logger {
	if uc.Logger != nil {
		return uc.Logger
	}
	return slog.Default()
}
