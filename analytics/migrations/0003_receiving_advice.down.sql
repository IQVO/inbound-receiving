-- 0003_receiving_advice.down.sql: the fact is derived and is rebuilt by replaying
-- the analytics topic under a new consumer group.
DROP INDEX IF EXISTS idx_advice_facts_issued_at;
DROP TABLE IF EXISTS advice_facts;
