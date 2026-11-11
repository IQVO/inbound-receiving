-- 0008_gs1_label_receiving.down.sql: reverts ADR 0024's columns. The findings of the two
-- new kinds cannot stay under the old check, so they are deleted (this is the only data loss);
-- the SSCC sequences are left in place so a re-applied migration never reissues a serial.
DELETE FROM receipt_carton_discrepancies WHERE kind IN ('LabelNonCompliant', 'WrongDestination');
ALTER TABLE receipt_carton_discrepancies DROP CONSTRAINT receipt_carton_discrepancies_kind_check;
ALTER TABLE receipt_carton_discrepancies ADD CONSTRAINT receipt_carton_discrepancies_kind_check CHECK (kind IN (
    'UnexpectedCarton', 'MissingCarton', 'DuplicateSscc', 'UnknownGtin', 'CaseQtyMismatch',
    'MixedCaseDeclaredAsCasePack', 'UnlabelledCarton', 'CartonOverLimit', 'MixedCartonContentMismatch'));
ALTER TABLE receipt_carton_discrepancies DROP COLUMN supplier_ref;
ALTER TABLE receipt_cartons DROP COLUMN label;
ALTER TABLE receipt_cartons DROP COLUMN sscc_source;
