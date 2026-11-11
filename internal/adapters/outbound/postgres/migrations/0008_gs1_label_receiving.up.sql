-- 0008_gs1_label_receiving.up.sql: GS1 logistic-label receiving (ADR 0024).
-- Additive only; no backfill: every existing row stays valid and reads right.

-- sscc_source: who gave the carton its SSCC. Only the receiver's own is stored
-- ('receiver', AssignSscc); NULL on a carton with a supplier SSCC (the supplier is
-- derived from the SSCC, so a row written before this migration reads as 'supplier')
-- and on a carton with no SSCC. 'supplier' is allowed so a writer may be explicit.
ALTER TABLE receipt_cartons ADD COLUMN sscc_source TEXT CHECK (sscc_source IN ('supplier', 'receiver'));

-- label: what the scanned GS1-128 label said besides its identifiers, as JSON
-- (best_before, expiry, production_date, packaging_date, serial, purchase_order,
-- ship_to_gln, ship_for_gln). NULL when the label said nothing or there was none.
ALTER TABLE receipt_cartons ADD COLUMN label JSONB;

-- supplier_ref: the supplier of the ASN, kept on the label findings (LabelNonCompliant,
-- WrongDestination) so a first-pass rate can be told per supplier.
ALTER TABLE receipt_carton_discrepancies ADD COLUMN supplier_ref TEXT;

-- The two new carton kinds. The inline check of 0004 has the generated name below.
ALTER TABLE receipt_carton_discrepancies DROP CONSTRAINT receipt_carton_discrepancies_kind_check;
ALTER TABLE receipt_carton_discrepancies ADD CONSTRAINT receipt_carton_discrepancies_kind_check CHECK (kind IN (
    'UnexpectedCarton', 'MissingCarton', 'DuplicateSscc', 'UnknownGtin', 'CaseQtyMismatch',
    'MixedCaseDeclaredAsCasePack', 'UnlabelledCarton', 'CartonOverLimit', 'MixedCartonContentMismatch',
    'LabelNonCompliant', 'WrongDestination'));

-- The SSCC serials are not here: one SEQUENCE per company prefix (sscc_serial_<prefix>) is
-- created by the allocator the first time the prefix is used and at boot.
