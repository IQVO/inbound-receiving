-- 0004_receipt_carton_discrepancies.up.sql: the carton discrepancies raised on
-- a receipt are part of its read model (ADR 0015, amends ADR 0014). Additive
-- only; append-only (a finding is never updated or deleted, only cascaded with
-- its receipt). Receipts raised before this migration show none.

-- position is the raise order on the receipt (1..n). raised_at is the instant
-- of the scan, or of the close for MissingCarton. sscc is NULL when the finding
-- is not about a scanned SSCC (UnknownGtin by GTIN, UnlabelledCarton by LPN,
-- MissingCarton, which names the manifest SSCC in expected).
CREATE TABLE receipt_carton_discrepancies (
    receipt_id TEXT COLLATE "C" NOT NULL REFERENCES receipts (id) ON DELETE CASCADE,
    position   INTEGER     NOT NULL CHECK (position >= 1),
    kind       TEXT        NOT NULL CHECK (kind IN (
        'UnexpectedCarton', 'MissingCarton', 'DuplicateSscc', 'UnknownGtin', 'CaseQtyMismatch',
        'MixedCaseDeclaredAsCasePack', 'UnlabelledCarton', 'CartonOverLimit', 'MixedCartonContentMismatch')),
    sscc       TEXT        CHECK (sscc ~ '^[0-9]{18}$'),
    expected   TEXT,
    actual     TEXT,
    raised_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (receipt_id, position)
);
