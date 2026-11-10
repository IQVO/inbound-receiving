-- 0006_dock_to_stock.up.sql: the dock-to-stock KPI (ADR 0021). Additive only.
--
-- visit_arrivals: LOCAL COPY of yard-management's VisitCheckedIn, one row per
-- appointment (a visit without an appointment is never stored). arrived_at is
-- the EARLIEST physical gate arrival seen for the appointment, kept with
-- LEAST, so the upsert is idempotent and order-independent.
CREATE TABLE visit_arrivals (
    appointment_id TEXT COLLATE "C" PRIMARY KEY,
    arrived_at     TIMESTAMPTZ NOT NULL,
    visit_id       TEXT        NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- carton_stows: LOCAL COPY of inventory-storage's ContainerStowed, one row per
-- LPN. stowed_at is the EARLIEST `at` seen (a later move of the container to
-- another bin is not a new stow), kept with LEAST.
CREATE TABLE carton_stows (
    lpn        TEXT COLLATE "C" PRIMARY KEY,
    stowed_at  TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- receipt_stow_completions: the marker that makes ReceiptStowCompleted
-- exactly-once. A row is inserted (ON CONFLICT DO NOTHING) in the unit of work
-- that writes the event to the outbox; only the inserter raises the event.
CREATE TABLE receipt_stow_completions (
    receipt_id   TEXT COLLATE "C" PRIMARY KEY,
    stowed_at    TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Evaluation looks receipts up by the carton they hold and by the appointment
-- they were opened from.
CREATE INDEX idx_receipt_cartons_lpn ON receipt_cartons (lpn);
CREATE INDEX idx_receipts_appointment ON receipts (appointment_id) WHERE appointment_id IS NOT NULL;
