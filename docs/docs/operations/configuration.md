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
variable (for example `PRODUCT_MODE=kafka requires PRODUCT_CONSUMER_GROUP`).

## `cmd/api`

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST server (`/healthz`, `/readyz`, `/metrics` and every route of `apis/openapi.yaml`). | `cmd/api/main.go` |
| `DATABASE_URL` | unset | no | Postgres DSN of the OLTP database. **Unset = in-memory adapters** (data is lost on restart; the in-memory outbox is still drained by the relay). When set, the embedded migrations run first, then a pgx pool is opened and pinged. | `cmd/api/main.go` |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | DSN used **only** for the boot migrations. Point it at Postgres directly when `DATABASE_URL` goes through PgBouncer: golang-migrate's session-scoped advisory lock does not survive transaction pooling. | `cmd/api/main.go` |
| `EVENT_PUBLISHER` | `log` | no | Outbox relay sink: `log` (empty also means `log`) writes each outbox row to the JSON log and marks it published; `kafka` publishes to Kafka. Any other value is a boot error (`unknown EVENT_PUBLISHER`). | `cmd/api/main.go` |
| `KAFKA_BROKERS` | unset | when `EVENT_PUBLISHER=kafka` or either mode below is `kafka` | Comma-separated broker list used by the relay sink and by the two local-copy consumers. Locally the fleet broker is `localhost:9092`. | `cmd/api/main.go` |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Relay poll interval. Accepts a Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Malformed or non-positive values log a warning and fall back to `1s`. | `cmd/api/main.go`, `internal/adapters/outbound/outbox/relay.go` |
| `PRODUCT_MODE` | `permissive` | no | `permissive` accepts any SKU on `POST /asns`; `kafka` starts the product consumer and rejects SKUs that are not in the local copy (ADR 0003). Any other value is a boot error. | `cmd/api/main.go`, `internal/application/usecases/errors.go` |
| `PRODUCT_CONSUMER_GROUP` | unset | when `PRODUCT_MODE=kafka` | Stable consumer group of the product consumer. There is no default on purpose: a random group would re-read the topic on every restart. | `cmd/api/main.go` |
| `DOCK_DOOR_MODE` | `permissive` | no | `permissive` accepts any door on `POST /appointments`; `kafka` starts the dock-door consumer and rejects doors that are not known inbound doors in the local copy. Also selects what `GET /docks` reports. | `cmd/api/main.go` |
| `DOCK_DOOR_CONSUMER_GROUP` | unset | when `DOCK_DOOR_MODE=kafka` | Stable consumer group of the dock-door consumer. | `cmd/api/main.go` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | no | Comma-separated list of browser origins allowed by the CORS middleware (methods `GET`, `POST`; headers `Accept`, `Content-Type`, `Idempotency-Key`, `If-Match`; exposed `ETag`, `Location`). The default is the console shell's Vite dev port. | `internal/adapters/inbound/http/server.go` |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | Time between flipping `/readyz` to 503 on SIGTERM and closing the listener. `0` disables the wait; negative or unparsable values log a warning and use `5s`. | `cmd/api/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint (insecure) for traces and metrics. A missing collector never blocks boot; telemetry is dropped. | `cmd/api/main.go`, `internal/adapters/outbound/telemetry/telemetry.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute on every span and metric. | `cmd/api/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` or `error` (case-insensitive; anything else = `info`). Logs are JSON on stdout. | `cmd/api/main.go` |

Validation rules enforced by `loadConfig` and `startOutboxRelay`:

- `PRODUCT_MODE=kafka` without `PRODUCT_CONSUMER_GROUP` → boot error.
- `DOCK_DOOR_MODE=kafka` without `DOCK_DOOR_CONSUMER_GROUP` → boot error.
- Either mode `kafka` without `KAFKA_BROKERS` → boot error.
- `EVENT_PUBLISHER=kafka` without `KAFKA_BROKERS` → boot error.

Kafka is dialled lazily (consumers inside `Run`, the relay sink on its first
send), so an unreachable broker never blocks boot. Postgres is not lazy: see
[boot retry](#boot-retry) below.

Fixed (not configurable) values in `cmd/api`: HTTP read-header timeout 5 s,
graceful HTTP shutdown 10 s, worker (consumer/relay) drain 10 s, pgx pool
`MaxConns = 10` with `statement_timeout = 5s` on every connection
(`internal/adapters/outbound/postgres/pool.go`).

## `cmd/mcp`

The MCP binary is read-only: it never starts the outbox relay or the
consumers and never reads `EVENT_PUBLISHER`, `KAFKA_BROKERS` or the consumer
group variables.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `MCP_ADDR` | `:8090` | no | Listen address of the MCP Streamable HTTP server (mounted at `/` and `/mcp`, plus `GET /healthz`). | `cmd/mcp/main.go` |
| `DATABASE_URL` | unset | no | OLTP Postgres DSN. Unset = **empty** in-memory repositories (they are not shared with a local `cmd/api` process). When set, the embedded migrations run first (so the MCP server can boot before `cmd/api` on a fresh database). | `cmd/mcp/main.go` |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations only (same PgBouncer reason as `cmd/api`). | `cmd/mcp/main.go` |
| `DOCK_DOOR_MODE` | `permissive` | no | Only selects the mode the `list_docks` tool reports, so it matches `cmd/api`. No consumer runs here. Invalid values are a boot error. | `cmd/mcp/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint. | `cmd/mcp/main.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute (service name `inbound-receiving-mcp`). | `cmd/mcp/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/mcp/main.go` |

## `cmd/inbound-projector`

The projector is the only writer of the **analytical** database (ADR 0006).
It never opens the OLTP database and has **no OpenTelemetry setup**: it reads
neither `OTEL_EXPORTER_OTLP_ENDPOINT` nor `SERVICE_VERSION` nor `ENVIRONMENT`.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | unset | **yes** | Analytical Postgres DSN, **direct** (not PgBouncer): the embedded analytics migrations (`analytics/migrations`) run over it at boot, then the writer pool (`MaxConns = 5`, `statement_timeout = 10s`) is opened. Missing = boot error. | `cmd/inbound-projector/main.go` |
| `KAFKA_BROKERS` | unset | **yes** | Comma-separated brokers; blanks are trimmed and empty entries dropped. Missing = boot error. | `cmd/inbound-projector/main.go` |
| `ANALYTICS_CONSUMER_GROUP` | `inbound-receiving-analytics` | no | Consumer group on `warehouse.inbound-receiving.analytics`. The chart always sets it. | `cmd/inbound-projector/main.go` |
| `ADMIN_ADDR` | `:8091` | no | Admin listener serving only `GET /healthz` and `GET /readyz`. | `cmd/inbound-projector/main.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/inbound-projector/main.go` |

## `cmd/inbound-reports`

The reports binary is read-only: it never opens the OLTP database or Kafka.

| Variable | Default | Required? | Meaning | Source |
|---|---|---|---|---|
| `ANALYTICS_DATABASE_URL` | unset | **yes** | Analytical Postgres DSN, ideally a read-only role. The pool also forces `default_transaction_read_only = on`, `MaxConns = 5` and `statement_timeout = 15s`. No migrations run here. Missing = boot error. The chart feeds it from the analytics Secret key `ANALYTICS_READER_DATABASE_URL` (falls back to the projector URL). | `cmd/inbound-reports/main.go`, `internal/adapters/outbound/analyticsstore/pool.go` |
| `HTTP_ADDR` | `:8092` | no | Listen address for `GET /healthz`, `GET /reports/receiving-performance` and `GET /reports/freshness`. | `cmd/inbound-reports/main.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC collector endpoint. | `cmd/inbound-reports/main.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute (service name `inbound-receiving-reports`). | `cmd/inbound-reports/main.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. The chart's reports Deployment does not set it, so in-cluster it reports `local`. | `internal/adapters/outbound/telemetry/telemetry.go` |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`/`warning`, `error`. | `cmd/inbound-reports/main.go` |

## Boot retry

Every first Postgres dial is wrapped in `internal/bootretry.Retry`: the
migrations (`cmd/api`, `cmd/mcp`, `cmd/inbound-projector`) and the first pool
ping (all four binaries). It exists because the first outbound TCP dial of an
Istio-injected pod is reset shortly after start.

The budget is **5 attempts** with exponential backoff starting at 1 s. The
helper sleeps only *between* attempts, so the total wait is
1 + 2 + 4 + 8 = **15 s** before the last error is returned and the process
exits non-zero. (The package comment in `internal/bootretry/bootretry.go`
says "~31s (1+2+4+8+16)"; the code never sleeps after the fifth attempt.)
None of this is configurable.

## Helm chart mapping

The chart `charts/inbound-receiving` renders these variables per Deployment:

| Deployment template | Variables set |
|---|---|
| `deployment.yaml` (`cmd/api`) | `HTTP_ADDR`, `LOG_LEVEL`, `EVENT_PUBLISHER`, `OUTBOX_RELAY_INTERVAL`, `PRODUCT_MODE`, `PRODUCT_CONSUMER_GROUP`, `DOCK_DOOR_MODE`, `DOCK_DOOR_CONSUMER_GROUP`, `SHUTDOWN_DRAIN_DELAY`, `CORS_ALLOWED_ORIGINS`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`, `KAFKA_BROKERS` |
| `mcp-deployment.yaml` | `MCP_ADDR`, `LOG_LEVEL`, `DOCK_DOOR_MODE`, `ENVIRONMENT`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `DATABASE_URL`, `MIGRATIONS_DATABASE_URL` |
| `projector-deployment.yaml` | `ADMIN_ADDR`, `KAFKA_BROKERS`, `ANALYTICS_CONSUMER_GROUP`, `LOG_LEVEL`, `ANALYTICS_DATABASE_URL` |
| `reports-deployment.yaml` | `HTTP_ADDR`, `LOG_LEVEL`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `ANALYTICS_DATABASE_URL` |

See the [runbook](./runbook.md) for deployment, probes and routine
procedures.
