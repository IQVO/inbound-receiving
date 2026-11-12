-- 0004_asn_timeliness.down.sql: the inverse of 0004 (ADR 0025).
DROP TABLE asn_late_facts;
ALTER TABLE asn_facts DROP COLUMN supplier_ref;
