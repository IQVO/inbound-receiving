---
id: observability
title: Observability
sidebar_label: Observability
---

# Observability

What each binary emits, built from `internal/adapters/outbound/telemetry`, the
chi routers and the pinned library versions in `go.mod` (otelchi `v0.12.3`,
OTel contrib runtime `v0.72.0`, prometheus `client_golang`).

## Export pipeline

| Binary | OTel traces + metrics (OTLP/gRPC) | Prometheus `GET /metrics` | `service.name` |
|---|---|---|---|
| `cmd/api` | yes | yes | `inbound-receiving` |
| `cmd/mcp` | yes | no | `inbound-receiving-mcp` |
| `cmd/inbound-reports` | yes | no | `inbound-receiving-reports` |
| `cmd/inbound-projector` | yes since gap fix D21 ([ADR 0008](/docs/adr/0008-projector-telemetry-and-environment)): providers, propagator and Go runtime metrics; no spans of its own | no | `inbound-receiving-projector` |

- **OTLP:** `telemetry.Setup` installs a batching TracerProvider and a
  MeterProvider with a periodic reader every **30 s**, both exporting
  insecure OTLP/gRPC to `OTEL_EXPORTER_OTLP_ENDPOINT` (default
  `localhost:4317`; the chart sets
  `otel-collector.observability.svc.cluster.local:4317` when `otel.enabled`).
  The exporter connects lazily: with no collector, telemetry is dropped and the
  service runs normally (expect periodic export errors in the log).
- **Resource attributes:** `service.name`, `service.version`
  (`SERVICE_VERSION`, default `dev`; the chart sets the image tag) and
  `deployment.environment.name` (`ENVIRONMENT`, default `local`), merged with
  the SDK defaults.
- **Propagation:** W3C `traceparent` + `baggage` on inbound HTTP. Nothing is
  propagated through Kafka: outbox rows store only the content-type header, so
  a consumer-side trace is never linked to the HTTP request that produced the
  event.
- **`GET /metrics` (api only):** a private Prometheus registry with the Go
  collector (`go_*`) and the process collector (`process_*`). The HTTP and
  runtime OTel metrics below are **not** on this endpoint; they go through
  OTLP. The route is not in `apis/openapi.yaml`.

## Metric instruments

The repo defines **no custom (business) instruments**. Every metric comes from
a library:

| Instrument | Type | Unit | Attributes | Emitted by |
|---|---|---|---|---|
| `http.server.request.duration` | Float64Histogram (buckets 5 ms … 10 s) | `s` | `http.method`, `http.scheme`, `http.route` (chi pattern, e.g. `/asns/{asnNumber}`) | otelchi `NewServerRequestDuration` on the api, mcp and reports routers |
| `go.memory.used` | UpDownCounter (observable) | `By` | `go.memory.type` (`stack`, `other`) | contrib runtime (api, mcp, reports, projector) |
| `go.memory.limit` | UpDownCounter (observable) | `By` | — | contrib runtime |
| `go.memory.allocated` | Counter (observable) | `By` | — | contrib runtime |
| `go.memory.allocations` | Counter (observable) | `{allocation}` | — | contrib runtime |
| `go.memory.gc.goal` | UpDownCounter (observable) | `By` | — | contrib runtime |
| `go.goroutine.count` | UpDownCounter (observable) | `{goroutine}` | — | contrib runtime |
| `go.processor.limit` | UpDownCounter (observable) | `{thread}` | — | contrib runtime |
| `go.config.gogc` | UpDownCounter (observable) | `%` | — | contrib runtime |

`runtime.Start` is the only runtime hook wired; the contrib runtime
`NewProducer` (which would add `go.schedule.duration`) is not registered.

`http.server.request.duration` carries **no status-code attribute** in otelchi
v0.12.3, so error rates cannot be derived from it; use logs or the Kong
gateway's metrics for 4xx/5xx rates. The instrumentation scope also carries
`service.name` of the router (`inbound-receiving`, `inbound-receiving-mcp` or
`inbound-receiving-reports`).

## Traces

| Binary | Span source | Span name |
|---|---|---|
| `cmd/api` | `otelchi.Middleware("inbound-receiving", WithChiRoutes)` | the chi route pattern, e.g. `/asns`, `/asns/{asnNumber}/cancel`, `/receipts/{receiptId}/lines`, `/readyz` |
| `cmd/mcp` | `otelchi.Middleware("inbound-receiving-mcp")` | the mount pattern (`/mcp/*` or `/*`), one span per Streamable HTTP request; individual tool calls do not get their own span |
| `cmd/inbound-reports` | `otelchi.Middleware("inbound-receiving-reports")` | `/reports/receiving-performance`, `/reports/freshness`, `/healthz` |

There are no manual spans (`tracer.Start`) anywhere in `cmd/` or `internal/`,
and no database or Kafka client instrumentation: the outbox relay, the
consumers and the projector produce no spans.

## Logs

All four binaries log JSON to stdout via `log/slog` (`LOG_LEVEL`, default
`info`). Log lines are not correlated with trace ids. Fields worth indexing:

| Message | Level | Fields | Binary |
|---|---|---|---|
| `http server listening` / `mcp server listening (Streamable HTTP)` / `reports server listening` / `projector admin server listening` | INFO | `addr` | each |
| `telemetry configured` | INFO | `service_name`, `service_version`, `environment`, `otlp_endpoint` | projector |
| `DATABASE_URL not configured; using in-memory adapters` | INFO | — | api, mcp |
| `retrying` / `succeeded after retry` | WARN / INFO | `op` (`run migrations`, `ping postgres`, `run analytics migrations`, `ping analytics database`), `attempt`, `in`, `err` | all (boot retry) |
| `outbox relay running` | INFO | `publisher`, `interval`, `max_attempts`, `topics` | api |
| `outbox relay pass failed` | ERROR | `error` (contains `outbox row <id> (<type>)`) | api |
| `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | ERROR (once per row) | `outbox_id`, `topic`, `event_type`, `key`, `attempts`, `max_attempts`, `error` | api ([ADR 0007](/docs/adr/0007-outbox-relay-attempt-cap)) |
| `invalid OUTBOX_MAX_ATTEMPTS; using the default` | WARN | `value`, `default` | api |
| `event published (log sink)` | INFO | `topic`, `type`, `subject`, `id` | api with `EVENT_PUBLISHER=log` |
| `product consumer running` / `dock door consumer running` / `analytics consumer running` | INFO | `topic`, `group_id`, `brokers` | api / projector |
| `PRODUCT_MODE is permissive; product consumer not started` (and the dock-door twin) | INFO | — | api |
| `product registered processed` / `location slot event processed` | INFO | `event_id`, `sku` or `location_code`, `type`, `outcome` | api |
| `skipping a message that is not a valid CloudEvent`, `skipping a malformed ... payload`, `skipping an invalid ...` | WARN | `topic` or `event_id`, `type`, `error` | api consumers |
| `<name> handling failed; retrying the same message` / `<name> offset commit failed; ...` | ERROR | `error`, `partition`, `offset`, `attempt`, `retry_in` | api consumers, projector |
| `analytics: skipping message that is not a CloudEvents 1.0 event ...` | WARN (at most once a minute) | `topic`, `partition`, `offset`, `suppressed_since_last_warning` | projector |
| `analytics: dead-lettering a message that can never be projected` | ERROR | `error`, `dlq_topic`, `partition`, `offset` | projector |
| `idempotency store failed` | ERROR | `error`, `method`, `path` | api |
| `report query failed` | ERROR | `error`, `path` | reports |
| `shutdown: readiness flipped to not-ready; waiting for traffic to drain` | INFO | `drain_delay` | api |
| `worker did not stop before the shutdown drain deadline` | WARN | `worker` (`product-consumer`, `dock-door-consumer`, `outbox-relay`) | api |

## Health endpoints

| Binary | `GET /healthz` | `GET /readyz` |
|---|---|---|
| `cmd/api` | `200 {"status":"ok"}` | `200 {"status":"ready"}`; `503 {"status":"not_ready"}` once shutdown starts |
| `cmd/mcp` | `200 {"status":"ok"}` | — |
| `cmd/inbound-projector` (`:8091`) | `200 {"status":"ok"}` | as api |
| `cmd/inbound-reports` | `200 {"status":"ok"}` | — |

The analytics freshness endpoint `GET /reports/freshness` (reports binary)
is the projector's lag signal: `lag_seconds = now − time of the newest applied
event`.

## Dashboards

warehouse-infra has **no** Grafana dashboard for this context:
`terraform/dashboards/contexts/` on `origin/develop` holds dashboards for eleven
other contexts but no `inbound-receiving.json`. This repo has none either.

## Suggested alerts

These are suggestions built from the signals above; none exist today.

| Alert | Signal | Why |
|---|---|---|
| Outbox backlog | `SELECT count(*) FROM outbox_events WHERE published_at IS NULL` above a threshold for 5 min, or any row with `attempts > 5` | the relay stops at the first failing row; a stuck head blocks every later event until it reaches the attempt cap |
| Outbox row excluded | any `outbox relay: row reached the attempt cap ...` ERROR line, or `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND attempts >= 20` > 0 | the row will never be delivered until an operator re-arms it (runbook) |
| Relay failing | rate of `outbox relay pass failed` log lines | Kafka unreachable or rejecting writes |
| Consumer stuck | repeated `handling failed; retrying the same message` for the same `partition`/`offset` | a transient failure blocks the partition indefinitely |
| Analytics DLQ growth | any message on `warehouse.inbound-receiving.analytics.dlq` | every DLQ entry is a deterministic projection failure |
| Projection lag | `GET /reports/freshness` `lag_seconds` above e.g. 300 while receipts are being worked | projector down, lagging or blocked |
| Latency | p95 of `http.server.request.duration` by `http.route` above 1 s | each query has a 5 s `statement_timeout` on the OLTP pool |
| Pod restarts | kube restarts on any component | boot retry exhausted (Postgres unreachable) or projector consumer exited |
