-- The projection is rebuildable from the topic: the two tolerance kinds a
-- rollback can no longer hold are dropped with the column that explains them.
DELETE FROM receipt_discrepancies WHERE kind IN ('OverTolerance', 'UnderTolerance');

ALTER TABLE receipt_discrepancies
    DROP CONSTRAINT receipt_discrepancies_tolerance_reason_check;

ALTER TABLE receipt_discrepancies
    DROP CONSTRAINT receipt_discrepancies_kind_check;

ALTER TABLE receipt_discrepancies
    ADD CONSTRAINT receipt_discrepancies_kind_check
        CHECK (kind IN ('Short', 'Over', 'Damaged'));

ALTER TABLE receipt_discrepancies
    DROP COLUMN reason_code;

ALTER TABLE receipt_facts
    DROP COLUMN tolerance_applied;
