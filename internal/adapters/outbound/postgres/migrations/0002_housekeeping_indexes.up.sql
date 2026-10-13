-- 0002_housekeeping_indexes.up.sql: indexes for the housekeeping sweeper
-- (gap fix D24, ADR 0010). Additive only. Each one serves the age predicate
-- of one bounded DELETE in internal/adapters/outbound/postgres/sweeper.go, so
-- a sweep pass never scans a whole append-only table.
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_created_at ON idempotency_keys (created_at);
CREATE INDEX IF NOT EXISTS idx_outbox_events_published_at ON outbox_events (published_at) WHERE published_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_processed_events_processed_at ON processed_events (processed_at);
