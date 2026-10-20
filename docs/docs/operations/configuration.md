---
id: configuration
title: Configuration
sidebar_label: Configuration
---

# Configuration

inbound-receiving ships **four binaries**, each configured only through
environment variables. There are no config files and no flags. Every table
below was built from the binary's `main.go` (and the adapter helpers it calls);
the "Source" column names the file that reads the variable.

| Binary | Role | Default listen address |
|---|---|---|
| `cmd/api` | REST API, local-copy Kafka consumers, outbox relay | `:8080` (`HTTP_ADDR`) |
| `cmd/mcp` | Read-only MCP server (Streamable HTTP) | `:8090` (`MCP_ADDR`) |
| `cmd/inbound-projector` | Analytics writer: consumes the analytics topic, projects into the analytical database | `:8091` admin port (`ADMIN_ADDR`) |
| `cmd/inbound-reports` | Read-only analytics reports over HTTP | `:8092` (`HTTP_ADDR`) |

Misconfiguration is refused at boot: each binary validates its environment
before opening any connection and exits non-zero with a message naming the
variable (for example `PRODUCT_CONSUMER_GROUP is required: the product consumer
always runs on real Kafka`).

**Postgres and Kafka are the only runtime** ([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)):
there is no in-memory mode, no log sink, no optional relay and no consumer that
can be switched off. The `*_MODE` and `EVENT_PUBLISHER` names are kept because
the chart and warehouse-infra set them, but their only legal value is `kafka`.

## `cmd/api`

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST server (`/healthz`, `/readyz`, `/metrics` and every route of `apis/openapi.yaml`). | `cmd/api/main.go` |
| `DATABASE_URL` | unset | **yes** | Postgres DSN of the OLTP database. Missing = boot error (`DATABASE_URL is required`); there is no in-memory mode. The embedded migrations run first, then a pgx pool is opened and pinged. | `cmd/api/main.go` |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | DSN used **only** for the boot migrations. Point it at Postgres directly when `DATABASE_URL` goes through PgBouncer: golang-migrate's session-scoped advisory lock does not survive transaction pooling. | `cmd/api/main.go` |
| `EVENT_PUBLISHER` | `kafka` | no | Kept because the chart and warehouse-infra set it. Unset or `kafka` publishes the outbox to Kafka; `log` is a boot error (`EVENT_PUBLISHER=log is not supported`) and any other value is too. The relay is always on. | `cmd/api/main.go` |
| `KAFKA_BROKERS` | unset | **yes** | Comma-separated broker list used by the relay sink, by the two local-copy consumers and by the `/readyz` Kafka check. Missing = boot error (`KAFKA_BROKERS is required`). Locally the fleet broker is `localhost:9092` (`docker compose up -d --wait`). | `cmd/api/main.go` |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Relay poll interval. Accepts a Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Malformed or non-positive values log a warning and fall back to `1s`. | `cmd/api/main.go`, `internal/adapters/outbound/outbox/relay.go` |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Relay attempt cap ([ADR 0007](/docs/adr/0007-outbox-relay-attempt-cap)): a row that failed this many times is no longer claimed and is logged once at ERROR; it stays in `outbox_events` until re-armed (`UPDATE outbox_events SET attempts = 0 WHERE id = ...`, see the runbook). `0` = unlimited. Negative or unparsable values log a warning and use `20`. Chart: `config.outboxMaxAttempts`. | `cmd/api/main.go`, `internal/adapters/outbound/outbox/relay.go` |
| `HOUSEKEEPING_INTERVAL` | `1h` | no | Housekeeping sweeper interval ([ADR 0010](/docs/adr/0010-housekeeping-sweeper)); it sweeps once at start, then every interval. `0` disables the sweeper. Go duration or plain seconds; negative or malformed values log a warning and use the default. Chart: `config.housekeepingInterval` (via the ConfigMap). | `cmd/api/main.go`, `internal/adapters/outbound/postgres/sweeper.go` |
| `IDEMPOTENCY_KEY_TTL` | `24h` | no | `idempotency_keys` rows older than this are deleted: a retry after the TTL runs the handler again. `0` keeps keys forever. Chart: `config.idempotencyKeyTTL`. | `internal/adapters/outbound/postgres/sweeper.go` |
| `OUTBOX_RETENTION` | `168h` | no | PUBLISHED `outbox_events` rows older than this are deleted. Unpublished rows (including rows excluded at the attempt cap) are never swept. `0` keeps them forever. Chart: `config.outboxRetention`. | `internal/adapters/outbound/postgres/sweeper.go` |
| `PROCESSED_EVENTS_RETENTION` | `720h` | no | `processed_events` dedupe marks older than this are deleted. Keep it above the consumed topics' retention (Kafka default 7 days), or an older redelivery is applied again. `0` keeps marks forever. Chart: `config.processedEventsRetention`. | `internal/adapters/outbound/postgres/sweeper.go` |
| `PRODUCT_MODE` | `kafka` | no | The only mode is `kafka` (the default): the product consumer always runs and `POST /asns` rejects SKUs that are not in the local copy (ADR 0003). `permissive` and `off` are boot errors (`PRODUCT_MODE=permissive is not supported: this context runs on real Kafka`); any other value is a boot error too. | `cmd/api/main.go` |
| `PRODUCT_CONSUMER_GROUP` | unset | **yes** | Stable consumer group of the product consumer. Missing = boot error. There is no default on purpose: a random group would re-read the topic on every restart. | `cmd/api/main.go` |
| `DOCK_DOOR_MODE` | `kafka` | no | The only mode is `kafka` (the default): the dock-door consumer always runs and `POST /appointments` rejects doors that are not known inbound doors in the local copy. `permissive` and `off` are boot errors. `GET /docks` always reports `mode: kafka`. | `cmd/api/main.go` |
| `DOCK_DOOR_CONSUMER_GROUP` | unset | **yes** | Stable consumer group of the dock-door consumer. Missing = boot error. | `cmd/api/main.go` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | no | Comma-separated list of browser origins allowed by the CORS middleware (methods `GET`, `POST`; headers `Accept`, `Content-Type`, `Idempotency-Key`, `If-Match`; exposed `ETag`, `Location`). The default is the console shell's Vite dev port; add `http://localhost:5193` to call the API from the standalone `web/` remote. | `internal/adapters/inbound/http/server.go` |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Time between flipping `/readyz` to 503 on SIGTERM and closing the listener. `0` disables the wait; negative or unparsable values log a warning and use `5s`. | `cmd/api/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint (insecure) for traces and metrics. A missing collector never blocks boot; telemetry is dropped. | `cmd/api/main.go`, `internal/adapters/outbound/telemetry/telemetry.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute on every span and metric. | `cmd/api/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning` or `error` (case-insensitive; anything else = `info`). Logs are JSON on stdout. | `cmd/api/main.go` |

Validation rules enforced by `loadConfig` (all raised before anything is opened
or the listener binds; each message names the variable):

- `DATABASE_URL`, `KAFKA_BROKERS`, `PRODUCT_CONSUMER_GROUP` and
  `DOCK_DOOR_CONSUMER_GROUP` unset → boot error.
- `PRODUCT_MODE` / `DOCK_DOOR_MODE` = `permissive` or `off` → boot error
  (`<NAME>=permissive is not supported: this context runs on real Kafka`);
  an unknown value → boot error.
- `EVENT_PUBLISHER=log` → boot error; an unknown value → boot error.

`GET /readyz` is 200 only while the drain gate is open, Postgres answers a ping
and a Kafka broker answers a topic-less metadata request; a failed dependency is
a `503 dependency-unavailable` problem naming it (verdict cached 3 s, one round
bounded to 2 s). It never requires topics: the broker auto-creates the output
topics on first write. `/healthz` stays liveness only.

Kafka is dialled lazily by the consumers (inside `Run`) and the relay sink (on
its first send), so an unreachable broker never blocks boot: the pod is simply
not ready until the broker answers. Postgres is not lazy: see
[boot retry](#boot-retry) below.

Fixed (not configurable) values in `cmd/api`: HTTP read-header timeout 5 s,
graceful HTTP shutdown 10 s, worker (consumer/relay) drain 10 s, pgx pool
`MaxConns = 10` with `statement_timeout = 5s` on every connection
(`internal/adapters/outbound/postgres/pool.go`).

## `cmd/mcp`

The MCP binary is read-only: it never starts the outbox relay or the
consumers and never reads `EVENT_PUBLISHER`, `KAFKA_BROKERS` or the consumer
group variables. It reads the same Postgres as `cmd/api`, so `DATABASE_URL` is
mandatory here too.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `MCP_ADDR` | `:8090` | no | Listen address of the MCP Streamable HTTP server (mounted at `/` and `/mcp`, plus `GET /healthz`). | `cmd/mcp/main.go` |
| `DATABASE_URL` | unset | **yes** | OLTP Postgres DSN, the same database as `cmd/api`. Missing = boot error (`DATABASE_URL is required`); there is no in-memory mode. The embedded migrations run first (so the MCP server can boot before `cmd/api` on a fresh database). | `cmd/mcp/main.go` |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations only (same PgBouncer reason as `cmd/api`). | `cmd/mcp/main.go` |
| `DOCK_DOOR_MODE` | `kafka` | no | Only selects the mode the `list_docks` tool reports (always `kafka`), so it matches `cmd/api`. No consumer runs here. `permissive`, `off` and unknown values are boot errors. | `cmd/mcp/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint. | `cmd/mcp/main.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute (service name `inbound-receiving-mcp`). | `cmd/mcp/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/mcp/main.go` |

## `cmd/inbound-projector`

The projector is the only writer of the **analytical** database (ADR 0006).
It never opens the OLTP database. Since gap fix D21 it sets up OpenTelemetry
like the other binaries ([ADR 0008](/docs/adr/0008-projector-telemetry-and-environment)):
service name `inbound-receiving-projector`, Go runtime metrics and the W3C
propagator; it opens no spans of its own and serves no `/metrics`.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | unset | **yes** | Analytical Postgres DSN, **direct** (not PgBouncer): the embedded analytics migrations (`analytics/migrations`) run over it at boot, then the writer pool (`MaxConns = 5`, `statement_timeout = 10s`) is opened. Missing = boot error. | `cmd/inbound-projector/main.go` |
| `KAFKA_BROKERS` | unset | **yes** | Comma-separated brokers; blanks are trimmed and empty entries dropped. Missing = boot error. | `cmd/inbound-projector/main.go` |
| `ANALYTICS_CONSUMER_GROUP` | `inbound-receiving-analytics` | no | Consumer group on `warehouse.inbound-receiving.analytics`. The chart always sets it. | `cmd/inbound-projector/main.go` |
| `HOUSEKEEPING_INTERVAL` | `1h` | no | Interval of the analytics sweeper ([ADR 0010](/docs/adr/0010-housekeeping-sweeper)). `0` disables it. | `cmd/inbound-projector/main.go`, `internal/adapters/outbound/analyticsstore/sweeper.go` |
| `PROCESSED_EVENTS_RETENTION` | `720h` | no | `analytics_processed_events` marks with an event time older than this are deleted, except the newest one (it is the projection's freshness). `0` keeps every mark. | `internal/adapters/outbound/analyticsstore/sweeper.go` |
| `ADMIN_ADDR` | `:8091` | no | Admin listener serving only `GET /healthz` (liveness) and `GET /readyz` (200 only while the analytical database answers a ping and a Kafka broker answers a metadata request; 503 `{"status":"not_ready","failing":[...]}` otherwise). | `cmd/inbound-projector/main.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/inbound-projector/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint. A missing collector never blocks boot. | `cmd/inbound-projector/main.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute (service name `inbound-receiving-projector`). | `cmd/inbound-projector/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. | `internal/adapters/outbound/telemetry/telemetry.go` |

## `cmd/inbound-reports`

The reports binary is read-only: it never opens the OLTP database or Kafka.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | unset | **yes** | Analytical Postgres DSN, ideally a read-only role. The pool also forces `default_transaction_read_only = on`, `MaxConns = 5` and `statement_timeout = 15s`. No migrations run here. Missing = boot error. The chart feeds it from the analytics Secret key `ANALYTICS_READER_DATABASE_URL` (falls back to the projector URL). | `cmd/inbound-reports/main.go`, `internal/adapters/outbound/analyticsstore/pool.go` |
| `HTTP_ADDR` | `:8092` | no | Listen address for `GET /healthz`, `GET /reports/receiving-performance` and `GET /reports/freshness`. | `cmd/inbound-reports/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint. | `cmd/inbound-reports/main.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute (service name `inbound-receiving-reports`). | `cmd/inbound-reports/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. The chart's reports Deployment sets it from `environment` (gap fix D21). | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/inbound-reports/main.go` |

## Boot retry

Every first Postgres dial is wrapped in `internal/bootretry.Retry`: the
migrations (`cmd/api`, `cmd/mcp`, `cmd/inbound-projector`) and the first pool
ping (all four binaries). It exists because the first outbound TCP dial of an
Istio-injected pod is reset shortly after start.

The budget is **5 attempts** with exponential backoff starting at 1 s. The
helper sleeps only *between* attempts, so the total wait is
1 + 2 + 4 + 8 = **15 s** before the last error is returned and the process
exits non-zero (the package comment in `internal/bootretry/bootretry.go` and
the chart's `values.yaml` say the same). None of this is configurable.

## Helm chart mapping

The chart `charts/inbound-receiving` renders these variables per Deployment:

| Deployment template | Variables set |
|---|---|
| `deployment.yaml` (`cmd/api`; the chart refuses to render without `database.url`/`database.existingSecret`, `kafka.brokers`, both consumer groups, and with any mode or publisher other than `kafka`) | `HTTP_ADDR`, `LOG_LEVEL`, `EVENT_PUBLISHER`, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, the housekeeping knobs (through the ConfigMap only), `PRODUCT_MODE`, `PRODUCT_CONSUMER_GROUP`, `DOCK_DOOR_MODE`, `DOCK_DOOR_CONSUMER_GROUP`, `SHUTDOWN_DRAIN_DELAY`, `CORS_ALLOWED_ORIGINS`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`, `KAFKA_BROKERS` |
| `mcp-deployment.yaml` | `MCP_ADDR`, `LOG_LEVEL`, `DOCK_DOOR_MODE`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `DATABASE_URL`, `MIGRATIONS_DATABASE_URL` |
| `projector-deployment.yaml` | `ADMIN_ADDR`, `KAFKA_BROKERS`, `ANALYTICS_CONSUMER_GROUP`, `LOG_LEVEL`, `HOUSEKEEPING_INTERVAL`, `PROCESSED_EVENTS_RETENTION`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `ANALYTICS_DATABASE_URL` |
| `reports-deployment.yaml` | `HTTP_ADDR`, `LOG_LEVEL`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `ANALYTICS_DATABASE_URL` |

The chart ConfigMap (`templates/configmap.yaml`, named `<release>`) holds the
non-secret `cmd/api` variables (`HTTP_ADDR`, `LOG_LEVEL`, `EVENT_PUBLISHER`,
the modes and groups, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, the
housekeeping knobs `HOUSEKEEPING_INTERVAL`, `IDEMPOTENCY_KEY_TTL`,
`OUTBOX_RETENTION`, `PROCESSED_EVENTS_RETENTION`,
`SHUTDOWN_DRAIN_DELAY`, `CORS_ALLOWED_ORIGINS`, `ENVIRONMENT`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, `KAFKA_BROKERS`). The api Deployment mounts it
with `envFrom` (gap fix D14), so every `config.*` value really reaches the
process; its explicit `env` entries carry the same values and win for the same
key. Only the api Deployment mounts it: the MCP, projector and reports
Deployments read their own explicit env (the ConfigMap's `HTTP_ADDR` would
otherwise move the reports listener). `charts/inbound-receiving/tests/render_test.sh`
(CI job `chart-tests`) asserts the mount.

See the [runbook](./runbook.md) for deployment, probes and routine
procedures.
