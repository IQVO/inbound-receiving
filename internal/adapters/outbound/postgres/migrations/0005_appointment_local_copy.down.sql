-- 0005_appointment_local_copy.down.sql: back to the aggregate table. The
-- dock-scheduling versions cannot be mapped back to the legacy counter, so
-- every row restarts it at 1.
ALTER TABLE appointments DROP CONSTRAINT IF EXISTS appointments_version_check;
UPDATE appointments SET version = 1;
ALTER TABLE appointments ALTER COLUMN version DROP DEFAULT;
ALTER TABLE appointments ADD CONSTRAINT appointments_version_check CHECK (version >= 1);
CREATE INDEX IF NOT EXISTS idx_appointments_active_door ON appointments (door_code, window_start)
    WHERE state IN ('Booked', 'CheckedIn');
