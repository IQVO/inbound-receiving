---
id: runtime
title: Runtime and integration mechanics
sidebar_position: 4
---

# Runtime and integration mechanics

## Deployables

One container image (`Dockerfile`) builds every `cmd/*` binary into `/app`
(uid 1000; the default entrypoint is `./api`). The Helm chart
`charts/inbound-receiving` renders each one as its own component.

| Binary | Role | Port | ADR |
| --- | --- | --- | --- |
| `cmd/api` | REST API, transactional-outbox relay, the two local-copy consumers | `HTTP_ADDR` (`:8080`) | 0001 to 0004 |
| `cmd/mcp` | Read-only MCP server (Streamable HTTP at `/` and `/mcp`, `GET /healthz`) over the same read use cases and database as REST; no relay, no consumer, never dials Kafka | `MCP_ADDR` (`:8090`) | [0005](/docs/adr/0005-mcp-server-adoption) |
| `cmd/inbound-projector` | Analytics writer: consumes `warehouse.inbound-receiving.analytics` into the separate analytical database; serves only `/healthz` and `/readyz` | `ADMIN_ADDR` (`:8091`) | [0006](/docs/adr/0006-analytics-read-side) |
| `cmd/inbound-reports` | Read-only reports HTTP over the analytical database: `GET /healthz`, `GET /reports/receiving-performance`, `GET /reports/freshness` | `HTTP_ADDR` (`:8092`) | [0006](/docs/adr/0006-analytics-read-side) |

The `web/` directory is a separate npm project, not part of the Go module: see
[Web remote](#web-remote-inbound_mfe) below.

## Layers

Hexagonal architecture, enforced by the fitness tests in `internal/architecture`:

| Layer | Path |
| --- | --- |
| Domain | `internal/domain/{asn,appointment,receipt,shared}` |
| Application (use cases, ports) | `internal/application/{usecases,ports,repository,outbox,idempotency}` |
| Analytics report rules (pure) | `internal/analytics/report` (range rules, day windows, freshness lag) |
| Inbound adapters | `internal/adapters/inbound/http` (REST and the reports router), `internal/adapters/inbound/mcp` (MCP tools), `internal/adapters/inbound/kafka` (the product and dock-door consumers, and the projector's analytics consumer) |
| Outbound adapters | `internal/adapters/outbound/{postgres,memory,kafka,outbox,analyticsstore,clock,idgen,telemetry}` |
| CloudEvents helper | `internal/adapters/kafka/cloudevents` |
| Composition roots | `cmd/api`, `cmd/mcp`, `cmd/inbound-projector`, `cmd/inbound-reports` |
| Boot helper | `internal/bootretry` |

The domain imports nothing internal but itself; adapters never import each
other; only the `cmd/*` composition roots wire them.

## Writing: one unit of work per command

Every write use case (`internal/application/usecases/writer.go`) runs inside one
`ports.UnitOfWork`: load the aggregate(s), apply the aggregate command, save each
guarded by the version that was loaded, encode the raised events
(`EventEncoder`) and insert them into `outbox_events`. With Postgres the unit of
work is one transaction (`postgres/unit_of_work.go`); without `DATABASE_URL` the
in-memory adapters provide the same contract. `CloseReceipt` saves the receipt,
the ASN and the appointment in a single unit of work, so their events reach the
outbox together.

## Publishing: transactional outbox

- `cmd/api` hands the use cases `FanoutEncoder`
  (`internal/adapters/outbound/kafka/analytics_encoder.go`): every domain event
  becomes **two outbox rows**, the integration row on
  `warehouse.inbound-receiving.events` first and the analytics row on
  `warehouse.inbound-receiving.analytics` second, under **one CloudEvents `id`**
  minted once at encode time and persisted with both rows. Only `dataschema`
  differs between the two (`...:events:...` versus `...:analytics:...`). The
  integration bytes come from the unchanged `Encoder` (`encoder.go`), and a relay
  retry republishes each row's persisted bytes, so the id never changes
  (ADR 0006).
- The relay (`internal/adapters/outbound/outbox/relay.go`) drains every
  `OUTBOX_RELAY_INTERVAL` (default 1 s); a full batch (100 rows) is followed
  immediately by another pass. `OutboxRepo.Drain` claims rows with
  `FOR UPDATE SKIP LOCKED`, sends them one at a time in id order, marks each
  published, and stops at the first failure (recording `attempts` and
  `last_error`). Delivery is at-least-once.
- `EVENT_PUBLISHER=kafka` uses `RelaySink` (kafka-go writer, `RequireAll` acks,
  10 ms batch timeout, the `Hash` balancer on the message key); the default
  `log` sink only logs each message so the outbox still drains without a broker.

## Consuming: two local-copy consumers

`cmd/api` starts a consumer only for a copy whose mode is `kafka`
(`startConsumers`). Each decodes only through `internal/adapters/kafka/cloudevents`,
dispatches on the full `type`, runs the effect and the `processed_events` claim
in one unit of work and commits the offset only after that
(`FetchMessage` then `CommitMessages`, `internal/adapters/inbound/kafka/kafka.go`).
A transient failure retries the same message with backoff (200 ms doubling to
5 s) and never commits past it; a message that is not a valid CloudEvent, a
malformed payload or an invalid value is logged at WARN and committed past.

## Analytics read side

[ADR 0006](/docs/adr/0006-analytics-read-side) adds a separate analytical
Postgres, written only by `cmd/inbound-projector` and read only by
`cmd/inbound-reports`. Neither binary opens the OLTP database.

- **Projector.** Consumes `warehouse.inbound-receiving.analytics` under the
  fixed group `ANALYTICS_CONSUMER_GROUP` (default `inbound-receiving-analytics`).
  `Projection.Apply` claims the CloudEvents `id` in
  `analytics_processed_events` and folds the event into the fact tables
  (`asn_facts`, `appointment_facts`, `receipt_facts`, `receipt_line_events`,
  `receipt_discrepancies`) in one analytical transaction; the offset is
  committed only afterwards. A message that is not a CloudEvent is skipped with
  a rate-limited WARN, an unknown type is ignored, a poison message of a known
  type (or one the store rejects with SQLSTATE class 22 or 23) is dead-lettered
  at once to `warehouse.inbound-receiving.analytics.dlq`, and a transient
  failure is retried with capped backoff. The embedded migrations
  (`analytics/migrations`) and the first ping run under `internal/bootretry`;
  Kafka is dialled lazily.
- **Reports.** A read-only pool (`default_transaction_read_only=on`) behind
  `GET /reports/receiving-performance` (per UTC day over an optional RFC 3339
  `from` / `to`, default the last 30 days, at most 366 days; plus the state
  right now) and `GET /reports/freshness` (`as_of`, `lag_seconds`, both `null`
  until the first event). Errors are RFC 7807 with the slugs
  `invalid-report-range` (400) and `report-store-error` (500). No CORS and no
  `/readyz` on this router.
- **Chart.** Both are behind `analytics.enabled` (default `false`): components
  `analytics-projector` and `analytics-reports` (Service
  `<release>-reports:80`), with a Secret holding `ANALYTICS_DATABASE_URL` and
  `ANALYTICS_READER_DATABASE_URL` (the reports Deployment receives the reader
  DSN as its `ANALYTICS_DATABASE_URL`).

## Web remote (`inbound_mfe`)

`web/` is the Module Federation remote `inbound_mfe` (Vite + React). It exposes
one module, `./App` (default export, no props), which the `warehouse-console`
shell lazy-loads as `inbound_mfe/App` and mounts under `/inbound-receiving/*`.
Its production asset base is `/mfes/inbound-receiving/` (the Nginx web-gateway
path); `npm run dev` and `preview` serve it standalone on port `5193`. At
runtime it calls `${apiOrigin}/api/inbound-receiving` (Kong) from the shell's
`window.__WAREHOUSE_CONFIG__`; in dev without that config it calls
`http://localhost:8080`. Source: `web/vite.config.ts`, `web/src/config.ts`.
CI lints, type-checks, tests and builds it in the `web` job.

## Configuration

### `cmd/api`

| Variable | Meaning | Default |
| --- | --- | --- |
| `HTTP_ADDR` | listen address | `:8080` |
| `DATABASE_URL` | Postgres DSN; unset = in-memory adapters | unset |
| `MIGRATIONS_DATABASE_URL` | direct DSN for the boot migrations | `DATABASE_URL` |
| `EVENT_PUBLISHER` | `kafka` or `log` | `log` |
| `KAFKA_BROKERS` | comma-separated brokers; required by `EVENT_PUBLISHER=kafka` and by either mode below being `kafka` | unset |
| `OUTBOX_RELAY_INTERVAL` | relay poll interval (Go duration or seconds) | `1s` |
| `PRODUCT_MODE` | `permissive` or `kafka`: enforce known SKUs | `permissive` |
| `PRODUCT_CONSUMER_GROUP` | consumer group of the product consumer; **required** when `PRODUCT_MODE=kafka` | unset |
| `DOCK_DOOR_MODE` | `permissive` or `kafka`: enforce known inbound doors | `permissive` |
| `DOCK_DOOR_CONSUMER_GROUP` | consumer group of the dock-door consumer; **required** when `DOCK_DOOR_MODE=kafka` | unset |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC collector | `localhost:4317` |
| `SHUTDOWN_DRAIN_DELAY` | wait after flipping `/readyz` before closing | `5s` |
| `CORS_ALLOWED_ORIGINS` | comma-separated allowed origins | `http://localhost:5173` (the console shell's dev port; add `http://localhost:5193` to call the API from the standalone remote) |
| `LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT` | log level and telemetry resource attributes | `info`, `dev`, `local` |

A `kafka` mode with an unset group, or without brokers, is a boot error
(`loadConfig`), not a silent no-op. With `permissive` the consumer is not
started and its group id is ignored. The first start under a new group replays
the topic from the first offset; readiness does not wait for the replay, which
is why the cluster flips to `kafka` only afterwards.

### `cmd/mcp`

`MCP_ADDR` (`:8090`), `DATABASE_URL` (unset = in-memory), `MIGRATIONS_DATABASE_URL`
(default `DATABASE_URL`), `DOCK_DOOR_MODE` (only the mode `list_docks` reports,
so it matches `cmd/api`; no consumer runs here), `OTEL_EXPORTER_OTLP_ENDPOINT`,
`LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT`. Run in-memory, the MCP server
has its own empty store, separate from a local `cmd/api`.

### `cmd/inbound-projector`

| Variable | Meaning | Default |
| --- | --- | --- |
| `ANALYTICS_DATABASE_URL` | analytical Postgres DSN, direct | **required** |
| `KAFKA_BROKERS` | comma-separated brokers | **required** |
| `ANALYTICS_CONSUMER_GROUP` | fixed consumer group | `inbound-receiving-analytics` |
| `ADMIN_ADDR` | `/healthz` and `/readyz` listen address | `:8091` |
| `LOG_LEVEL` | log level | `info` |

### `cmd/inbound-reports`

`ANALYTICS_DATABASE_URL` (**required**; the chart passes the reader DSN),
`HTTP_ADDR` (`:8092`), `OTEL_EXPORTER_OTLP_ENDPOINT`, `LOG_LEVEL`,
`SERVICE_VERSION`, `ENVIRONMENT`.

## Boot and shutdown

`cmd/api`: migrations (golang-migrate, embedded in `postgres/migrations`) and
the first Postgres ping retry with backoff (`internal/bootretry`); on
exhaustion the process refuses to boot. Shutdown order: `/readyz` flips to 503,
the drain delay elapses, HTTP drains, the consumers (outbox writers) stop, the
relay stops last so nothing committed is stranded, then the pool closes.

`cmd/inbound-projector`: flips `/readyz` to 503, drains the admin server, lets
the in-flight message finish (commit or dead-letter), then closes the pool.

## Observability

OpenTelemetry traces and metrics (`internal/adapters/outbound/telemetry`,
OTLP/gRPC) and `otelchi` spans labelled by route pattern with request-duration
metrics on the `cmd/api`, `cmd/mcp` and `cmd/inbound-reports` routers (service
names `inbound-receiving`, `inbound-receiving-mcp`, `inbound-receiving-reports`).
Only `cmd/api` serves a Prometheus handler on `GET /metrics`. `GET /healthz` is
liveness on every binary; `GET /readyz` (readiness, 503 while draining) exists
on `cmd/api` and `cmd/inbound-projector` only.
