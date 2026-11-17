-- ADR 0026: the tolerance fold. receipt_facts learns which closes were
-- judged under a declared policy; receipt_discrepancies learns the two
-- tolerance kinds and the override reason code they carry. Strictly
-- additive, applied by the projector, no backfill: a close folded before
-- this migration reads as a legacy close, which it was.
ALTER TABLE receipt_facts
    ADD COLUMN tolerance_applied BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE receipt_discrepancies
    ADD COLUMN reason_code TEXT NULL;

ALTER TABLE receipt_discrepancies
    DROP CONSTRAINT receipt_discrepancies_kind_check;

ALTER TABLE receipt_discrepancies
    ADD CONSTRAINT receipt_discrepancies_kind_check
        CHECK (kind IN ('Short', 'Over', 'Damaged', 'OverTolerance', 'UnderTolerance'));
