-- 0002_dock_to_stock.up.sql: the dock-to-stock fact (ADR 0021). Additive.
--
-- dock_to_stock_facts: one row per receipt, folded from ReceiptStowCompleted.
-- dock_to_stock_seconds = last Good carton stowed - yard arrival (never
-- negative). The report counts a fact in the UTC day of its stowed_at.
CREATE TABLE dock_to_stock_facts (
    receipt_id            TEXT COLLATE "C" PRIMARY KEY,
    asn_number            TEXT COLLATE "C" NOT NULL,
    arrived_at            TIMESTAMPTZ NOT NULL,
    stowed_at             TIMESTAMPTZ NOT NULL,
    dock_to_stock_seconds BIGINT      NOT NULL CHECK (dock_to_stock_seconds >= 0),
    carton_count          INTEGER     NOT NULL CHECK (carton_count >= 1)
);

CREATE INDEX idx_dock_to_stock_facts_stowed_at ON dock_to_stock_facts (stowed_at);
