-- ADR 0026: the receiving tolerance policy, one row per (owner, supplier).
-- An empty owner_ref is the house default. The band CHECK pins
-- 0 <= tolerance < hard_stop <= 1 per side at the storage layer too; a side
-- left at (0, 0) is undeclared and keeps today's behaviour. Strictly
-- additive, no backfill.
CREATE TABLE receiving_policies (
    owner_ref       TEXT                       NOT NULL DEFAULT '',
    supplier_ref    TEXT                       NOT NULL,
    over_tolerance  DOUBLE PRECISION           NOT NULL,
    under_tolerance DOUBLE PRECISION           NOT NULL,
    over_hard_stop  DOUBLE PRECISION           NOT NULL,
    under_hard_stop DOUBLE PRECISION           NOT NULL,
    version         BIGINT                     NOT NULL CHECK (version >= 1),
    declared_at     TIMESTAMPTZ                NOT NULL,
    CONSTRAINT receiving_policies_band CHECK (
        (0 <= over_tolerance AND over_tolerance < over_hard_stop AND over_hard_stop <= 1)
        AND (0 <= under_tolerance AND under_tolerance < under_hard_stop AND under_hard_stop <= 1)
    ),
    PRIMARY KEY (owner_ref, supplier_ref)
);
