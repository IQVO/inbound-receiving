-- 0006_dock_to_stock.down.sql: drops the two local copies and the marker. The
-- copies are rebuilt by replaying the two topics under a new consumer group.
DROP INDEX IF EXISTS idx_receipts_appointment;
DROP INDEX IF EXISTS idx_receipt_cartons_lpn;
DROP TABLE IF EXISTS receipt_stow_completions;
DROP TABLE IF EXISTS carton_stows;
DROP TABLE IF EXISTS visit_arrivals;
