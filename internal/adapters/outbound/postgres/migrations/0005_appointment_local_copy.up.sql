-- 0005_appointment_local_copy.up.sql: stage C of the DockAppointment ownership
-- transfer to dock-scheduling (ADR 0019).
--
-- `appointments` stops being an aggregate table (this context no longer books,
-- checks in, cancels or completes appointments) and becomes the LOCAL COPY of
-- dock-scheduling's appointments, fed by the dock-appointment consumer.
--
-- `version` was the legacy aggregate's optimistic-lock counter (>= 1). It now
-- holds the dock-scheduling aggregate version of the last event applied, and a
-- consumer applies an event only when its version is greater. Every row that
-- exists today predates the transfer, so it resets to 0 and the first
-- dock-scheduling version (1 or more) always wins. The legacy counter is not
-- needed any more: the stage B backfill replays history from `state`,
-- `created_at`, `updated_at` and the receipts.
ALTER TABLE appointments DROP CONSTRAINT IF EXISTS appointments_version_check;
UPDATE appointments SET version = 0;
ALTER TABLE appointments ALTER COLUMN version SET DEFAULT 0;
ALTER TABLE appointments ADD CONSTRAINT appointments_version_check CHECK (version >= 0);

-- Nothing reads "the active appointments of one door" any more (the overlap
-- rule and its per-door lock moved to dock-scheduling).
DROP INDEX IF EXISTS idx_appointments_active_door;
