-- 0009_asn_timeliness_and_flat_asns.down.sql: the inverse of 0009 (ADR 0025).
ALTER TABLE receipt_carton_discrepancies DROP CONSTRAINT receipt_carton_discrepancies_kind_check;
ALTER TABLE receipt_carton_discrepancies ADD CONSTRAINT receipt_carton_discrepancies_kind_check CHECK (kind IN (
    'UnexpectedCarton', 'MissingCarton', 'DuplicateSscc', 'UnknownGtin', 'CaseQtyMismatch',
    'MixedCaseDeclaredAsCasePack', 'UnlabelledCarton', 'CartonOverLimit', 'MixedCartonContentMismatch',
    'LabelNonCompliant', 'WrongDestination'));
DROP TABLE asn_late_arrivals;
ALTER TABLE asns DROP COLUMN registered_at;
