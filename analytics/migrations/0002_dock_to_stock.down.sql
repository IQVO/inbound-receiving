-- 0002_dock_to_stock.down.sql: the fact is derived and is rebuilt by replaying
-- the analytics topic under a new consumer group.
DROP INDEX IF EXISTS idx_dock_to_stock_facts_stowed_at;
DROP TABLE IF EXISTS dock_to_stock_facts;
