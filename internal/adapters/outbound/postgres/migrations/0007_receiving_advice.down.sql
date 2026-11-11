-- 0007_receiving_advice.down.sql: drops the advice markers, the SLA clock and the
-- held quantity. The advice events already published are not recalled.
DROP INDEX IF EXISTS idx_receipts_open_advice_clock;
DROP TABLE IF EXISTS receipt_advices;
ALTER TABLE receipt_lines DROP COLUMN IF EXISTS held_qty;
ALTER TABLE receipts DROP COLUMN IF EXISTS last_received_at;
