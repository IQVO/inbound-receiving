---
id: runbook
title: Runbook
sidebar_label: Runbook
---

# Runbook

How inbound-receiving is deployed and operated. Every fact here comes from
`charts/inbound-receiving`, the four `cmd/*/main.go` composition roots and the
adapters they wire. Configuration knobs are listed per binary on
[Configuration](./configuration.md); metrics and logs on
[Observability](./observability.md); symptom-driven fixes on
[Troubleshooting](./troubleshooting.md).

## Deployment

One container image (`Dockerfile`) carries every binary under `/app`
(`/app/api` is the default entrypoint; the other Deployments set `command`).
The Helm chart is **`charts/inbound-receiving`**. In the fleet's kind cluster
warehouse-infra installs it through Terraform/ArgoCD
(`warehouse-infra/terraform/locals.tf`, values in
`warehouse-infra/helm-values/inbound-receiving.yaml`), with the image
side-loaded by `kind load docker-image` (`pullPolicy: IfNotPresent`).

| Component label | Template | Command | Container port | Service | Enabled by |
|---|---|---|---|---|---|
| `api` | `deployment.yaml` | `/app/api` | `8080` (`http`) | `<release>` ClusterIP `:80` → `http` | always |
| `mcp` | `mcp-deployment.yaml` | `/app/mcp` | `8090` | `<release>-mcp` ClusterIP `:8090` | `mcp.enabled` (default `false`) |
| `analytics-projector` | `projector-deployment.yaml` | `/app/inbound-projector` | `8091` (`admin`) | none (admin port only) | `analytics.enabled` (default `false`) |
| `analytics-reports` | `reports-deployment.yaml` | `/app/inbound-reports` | `8092` (`http`) | `<release>-reports` ClusterIP `:80` → `8092` | `analytics.enabled` |
| `frontend` | `frontend-deployment.yaml` | nginx-unprivileged | `8080` | ClusterIP `:80` | `frontend.enabled` (default `false`) |

The chart refuses to render (template `fail`) when `database.url` and
`database.existingSecret` are both empty, when `config.eventPublisher` is not
`log`/`kafka` or is `kafka` without `kafka.enabled`, when a mode is not
`permissive`/`kafka`, when a `kafka` mode has no consumer group or no Kafka, when a
consumer group is set while its mode is `permissive`, and when
`analytics.enabled` has no Kafka or no DSN source.

In the kind cluster warehouse-infra enables everything: `eventPublisher:
kafka`, `productMode: kafka` with group `inbound-receiving-product`,
`dockDoorMode: kafka` with group `inbound-receiving-dock-doors`,
`kafka.enabled`, `mcp`, `analytics` (database `inbound_receiving_analytics`)
and the frontend remote.

### Edge

- REST: Kong exposes the api Service at `/api/inbound-receiving` (Gateway API
  `httproute.yaml`, `stripPath: true`) on `:8000`. The `ingress.yaml`
  alternative is disabled by default.
- The `httproute`/`ingress` target **only** the api Service. The MCP and
  reports Services stay ClusterIP (in-cluster callers only).
- The frontend remote is served by warehouse-infra's Nginx web gateway at
  `/mfes/inbound-receiving/`, never through Kong.
- There is **no authentication** on REST, MCP or reports (fleet rule); access
  control is the in-cluster boundary.

### Probes

| Component | startupProbe | livenessProbe | readinessProbe |
|---|---|---|---|
| `api` | `GET /healthz`, every 2 s, 30 failures (~60 s window) | `GET /healthz`, 5 s initial, every 10 s | `GET /readyz`, 3 s initial, every 5 s |
| `mcp` | `GET /healthz` | `GET /healthz` | `GET /healthz` (no `/readyz` on this binary) |
| `analytics-projector` | `GET /healthz` on `admin` | `GET /healthz` on `admin` | `GET /readyz` on `admin` |
| `analytics-reports` | `GET /healthz` | `GET /healthz` | `GET /healthz` (no `/readyz` on this binary) |

What readiness means: `/readyz` (api and projector) starts **ready** the
moment the listener is up and only flips to `503 {"status":"not_ready"}` as the
first step of graceful shutdown. It does **not** wait for Kafka, for the
consumers to catch up, or for the outbox to drain. Postgres is covered
differently: the HTTP listener is only started after migrations and the first
ping succeeded, so a pod whose database is unreachable never answers
`/healthz` at all and exits after the [boot retry](./configuration.md#boot-retry)
budget (5 attempts, 15 s of backoff).

### Graceful shutdown

`cmd/api` on SIGTERM: (1) `/readyz` → 503, (2) wait `SHUTDOWN_DRAIN_DELAY`
(5 s), (3) drain HTTP (10 s cap), (4) stop and await each consumer (10 s cap
each), (5) stop and await the outbox relay last (10 s cap), then close the
pool. The chart's `terminationGracePeriodSeconds: 40` covers the ~35 s worst
case. The projector flips `/readyz`, drains its admin server, lets the
in-flight message commit or dead-letter (10 s cap) and closes its pool. The
MCP and reports binaries simply drain HTTP (10 s cap).

## Databases and migrations

| Database | Owner binaries | Migrations | Tables |
|---|---|---|---|
| OLTP (`inbound_receiving` in kind) | `cmd/api` (read/write), `cmd/mcp` (reads) | `internal/adapters/outbound/postgres/migrations/0001_inbound_receiving.up.sql`, embedded | `asns`, `asn_lines`, `appointments`, `appointment_asns`, `receipts`, `receipt_lines`, `known_skus`, `dock_doors`, `outbox_events`, `processed_events`, `idempotency_keys` |
| Analytical (`inbound_receiving_analytics` in kind) | `cmd/inbound-projector` (only writer), `cmd/inbound-reports` (read-only) | `analytics/migrations/0001_receiving_performance.up.sql`, embedded | `analytics_processed_events`, `asn_facts`, `appointment_facts`, `receipt_facts`, `receipt_line_events`, `receipt_discrepancies` |

- **When migrations run:** at process start, before the pool opens, inside
  `bootretry`. `cmd/api` and `cmd/mcp` both run the OLTP migrations (either
  may boot first on a fresh database; golang-migrate's advisory lock makes
  concurrent starts safe). `cmd/inbound-projector` runs the analytical
  migrations. `cmd/inbound-reports` never migrates. There is no separate
  migration Job.
- **`MIGRATIONS_DATABASE_URL`:** a direct (session-mode) DSN used only for the
  migration step of `cmd/api` and `cmd/mcp`, read from the database Secret key
  `database.migrationsExistingSecretKey` (`optional: true`, falls back to
  `DATABASE_URL`). Needed whenever `DATABASE_URL` points at PgBouncer in
  transaction mode, which cannot hold the advisory lock. The projector has no
  such split: `ANALYTICS_DATABASE_URL` must itself be direct.
- A migration failure is fatal: after the retry budget the process logs
  `service exited with error` with `run migrations (after 5 attempts): ...` and
  exits 1; the pod goes into CrashLoopBackOff. golang-migrate leaves a failed
  migration marked **dirty** in `schema_migrations`; fix the cause, then reset
  the dirty flag by hand before restarting.

### Analytics tables

The analytical database is a projection of `warehouse.inbound-receiving.analytics`,
never a source of truth, and can be rebuilt by replay (see below).

| Table | One row per | Written from |
|---|---|---|
| `analytics_processed_events` | applied CloudEvents `id` (with `event_type`, `occurred_at`); `max(occurred_at)` is the freshness | every applied event, same transaction as its effect |
| `asn_facts` | ASN: `expected_arrival`, `registered_at`, `cancelled_at`, earliest `receipt_opened_at` | `ASNRegistered`, `ASNCancelled`, `ReceiptOpened` |
| `appointment_facts` | appointment: `booked_at`, `checked_in_at`, `cancelled_at`, `completed_at` | the four `DockAppointment*` events |
| `receipt_facts` | receipt: `asn_number`, `opened_at`, `closed_at` | `ReceiptOpened`, `ReceiptClosed` |
| `receipt_line_events` | `ReceiptLineReceived` (keyed by event id): line, SKU, quantity, `Good`/`Damaged` | `ReceiptLineReceived` |
| `receipt_discrepancies` | (receipt, line, kind `Short`/`Over`/`Damaged`) stamped with the close time | `ReceiptClosed` |

Every milestone column keeps the **earliest** event time seen (`LEAST`), so
arrival order does not matter.

## Kafka

One broker platform-wide (in-cluster Bitnami; `localhost:9092` from the host).
All messages are CloudEvents 1.0 in structured mode
(`application/cloudevents+json`), built and decoded only through
`internal/adapters/kafka/cloudevents`.

| Topic | Direction | Binary | Consumer group | Notes |
|---|---|---|---|---|
| `warehouse.inbound-receiving.events` | produced | `cmd/api` (outbox relay) | — | the 9 integration events; key = ASN number (ASN and receipt events) or appointment id |
| `warehouse.inbound-receiving.analytics` | produced and consumed | `cmd/api` produces; `cmd/inbound-projector` consumes | `ANALYTICS_CONSUMER_GROUP` (`inbound-receiving-analytics`) | same 9 events, same `id` and `type` as the integration row, `dataschema` `...:analytics:...` |
| `warehouse.inbound-receiving.analytics.dlq` | produced | `cmd/inbound-projector` | — | raw poison message + `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`, `x-dlq-error`, `x-dlq-failed-at` headers; auto-created on first write |
| `warehouse.product-master.events` | consumed | `cmd/api` (only when `PRODUCT_MODE=kafka`) | `PRODUCT_CONSUMER_GROUP` (`inbound-receiving-product` in kind) | `ProductRegistered` fills `known_skus` |
| `warehouse.facility.events` | consumed | `cmd/api` (only when `DOCK_DOOR_MODE=kafka`) | `DOCK_DOOR_CONSUMER_GROUP` (`inbound-receiving-dock-doors` in kind) | `LocationSlotRegistered` / `LocationSlotDecommissioned` maintain `dock_doors` |

The local-copy consumers have **no DLQ**: a message that is not a CloudEvent,
has a malformed payload or an invalid value is logged at WARN and committed
past; a transient failure retries the same message forever (200 ms doubling to
5 s) and blocks the partition. Their idempotency guard is `processed_events`
with the consumer names `product-registry` and `dock-door-registry`.

### Outbox relay

`cmd/api` runs one relay goroutine (`internal/adapters/outbound/outbox/relay.go`):

- every `OUTBOX_RELAY_INTERVAL` (default 1 s) it claims up to **100** unpublished
  rows of `outbox_events` (`ORDER BY id ... FOR UPDATE SKIP LOCKED`); a full
  batch is followed immediately by another pass;
- rows are sent one at a time in id order; each success sets `published_at`;
  the **first failure stops the pass**, increments `attempts`, stores
  `last_error` and is retried on the next pass (head-of-line by design, so a
  later event never overtakes an earlier one);
- `EVENT_PUBLISHER=log` (the chart default) logs each row as
  `event published (log sink)` and marks it published: nothing reaches Kafka;
- every domain event is **two** rows (integration first, analytics second)
  sharing one CloudEvents `id`; `(event_id, topic)` is unique.

### Housekeeping

There is **no sweeper** in this repo. Published `outbox_events` rows,
`processed_events`, `idempotency_keys` and `analytics_processed_events` are
never deleted by the code. Plan periodic manual cleanup if volume matters
(for example `DELETE FROM outbox_events WHERE published_at < now() - interval '7 days'`).

## Scaling

| Component | Scaling | Why |
|---|---|---|
| `api` | `autoscaling.api` HPA (disabled by default; min 1, max 4, 70 % CPU). When enabled the Deployment omits `replicas`. | N>1 is safe: the relay uses `FOR UPDATE SKIP LOCKED`, aggregate writes are version-guarded (`concurrent-modification` on conflict), consumers use stable groups and dedupe on the CloudEvents id. Not load-tested. |
| `mcp` | fixed `mcp.replicaCount` (1), no HPA | the Streamable HTTP handler keeps per-process session state and the Service has no session affinity |
| `analytics-projector` | `analytics.projector.replicaCount` (1) | one fixed group shares the partitions; writes dedupe on the event id |
| `analytics-reports` | `analytics.reports.replicaCount` (1) | stateless reads |

Connection pools (pgxpool, fixed in code): api and mcp `MaxConns = 10` with
`statement_timeout = 5s` each; projector `MaxConns = 5`, `10s`; reports
`MaxConns = 5`, `15s`, read-only transactions. Size Postgres `max_connections`
for `10 × (api replicas + mcp replicas) + 5 × (projector + reports replicas)`.

## Reports API (`cmd/inbound-reports`)

These routes are also described in `apis/openapi.yaml` (the generated REST
reference), but they are served by a **different binary**: `cmd/inbound-reports`
on `:8092` (Service `<release>-reports:80`), from the analytical database only.
The api binary does not serve them, and Kong does not route to them.

### `GET /reports/receiving-performance`

| Query param | Format | Default |
|---|---|---|
| `from` | RFC 3339 timestamp | `to` minus 30 days |
| `to` | RFC 3339 timestamp | now |

The range is half-open `[from, to)` in UTC, must be non-empty and at most 366
days; otherwise `400` problem `invalid-report-range`. Response `200` (shape
from `performanceReportDTO` in `internal/adapters/inbound/http/reports_handler.go`;
the numbers are illustrative):

```json
{
  "from": "2026-09-12T00:00:00Z",
  "to": "2026-10-12T00:00:00Z",
  "days": [
    {
      "day": "2026-10-11",
      "receipts_closed": 3,
      "lines_received": 12,
      "units_good": 480,
      "units_damaged": 4,
      "discrepancies": {"short": 1, "over": 0, "damaged": 1},
      "receipt_cycle": {"count": 3, "p50_seconds": 5400, "p95_seconds": 9000},
      "appointments": {"booked": 4, "checked_in": 3, "cancelled": 1, "completed": 3},
      "dock_dwell": {"count": 3, "p50_seconds": 4200, "p95_seconds": 7100}
    }
  ],
  "current": {"open_receipts": 2, "expected_today": 5, "expected_today_started": 3}
}
```

One `days` entry per UTC calendar day intersecting the range. `p50_seconds` /
`p95_seconds` are `null` when `count` is 0. `current` is computed for today's
UTC day.

### `GET /reports/freshness`

`200 {"as_of": "<RFC 3339>", "lag_seconds": <number>}`: the CloudEvents time
of the newest applied event and `now - as_of` (never negative). Both are `null`
until the projector has applied its first event.

A failed analytical query returns `500` problem `report-store-error` (the cause
is logged, never echoed).

## Routine procedures

### Switch a local copy to `kafka`

The first start under a new consumer group reads the source topic from the
beginning. Readiness does not wait for that replay, so start with the mode
`permissive` until the copy is filled, or accept that `POST /asns` /
`POST /appointments` may answer `422 unknown-sku` / `unknown-dock-door` until
the consumer has caught up.

### Re-publish outbox rows

The relay only reads `published_at IS NULL`. To re-send rows (for example after
a downstream topic was recreated), clear `published_at` on exactly the rows
needed, in id order:

```sql
UPDATE outbox_events SET published_at = NULL
WHERE topic = 'warehouse.inbound-receiving.events' AND id >= <first id>;
```

Consumers dedupe on the CloudEvents `id`, which is persisted and therefore
identical on the re-send.

### Rebuild the analytical model (replay)

The model is a pure projection of `warehouse.inbound-receiving.analytics`.
Either truncate the six analytics tables and reset the group's offsets to
earliest, or deploy the projector with a **new** `ANALYTICS_CONSUMER_GROUP`
(a new group starts at the earliest offset) against an emptied database.
Replays are bounded by the topic's retention.

### Inspect and replay the analytics DLQ

Read `warehouse.inbound-receiving.analytics.dlq` and look at the `x-dlq-error`
header. A DLQ entry is deterministic (bad payload, subject/id mismatch, or a
row the analytical store rejects with SQLSTATE class 22/23); fix the producer
or the projection, then re-produce the original value to
`warehouse.inbound-receiving.analytics`. Re-applying an id that was already
applied is a no-op.

### Rotate database credentials

The DSNs come from Secrets (`database.existingSecret` keys `DATABASE_URL` and
`MIGRATIONS_DATABASE_URL`; analytics Secret keys `ANALYTICS_DATABASE_URL` and
`ANALYTICS_READER_DATABASE_URL`). The binaries read them only at start: update
the Secret, then `kubectl rollout restart` the affected Deployments. In kind
the passwords are generated by Terraform (`random_password.service_db`).

### Reset a stuck consumer

A local-copy consumer that keeps failing transiently on one message logs
`... handling failed; retrying the same message` at ERROR on every attempt.
Fix the dependency (usually Postgres); the consumer resumes on its own. Do not
skip the offset unless the message is confirmed irrelevant; then use
`kafka-consumer-groups.sh --reset-offsets --to-offset <n+1>` for that group and
partition while `cmd/api` is scaled to zero.
