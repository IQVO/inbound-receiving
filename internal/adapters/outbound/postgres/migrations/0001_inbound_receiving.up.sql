-- 0001_inbound_receiving.up.sql: inbound-receiving's OLTP schema (ADR 0002,
-- ADR 0003).

-- asns: one row per Asn aggregate. The ASN number uses the "C" collation so
-- ORDER BY / asn_number > $cursor follow byte order, the same order the
-- in-memory adapter and the opaque list cursor assume. version starts at 1
-- and guards every UPDATE (optimistic concurrency).
CREATE TABLE asns (
    asn_number       TEXT COLLATE "C" PRIMARY KEY,
    supplier_ref     TEXT        NOT NULL,
    expected_arrival TIMESTAMPTZ,
    state            TEXT        NOT NULL CHECK (state IN ('Registered', 'Receiving', 'Closed', 'Cancelled')),
    version          BIGINT      NOT NULL CHECK (version >= 1),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_asns_state ON asns (state, asn_number);

-- asn_lines: the immutable lines of an ASN, numbered exactly 1..n; SKUs are
-- unique per ASN.
CREATE TABLE asn_lines (
    asn_number   TEXT COLLATE "C" NOT NULL REFERENCES asns (asn_number) ON DELETE CASCADE,
    line_no      INTEGER NOT NULL CHECK (line_no >= 1),
    sku          TEXT    NOT NULL,
    expected_qty BIGINT  NOT NULL CHECK (expected_qty BETWEEN 1 AND 2147483647),
    PRIMARY KEY (asn_number, line_no),
    UNIQUE (asn_number, sku)
);

-- appointments: one row per DockAppointment aggregate. The window is the
-- half-open interval [window_start, window_end).
CREATE TABLE appointments (
    id           TEXT COLLATE "C" PRIMARY KEY,
    door_code    TEXT        NOT NULL,
    carrier      TEXT        NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    window_end   TIMESTAMPTZ NOT NULL,
    state        TEXT        NOT NULL CHECK (state IN ('Booked', 'CheckedIn', 'Completed', 'Cancelled')),
    version      BIGINT      NOT NULL CHECK (version >= 1),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (window_end > window_start)
);

-- GET /appointments pages by (window_start, id).
CREATE INDEX idx_appointments_window ON appointments (window_start, id);
-- The overlap check reads the active appointments of one door.
CREATE INDEX idx_appointments_active_door ON appointments (door_code, window_start)
    WHERE state IN ('Booked', 'CheckedIn');

-- appointment_asns: the ASNs an appointment covers, in booking order.
CREATE TABLE appointment_asns (
    appointment_id TEXT COLLATE "C" NOT NULL REFERENCES appointments (id) ON DELETE CASCADE,
    position       INTEGER NOT NULL CHECK (position >= 1),
    asn_number     TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (appointment_id, position),
    UNIQUE (appointment_id, asn_number)
);

-- receipts: one row per Receipt aggregate. closed_at is set exactly when the
-- receipt is Closed.
CREATE TABLE receipts (
    id             TEXT COLLATE "C" PRIMARY KEY,
    asn_number     TEXT COLLATE "C" NOT NULL REFERENCES asns (asn_number),
    appointment_id TEXT COLLATE "C",
    door_code      TEXT,
    state          TEXT        NOT NULL CHECK (state IN ('Open', 'Closed')),
    opened_at      TIMESTAMPTZ NOT NULL,
    closed_at      TIMESTAMPTZ,
    version        BIGINT      NOT NULL CHECK (version >= 1),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((state = 'Closed') = (closed_at IS NOT NULL))
);

-- At most one OPEN receipt per ASN (ADR 0002): a rule that spans aggregates,
-- so the database enforces it.
CREATE UNIQUE INDEX receipts_one_open_per_asn ON receipts (asn_number) WHERE state = 'Open';
CREATE INDEX idx_receipts_asn ON receipts (asn_number, id);

-- receipt_lines: the snapshot of the ASN's lines plus what was received.
CREATE TABLE receipt_lines (
    receipt_id       TEXT COLLATE "C" NOT NULL REFERENCES receipts (id) ON DELETE CASCADE,
    line_no          INTEGER NOT NULL CHECK (line_no >= 1),
    sku              TEXT    NOT NULL,
    expected_qty     BIGINT  NOT NULL CHECK (expected_qty BETWEEN 1 AND 2147483647),
    received_good    BIGINT  NOT NULL DEFAULT 0 CHECK (received_good >= 0),
    received_damaged BIGINT  NOT NULL DEFAULT 0 CHECK (received_damaged >= 0),
    PRIMARY KEY (receipt_id, line_no),
    CHECK (received_good + received_damaged <= 2147483647)
);

-- known_skus: local copy of product-master's ProductRegistered (ADR 0003).
CREATE TABLE known_skus (
    sku           TEXT PRIMARY KEY,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- dock_doors: local copy of facility-layout's inbound dock doors (ADR 0003).
CREATE TABLE dock_doors (
    door_code  TEXT PRIMARY KEY,
    dock_flow  TEXT        NOT NULL CHECK (dock_flow IN ('Inbound', 'Both')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- outbox_events: the transactional outbox (mirrors product-master's and
-- warehouse-planning's, including the (event_id, topic) identity). One row
-- per already-encoded Kafka message, inserted in the SAME transaction as the
-- aggregate rows; the relay drains unpublished rows to Kafka.
--   event_id    the CloudEvents `id`, minted once at encode time and
--               persisted: a relay retry republishes the same id.
--   event_type  the FULL CloudEvents `type`
--               (com.warehouse.wms.inbound-receiving.<entity>.<EventName>).
--   subject     the CloudEvents `subject` (the aggregate id).
--   key         the Kafka message key (asn_number or appointment_id).
--   dataschema  the CloudEvents `dataschema` URN.
--   value       the structured-mode CloudEvents JSON bytes.
--   headers     [{"key":..,"value":..}] Kafka headers (content-type).
CREATE TABLE outbox_events (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    key          BYTEA,
    dataschema   TEXT        NOT NULL,
    value        BYTEA       NOT NULL,
    headers      JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT,
    CONSTRAINT outbox_events_event_id_topic_key UNIQUE (event_id, topic)
);

-- The relay only ever scans the unpublished tail; keep that scan tiny.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events (id) WHERE published_at IS NULL;

-- processed_events: idempotency guard of the inbound consumers (ADR 0003).
-- (consumer, event_id) is claimed with INSERT ... ON CONFLICT DO NOTHING in
-- the SAME transaction as the effect, so a rollback un-claims it.
CREATE TABLE processed_events (
    consumer     TEXT        NOT NULL,
    event_id     TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

-- idempotency_keys: the Idempotency-Key middleware's store (fleet rule
-- fleet/idempotency-and-outbox.md). A committed row ALWAYS has its
-- status_code set: the inserting transaction fills the outcome before it
-- commits, so a reader never sees a half-written row.
CREATE TABLE idempotency_keys (
    key              TEXT PRIMARY KEY,
    request_hash     TEXT        NOT NULL,
    status_code      INTEGER,
    response_body    BYTEA,
    response_headers JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ
);
