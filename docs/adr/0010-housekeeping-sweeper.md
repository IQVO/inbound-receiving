# ADR 0010: Housekeeping sweeper for the append-only tables

## Status

Accepted (2026-10-12, ecosystem gap fix D24). Mirrors the fleet sweeper
(facility-layout ADR 0026 / inventory-storage `sweeper.go`): same type, same
env names, same batching. Additive: one migration that only adds indexes.

## Context

Four tables in this service only ever grow:

- `idempotency_keys`: one row (with the cached response) per
  `Idempotency-Key` ever seen on a write route. A row is only needed while a
  client might retry the same submit.
- `outbox_events`: two rows per domain event (integration and analytics
  topics). Once `published_at` is set the row is forensics. Unpublished
  rows, including rows the relay excluded at the attempt cap
  ([ADR 0007](./0007-outbox-relay-attempt-cap.md)), are the relay's work
  queue and the operator's evidence, and must never be deleted.
- `processed_events`: one dedupe mark per product-master and
  facility-layout event the local-copy consumers applied
  ([ADR 0003](./0003-local-copies-and-handover.md)). A mark is only
  needed while Kafka can still redeliver that event.
- `analytics_processed_events` (analytical database,
  [ADR 0006](./0006-analytics-read-side.md)): one mark per analytics event
  the projector applied. `max(occurred_at)` over it is the projection's
  freshness (`GET /reports/freshness`).

Nothing deleted any of them.

## Decision

1. **`postgres.Sweeper`** (OLTP, run by `cmd/api` whenever `DATABASE_URL`
   is set) deletes, in bounded batches:
   - `idempotency_keys` with `created_at` older than `IDEMPOTENCY_KEY_TTL`
     (default **24h**);
   - `outbox_events` with `published_at IS NOT NULL` older than
     `OUTBOX_RETENTION` (default **168h**, 7 days). Unpublished rows are
     never swept, however old;
   - `processed_events` with `processed_at` older than
     `PROCESSED_EVENTS_RETENTION` (default **720h**, 30 days).
2. **`analyticsstore.Sweeper`** (run by `cmd/inbound-projector`, the only
   writer of the analytical database) deletes `analytics_processed_events`
   marks with `occurred_at` older than `PROCESSED_EVENTS_RETENTION`, but
   never the newest mark, so the freshness survives a quiet period longer
   than the retention. The fact tables are the data product and are never
   swept.
3. **Loop.** Both sweep once at start and then every
   `HOUSEKEEPING_INTERVAL` (default **1h**). A failed pass is logged
   (`housekeeping sweep failed` / `analytics housekeeping sweep failed`)
   and retried on the next tick; housekeeping never stops the service.
4. **Batches.** Each DELETE removes an explicit key set chosen by a
   subquery with `LIMIT 1000`, repeated until a batch comes back short:
   short transactions, no long lock, and several replicas can sweep at once
   (at worst one deletes zero rows). Migration 0002 adds the age indexes
   the subqueries use.
5. **Knobs.** Go durations or plain seconds. Unset = the default; an
   explicit `0` disables that part (`HOUSEKEEPING_INTERVAL=0` disables the
   sweeper); a negative or malformed value logs a WARN and uses the default,
   so a typo never silently disables housekeeping
   (`internal/housekeeping.Duration`). The chart sets them under
   `config.*`: the api reads them through the ConfigMap (`envFrom`), the
   projector through explicit env.

## Consequences

- The four tables are bounded by traffic times retention.
- A retry later than the idempotency TTL runs the handler again (and is
  re-validated against current state). The idempotency window is 24h by
  default, not infinite.
- A redelivery older than `PROCESSED_EVENTS_RETENTION` is applied again.
  Keep the retention above the consumed topics' retention (Kafka's default
  is 7 days). The local-copy upserts and the analytics fact upserts are
  idempotent, so a replay is harmless.
- A published outbox row older than 7 days is no longer available for "what
  was published when". Excluded (unpublished) rows stay until re-armed or
  deleted by an operator.
- One more background goroutine in the api and in the projector; both stop
  with the process context, before the pool closes.
- Tests: `internal/adapters/outbound/postgres/sweeper_integration_test.go`
  and `internal/adapters/outbound/analyticsstore/sweeper_integration_test.go`
  (real Postgres via testcontainers), `internal/housekeeping` (knob
  parsing), the `cmd/api` and `cmd/inbound-projector` wiring tests,
  `internal/architecture/housekeeping_sweeper_fitness_test.go` (both writer
  composition roots construct their sweeper) and the chart's
  `tests/render_test.sh`.
