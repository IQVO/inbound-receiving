-- 0003_cartons_and_packaging.down.sql
DROP TABLE IF EXISTS pack_levels;
DROP TABLE IF EXISTS receipt_cartons;
DROP TABLE IF EXISTS asn_carton_contents;
DROP TABLE IF EXISTS asn_cartons;
ALTER TABLE receipts DROP COLUMN IF EXISTS owner_ref;
ALTER TABLE asns DROP COLUMN IF EXISTS owner_ref;
