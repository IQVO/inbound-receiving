-- 0009_asn_timeliness_and_flat_asns.up.sql: ASN timeliness and flat S-O-I ASN
-- receiving (ADR 0025). Additive only; no backfill: every existing row stays
-- valid and reads right.

-- registered_at: when the ASN was registered here. NULL on a row written
-- before this migration (read as unknown; an unknown registration time can
-- never be late, so no old ASN raises AsnLateForArrival).
ALTER TABLE asns ADD COLUMN registered_at TIMESTAMPTZ;

-- asn_late_arrivals: the marker that makes AsnLateForArrival exactly once per
-- ASN. A row is inserted (ON CONFLICT DO NOTHING) in the unit of work that
-- writes the event to the outbox; only the inserter raises the event (the
-- same shape as receipt_advices and receipt_stow_completions).
CREATE TABLE asn_late_arrivals (
    asn_number     TEXT COLLATE "C" PRIMARY KEY,
    appointment_id TEXT        NOT NULL,
    visit_id       TEXT        NOT NULL,
    raised_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The flat-ASN scan kind of ADR 0025. The inline check of 0008 has the
-- generated name below.
ALTER TABLE receipt_carton_discrepancies DROP CONSTRAINT receipt_carton_discrepancies_kind_check;
ALTER TABLE receipt_carton_discrepancies ADD CONSTRAINT receipt_carton_discrepancies_kind_check CHECK (kind IN (
    'UnexpectedCarton', 'MissingCarton', 'DuplicateSscc', 'UnknownGtin', 'CaseQtyMismatch',
    'MixedCaseDeclaredAsCasePack', 'UnlabelledCarton', 'CartonOverLimit', 'MixedCartonContentMismatch',
    'LabelNonCompliant', 'WrongDestination', 'MissingContentGtin'));
