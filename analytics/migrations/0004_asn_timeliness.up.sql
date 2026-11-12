-- 0004_asn_timeliness.up.sql: the per-supplier ASN-timeliness KPI (ADR 0025).
-- Additive.

-- The registered side of the ratio folds from ASNRegistered (and from the
-- late fact's payload, which carries the same two values, so the join holds
-- whichever event the projector sees first). NULL on an asn_facts row that
-- no such event has touched yet (a rebuild reading only receipt events).
ALTER TABLE asn_facts ADD COLUMN supplier_ref TEXT;

-- asn_late_facts: one row per ASN, folded from AsnLateForArrival. The report
-- counts a fact in the UTC day of the ASN's registered_at, so both sides of
-- the share land in the same day.
CREATE TABLE asn_late_facts (
    asn_number        TEXT COLLATE "C" PRIMARY KEY,
    supplier_ref      TEXT        NOT NULL,
    appointment_id    TEXT        NOT NULL,
    visit_id          TEXT        NOT NULL,
    asn_registered_at TIMESTAMPTZ NOT NULL,
    arrived_at        TIMESTAMPTZ NOT NULL,
    lateness_seconds  BIGINT      NOT NULL CHECK (lateness_seconds >= 1),
    raised_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_asn_late_facts_registered_at ON asn_late_facts (asn_registered_at);
