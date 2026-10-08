-- inbound-receiving analytics read model (ADR 0006).
--
-- This is the ANALYTICAL database (inbound_receiving_analytics), separate from
-- the OLTP database. It is written only by cmd/inbound-projector and read
-- (read-only) by cmd/inbound-reports. Everything here is a projection derived
-- from warehouse.inbound-receiving.analytics, not a source of truth: it can be
-- dropped and rebuilt by replaying the topic under a new consumer group.
--
-- Facts, not aggregates: every per-day number of the report is counted from
-- these rows over the requested window. Each milestone column is the
-- CloudEvents time of the EARLIEST such event seen (kept with LEAST, which
-- ignores NULLs), so arrival order never matters.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect. occurred_at is the event's CloudEvents
-- `time`; max(occurred_at) is the freshness of the projection.
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);

-- asn_facts: one row per ASN the projection has seen (any ASN or receipt event).
--   expected_arrival   from ASNRegistered (NULL when the ASN had none)
--   registered_at      ASNRegistered
--   cancelled_at       ASNCancelled
--   receipt_opened_at  the earliest ReceiptOpened of any receipt of the ASN
CREATE TABLE asn_facts (
    asn_number        TEXT COLLATE "C" PRIMARY KEY,
    expected_arrival  TIMESTAMPTZ,
    registered_at     TIMESTAMPTZ,
    cancelled_at      TIMESTAMPTZ,
    receipt_opened_at TIMESTAMPTZ
);

CREATE INDEX idx_asn_facts_expected_arrival ON asn_facts (expected_arrival) WHERE expected_arrival IS NOT NULL;

-- appointment_facts: one row per dock appointment, one column per milestone.
CREATE TABLE appointment_facts (
    appointment_id TEXT COLLATE "C" PRIMARY KEY,
    booked_at      TIMESTAMPTZ,
    checked_in_at  TIMESTAMPTZ,
    cancelled_at   TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ
);

CREATE INDEX idx_appointment_facts_booked_at    ON appointment_facts (booked_at)    WHERE booked_at IS NOT NULL;
CREATE INDEX idx_appointment_facts_checked_in   ON appointment_facts (checked_in_at) WHERE checked_in_at IS NOT NULL;
CREATE INDEX idx_appointment_facts_cancelled_at ON appointment_facts (cancelled_at) WHERE cancelled_at IS NOT NULL;
CREATE INDEX idx_appointment_facts_completed_at ON appointment_facts (completed_at) WHERE completed_at IS NOT NULL;

-- receipt_facts: one row per receipt. A receipt is OPEN while opened_at is set
-- and closed_at is NULL.
CREATE TABLE receipt_facts (
    receipt_id TEXT COLLATE "C" PRIMARY KEY,
    asn_number TEXT COLLATE "C" NOT NULL,
    opened_at  TIMESTAMPTZ,
    closed_at  TIMESTAMPTZ
);

CREATE INDEX idx_receipt_facts_closed_at ON receipt_facts (closed_at) WHERE closed_at IS NOT NULL;
CREATE INDEX idx_receipt_facts_open      ON receipt_facts (receipt_id) WHERE opened_at IS NOT NULL AND closed_at IS NULL;

-- receipt_line_events: one row per ReceiptLineReceived (keyed by its
-- CloudEvents id), i.e. one receiving action against a line.
CREATE TABLE receipt_line_events (
    event_id    TEXT        PRIMARY KEY,
    receipt_id  TEXT COLLATE "C" NOT NULL,
    line_no     INTEGER     NOT NULL CHECK (line_no >= 1),
    sku         TEXT        NOT NULL,
    quantity    BIGINT      NOT NULL CHECK (quantity >= 1),
    condition   TEXT        NOT NULL CHECK (condition IN ('Good', 'Damaged')),
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_receipt_line_events_occurred_at ON receipt_line_events (occurred_at);

-- receipt_discrepancies: the discrepancies a ReceiptClosed carried, one row
-- per (receipt, line, kind), stamped with the close time.
CREATE TABLE receipt_discrepancies (
    receipt_id TEXT COLLATE "C" NOT NULL,
    line_no    INTEGER     NOT NULL CHECK (line_no >= 1),
    kind       TEXT        NOT NULL CHECK (kind IN ('Short', 'Over', 'Damaged')),
    sku        TEXT        NOT NULL,
    closed_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (receipt_id, line_no, kind)
);

CREATE INDEX idx_receipt_discrepancies_closed_at ON receipt_discrepancies (closed_at);
