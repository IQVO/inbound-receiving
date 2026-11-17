ALTER TABLE receipt_discrepancies
    DROP CONSTRAINT receipt_discrepancies_kind_check;

ALTER TABLE receipt_discrepancies
    ADD CONSTRAINT receipt_discrepancies_kind_check
        CHECK (kind IN ('Short', 'Over', 'Damaged'));

ALTER TABLE receipt_discrepancies
    DROP COLUMN reason_code;

ALTER TABLE receipt_facts
    DROP COLUMN tolerance_applied;
