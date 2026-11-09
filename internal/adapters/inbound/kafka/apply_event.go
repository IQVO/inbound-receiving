package kafka

import (
	"context"
	"errors"
	"log/slog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// eventSpec names one consumed event type for the logs. The type parameter T
// is the consumer's restated payload struct.
type eventSpec[T any] struct {
	// kind is the event name used in the skip messages, e.g.
	// "DockAppointmentBooked".
	kind string
	// processed is the message logged when the use case accepted the event.
	processed string
	// attrs are the log attributes identifying the payload.
	attrs func(T) []any
	// quietIgnored logs an Ignored outcome at DEBUG instead of INFO, for
	// types where ignoring is the normal case (ADR 0020).
	quietIgnored bool
}

// applyEvent is the shared tail of every consumer case: decode the payload
// (a malformed one is logged and skipped), hand it to handle, skip an
// ErrInvalidEvent (deterministic, logged), return any other error so the
// loop retries, and log the outcome. It returns nil for everything
// deterministic and a non-nil error ONLY for a transient failure.
func applyEvent[T any](ctx context.Context, logger *slog.Logger, id string, spec eventSpec[T], decode func(*T) error, handle func(context.Context, string, T) (usecases.Outcome, error)) error {
	var data T
	if err := decode(&data); err != nil {
		logger.WarnContext(ctx, "skipping a malformed "+spec.kind+" payload", "event_id", id, "error", err)
		return nil
	}
	attrs := append([]any{"event_id", id}, spec.attrs(data)...)
	outcome, err := handle(ctx, id, data)
	if errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an invalid "+spec.kind, append(attrs, "error", err)...)
		return nil
	}
	if err != nil {
		return err
	}
	attrs = append(attrs, "outcome", string(outcome))
	if spec.quietIgnored && outcome == usecases.Ignored {
		logger.DebugContext(ctx, spec.processed, attrs...)
		return nil
	}
	logger.InfoContext(ctx, spec.processed, attrs...)
	return nil
}
