-- 0001_inbound_receiving.down.sql: drops everything the up migration created.
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS dock_doors;
DROP TABLE IF EXISTS known_skus;
DROP TABLE IF EXISTS receipt_lines;
DROP TABLE IF EXISTS receipts;
DROP TABLE IF EXISTS appointment_asns;
DROP TABLE IF EXISTS appointments;
DROP TABLE IF EXISTS asn_lines;
DROP TABLE IF EXISTS asns;
