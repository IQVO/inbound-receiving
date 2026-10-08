# ADR 0006: Analytics read side: an analytics stream, a projector and a receiving performance and accuracy report over a separate analytical database

## Status

Accepted (2026-10-08). Additive: the OLTP API, the integration topic
`warehouse.inbound-receiving.events` and its payloads are unchanged (the
integration encoder golden tests pass unchanged). No OLTP migration is needed
(see section 3). Adds a second, separate database. Closes the "Analytics topic
(later phase)" note of ADR 0004.

## Context

Every OLTP bounded context of the fleet ships an analytics read side: a
separate analytical Postgres fed by a **projector** that consumes the
context's own analytics topic, and a read-only **reports** binary over that
database (warehouse-planning ADR 0005, product-master ADR 0006, ...). ADR 0004
reserved the name `warehouse.inbound-receiving.analytics`. This ADR fills it.
It mirrors product-master ADR 0006 and records where this service differs.

The questions the data product answers are **receiving performance and
accuracy**: how many receipts closed each day, how many lines and units were
received (good versus damaged), where receiving disagreed with the ASN
(Short, Over, Damaged), how long a receipt takes from open to close, how many
dock appointments were booked, checked in, cancelled and completed, how long a
truck dwells at the dock from check-in to completion, and what is open or
expected right now. These are the operational KPIs of the dock.

## Decision

### 1. What is projected

All nine published events
(`com.warehouse.wms.inbound-receiving.asn.{ASNRegistered,ASNCancelled}`,
`...dockappointment.{DockAppointmentBooked,DockAppointmentCheckedIn,DockAppointmentCancelled,DockAppointmentCompleted}`,
`...receipt.{ReceiptOpened,ReceiptLineReceived,ReceiptClosed}`) are ALSO
written to `warehouse.inbound-receiving.analytics` as CloudEvents 1.0 with the
**same `type` and the same `id` per occurrence** as the integration message,
the same `subject` and Kafka key (ADR 0004 table), and
`dataschema=urn:warehouse:inbound-receiving:analytics:<EventName>:v1`. The DLQ
is `warehouse.inbound-receiving.analytics.dlq`.

### 2. Payloads: equal to the integration payloads

The analytics payload of every event is byte-for-byte the integration
payload. Nothing is added: the report needs the ids, the line quantities and
conditions, the discrepancy kinds, the expected arrival and the CloudEvents
`time`, all already in the integration payload. A later analytics-only field
would be additive and documented in `apis/asyncapi.yaml`.

### 3. One transaction, two topics, one id

`FanoutEncoder` (what `cmd/api` hands the use cases) turns every domain event
into **two outbox rows, the integration row first and the analytics row
second, under one CloudEvents id minted once and persisted with both**. The
use case's single `ports.UnitOfWork` inserts them with the aggregate rows, so
they commit or roll back together; a relay retry republishes each row's
persisted bytes, so the id never changes. The integration `Encoder` and its
bytes are untouched. `CloseReceipt` already writes the receipt, ASN and
appointment events in one unit of work, so they fan out together.

`outbox_events` was created with `UNIQUE (event_id, topic)` from migration
`0001`, so the shared id is legal and no OLTP migration is needed.

### 4. The analytical model

Facts, never aggregates, so the day buckets can be recomputed for any range
(`analytics/migrations/0001_receiving_performance`):

- `analytics_processed_events`: the dedupe set and the freshness source
  (`max(occurred_at)`).
- `asn_facts`, one row per ASN number: `expected_arrival`, `registered_at`,
  `cancelled_at`, `receipt_opened_at`.
- `appointment_facts`, one row per appointment: `booked_at`, `checked_in_at`,
  `cancelled_at`, `completed_at`.
- `receipt_facts`, one row per receipt: `asn_number`, `opened_at`,
  `closed_at`.
- `receipt_line_events`, one row per `ReceiptLineReceived` event (keyed by the
  CloudEvents id): `receipt_id`, `line_no`, `sku`, `quantity`, `condition`,
  `occurred_at`.
- `receipt_discrepancies`, one row per `(receipt_id, line_no, kind)` of a
  `ReceiptClosed`: `closed_at`.

Each milestone column is the CloudEvents `time` of the earliest such event
seen, kept with `LEAST`, so arrival order never matters (appointment and
receipt events are keyed differently and may be reordered, ADR 0004).

### 5. The projector (`cmd/inbound-projector`)

- Consumes the analytics topic under a **fixed consumer group read from env**
  (`ANALYTICS_CONSUMER_GROUP`, default `inbound-receiving-analytics`, set by
  the chart). At-least-once; committed offsets are honoured, a brand-new group
  starts at the earliest offset so the model can be rebuilt from retained
  history.
- **Dedupe and effect in one transaction**: `Projection.Apply` inserts the
  CloudEvents `id` into `analytics_processed_events` (`ON CONFLICT DO NOTHING`)
  and, only if it was inserted, folds the event into the fact tables in ONE
  analytical-database transaction. The offset is committed only after `Apply`
  returned.
- **Policy**:
  - not a CloudEvents 1.0 message: skipped and committed past, with a
    rate-limited WARN (the first, then at most one per minute with the number
    suppressed);
  - a valid CloudEvent of another type: acknowledged and ignored;
  - a known type with an unusable payload (an id, quantity, condition or
    discrepancy kind that cannot be projected, a missing `time`) or one the
    store deterministically rejects (Postgres SQLSTATE class 22 or 23):
    **dead-lettered at once** to `warehouse.inbound-receiving.analytics.dlq`
    (raw bytes plus `x-dlq-source-topic`, `x-dlq-error`, `x-dlq-failed-at`
    headers), then committed past; a failed DLQ write retries the message, so
    poison is never lost;
  - a **transient** failure (database down, timeout): the same message is
    retried with capped exponential backoff and never dead-lettered.
- Boot: the embedded analytical migrations (`analytics/migrations`) and the
  first ping run under `internal/bootretry`; Kafka is dialled lazily.
  `/healthz` and `/readyz` on `:8091`; graceful shutdown flips readiness,
  drains, lets the in-flight message finish, then closes the pool.
- It writes only to the analytical database (`ANALYTICS_DATABASE_URL`, a
  direct DSN) and never opens the OLTP database.

### 6. The report (`cmd/inbound-reports`, `:8092`)

Read-only HTTP over the analytical database through a read-only pool
(`default_transaction_read_only=on`); `/healthz`; RFC 7807 errors; no auth
(fleet rule); additive OpenAPI paths in `apis/openapi.yaml` (tag `reports`,
server `http://localhost:8092`).

| endpoint | question | shape |
| --- | --- | --- |
| `GET /reports/receiving-performance` | per UTC day: receipts closed, lines and units received (good versus damaged), discrepancies by kind, receipt cycle time, appointment milestones, dock dwell; plus the state right now | `from`, `to`, `days[]`: `day`, `receipts_closed`, `lines_received`, `units_good`, `units_damaged`, `discrepancies{short,over,damaged}`, `receipt_cycle{count,p50_seconds,p95_seconds}`, `appointments{booked,checked_in,cancelled,completed}`, `dock_dwell{count,p50_seconds,p95_seconds}`; `current`: `open_receipts`, `expected_today`, `expected_today_started` |
| `GET /reports/freshness` | how far the projection is behind (fleet analytics charter) | `as_of` (newest applied CloudEvents time), `lag_seconds` (now - as_of, never negative); both `null` until the first event |

Rules:

- `from` / `to` are optional RFC 3339 instants; both omitted = the 30 days
  ending now; at most 366 days; `from` inclusive, `to` exclusive; an empty or
  inverted range is a 400.
- `days[]` is dense: one row per UTC calendar day that intersects
  `[from, to)`, zeros included. A day's counts cover the part of the day
  inside the range. A fact counts in the day its milestone `time` falls in.
- `receipts_closed`, `discrepancies` and `receipt_cycle` follow the receipt's
  `ReceiptClosed` time. `receipt_cycle` and `dock_dwell` only include receipts
  and appointments whose opening and check-in the projection also saw (a
  receipt closed without a seen `ReceiptOpened` counts as closed but has no
  cycle); their p50 and p95 are linear-interpolated percentiles in seconds
  (PostgreSQL `percentile_cont`) and are `null` when `count` is 0.
- `lines_received` counts `ReceiptLineReceived` events (receiving actions on a
  line); `units_good` and `units_damaged` sum their quantities by condition.
- `current` is the state right now, independent of the range: `open_receipts`
  = opened and not closed; `expected_today` = ASNs not cancelled whose
  `expected_arrival` falls in today's UTC window; `expected_today_started` =
  those that already have a receipt opened.
- SQL only counts and takes percentiles; the range rules, day windows and the
  freshness lag live in `internal/analytics/report` (pure, imports nothing
  internal, in the coverage set). The in-memory store and the Postgres store
  run the same contract test.

### 7. Why a separate analytical database

The projection is a derived copy that can be dropped and rebuilt from the
topic; keeping it out of the OLTP database means a heavy report cannot take
OLTP connections, the two schemas evolve independently, the reports role can
be read-only on a database that holds nothing transactional, and the
projector has no credentials on OLTP data. The cost is a second role/database
to provision in `warehouse-infra` (`analytics_services` entry; an
already-populated Postgres does not re-run its init script).

### 8. Packaging

The one image builds every `cmd/*`, so `/app/inbound-projector` and
`/app/inbound-reports` ship in it (uid 1000); the analytical migrations are
embedded. The chart gains two components, `analytics-projector` (Deployment)
and `analytics-reports` (Deployment and Service `<release>-reports:80`), plus
a Secret with `ANALYTICS_DATABASE_URL` and `ANALYTICS_READER_DATABASE_URL`,
behind `analytics.enabled` (default `false`: the default render is
unchanged). The chart refuses to render analytics without a DSN source or
without Kafka. External routing to the reports Service is warehouse-infra's
job, not the chart's.

## Consequences

- Two outbox rows per event; the relay publishes both through the same sink.
  Tests that count outbox rows for one write now see two per event.
- The analytics stream is not an integration contract: only
  `inbound-projector` consumes it. Other contexts keep reading
  `warehouse.inbound-receiving.events`.
- Receipts and appointments that began before the analytics stream existed are
  only partly visible (no cycle time without the opening event). Replaying
  from the earliest retained offset is the backfill.
- A new published event type must be added to the projector's kind table, or
  it is silently absent from the report.

## Alternatives considered and rejected

- **Project from the integration topic** (a second consumer group on
  `warehouse.inbound-receiving.events`): the fleet convention is a dedicated
  analytics topic owned by the producer, and a future analytics-only field
  could not be added without touching the integration contract.
- **Two ids per occurrence**: breaks the "same type and id on both topics"
  rule that lets a consumer correlate the streams and keeps retries
  idempotent per occurrence.
- **Pre-aggregated daily counters**: cannot be re-bucketed for a partial first
  or last day, and a late or replayed event would need decrement logic. Facts
  are cheap at this volume.
- **Compute the report from the OLTP tables**: current state only, no
  per-event history (the receiving actions are not stored as events), and
  analytical queries on the OLTP database.
- **Dead-letter after N attempts for every failure**: drops analytics for
  events that failed only because the database was briefly down.
