-- 0007_receiving_advice.up.sql: the receiving advice at close, its SLA clock and
-- the Hold command (ADR 0023). Additive only; no backfill: a receipt closed
-- before this migration gets no advice retroactively and loses nothing.

-- last_received_at: when the latest unit was received on the receipt, the start
-- of the advice SLA clock. NULL while nothing was received and on a receipt
-- written before this migration; the clock then starts at opened_at.
ALTER TABLE receipts ADD COLUMN last_received_at TIMESTAMPTZ;

-- held_qty: the Good units of the line declared held (HoldLine), a subset of
-- received_good (the aggregate enforces it, the check keeps it non-negative).
ALTER TABLE receipt_lines ADD COLUMN held_qty BIGINT NOT NULL DEFAULT 0 CHECK (held_qty >= 0);

-- receipt_advices: the two markers of the advice, one row per receipt.
-- issued_at and within_sla are set together, in the close transaction, by the
-- call that raises ReceivingAdviceIssued (exactly once). overdue_at is set by
-- the sweep that raises ReceivingAdviceOverdue (exactly once). A late close of an
-- overdue receipt fills issued_at on the row the sweep created.
CREATE TABLE receipt_advices (
    receipt_id TEXT COLLATE "C" PRIMARY KEY REFERENCES receipts (id) ON DELETE CASCADE,
    issued_at  TIMESTAMPTZ,
    within_sla BOOLEAN,
    overdue_at TIMESTAMPTZ,
    CHECK ((issued_at IS NULL) = (within_sla IS NULL)),
    CHECK (issued_at IS NOT NULL OR overdue_at IS NOT NULL)
);

-- The overdue sweep reads the Open receipts by their advice clock.
CREATE INDEX idx_receipts_open_advice_clock ON receipts ((COALESCE(last_received_at, opened_at))) WHERE state = 'Open';
