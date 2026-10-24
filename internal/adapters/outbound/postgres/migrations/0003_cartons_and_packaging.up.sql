-- 0003_cartons_and_packaging.up.sql: hierarchical ASN with SSCC cartons,
-- the receipt's carton ledger and the PackagingView (ADR 0012, ADR 0013).
-- Additive only: a flat ASN stores nothing new.

-- owner_ref: the opaque tenant tag (contracts G4). NULL = fleet-owned stock.
ALTER TABLE asns ADD COLUMN owner_ref TEXT;
ALTER TABLE receipts ADD COLUMN owner_ref TEXT;

-- asn_cartons: the announced cartons of an ASN, in announced order. A pallet is
-- only an SSCC referenced by its cartons (parent_sscc). `active` turns false
-- when the ASN is cancelled, so the SSCCs of a cancelled ASN can be announced
-- again; the partial unique index keeps every other SSCC unique per site (this
-- service holds no site: one deployment is one site, contracts R6).
CREATE TABLE asn_cartons (
    asn_number     TEXT COLLATE "C" NOT NULL REFERENCES asns (asn_number) ON DELETE CASCADE,
    position       INTEGER NOT NULL CHECK (position >= 1),
    sscc           TEXT    NOT NULL CHECK (sscc ~ '^[0-9]{18}$'),
    parent_sscc    TEXT    CHECK (parent_sscc ~ '^[0-9]{18}$'),
    kind           TEXT    NOT NULL CHECK (kind IN ('HOMOGENEOUS_CASE', 'MIXED')),
    gross_weight_g BIGINT  CHECK (gross_weight_g >= 1),
    length_mm      BIGINT  CHECK (length_mm >= 1),
    width_mm       BIGINT  CHECK (width_mm >= 1),
    height_mm      BIGINT  CHECK (height_mm >= 1),
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (asn_number, position),
    UNIQUE (asn_number, sscc),
    CHECK ((length_mm IS NULL) = (width_mm IS NULL) AND (width_mm IS NULL) = (height_mm IS NULL))
);

CREATE UNIQUE INDEX asn_cartons_active_sscc ON asn_cartons (sscc) WHERE active;
CREATE INDEX idx_asn_cartons_active_parent ON asn_cartons (parent_sscc) WHERE active AND parent_sscc IS NOT NULL;

-- asn_carton_contents: what is inside a carton, in eaches, rolling up into
-- asn_lines (the per-SKU roll-up every consumer reads).
CREATE TABLE asn_carton_contents (
    asn_number TEXT COLLATE "C" NOT NULL,
    sscc       TEXT    NOT NULL,
    position   INTEGER NOT NULL CHECK (position >= 1),
    line_no    INTEGER NOT NULL CHECK (line_no >= 1),
    sku        TEXT    NOT NULL,
    gtin       TEXT    NOT NULL CHECK (gtin ~ '^[0-9]{14}$'),
    qty        BIGINT  NOT NULL CHECK (qty BETWEEN 1 AND 2147483647),
    lot        TEXT,
    expiry     TEXT,
    PRIMARY KEY (asn_number, sscc, position),
    FOREIGN KEY (asn_number, sscc) REFERENCES asn_cartons (asn_number, sscc) ON DELETE CASCADE,
    FOREIGN KEY (asn_number, line_no) REFERENCES asn_lines (asn_number, line_no)
);

-- receipt_cartons: the receipt's carton ledger. A manifest carton starts
-- 'Expected' (lpn = sscc) and becomes 'Accepted' when its SSCC is scanned; a
-- case received by case GTIN is 'Accepted' from the start with an internal LPN
-- and no sscc. contents is the JSON list of the carton's contents.
CREATE TABLE receipt_cartons (
    receipt_id  TEXT COLLATE "C" NOT NULL REFERENCES receipts (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL CHECK (position >= 1),
    lpn         TEXT    NOT NULL,
    sscc        TEXT    CHECK (sscc ~ '^[0-9]{18}$'),
    parent_sscc TEXT    CHECK (parent_sscc ~ '^[0-9]{18}$'),
    kind        TEXT    NOT NULL CHECK (kind IN ('HOMOGENEOUS_CASE', 'MIXED')),
    status      TEXT    NOT NULL CHECK (status IN ('Expected', 'Accepted')),
    sealed      BOOLEAN NOT NULL DEFAULT FALSE,
    contents    JSONB   NOT NULL,
    received_at TIMESTAMPTZ,
    PRIMARY KEY (receipt_id, position),
    UNIQUE (receipt_id, lpn),
    CHECK ((status = 'Accepted') = (received_at IS NOT NULL))
);

-- An SSCC is accepted once per site: a second acceptance is a DuplicateSscc.
CREATE UNIQUE INDEX receipt_cartons_accepted_sscc ON receipt_cartons (sscc) WHERE status = 'Accepted' AND sscc IS NOT NULL;

-- pack_levels: the PackagingView, the local projection of product-master's
-- ProductPackLevelDeclared / ProductPackLevelRetired (contracts R8), keyed by
-- GTIN (a GTIN identifies exactly one SKU and level) and looked up by
-- (sku, level). Deliberately no constraint beyond the shape the consumer has
-- already validated: a violated constraint on the consumer path would be
-- retried forever.
CREATE TABLE pack_levels (
    gtin           TEXT        PRIMARY KEY CHECK (gtin ~ '^[0-9]{14}$'),
    sku            TEXT        NOT NULL,
    level          TEXT        NOT NULL CHECK (level IN ('EACH', 'INNER', 'CASE', 'PALLET')),
    child_gtin     TEXT        CHECK (child_gtin ~ '^[0-9]{14}$'),
    qty_of_child   BIGINT      NOT NULL DEFAULT 0,
    units_per_pack BIGINT      NOT NULL CHECK (units_per_pack BETWEEN 1 AND 2147483647),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to   TIMESTAMPTZ,
    version        BIGINT      NOT NULL CHECK (version >= 1),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pack_levels_sku_level ON pack_levels (sku, level, effective_from DESC);
