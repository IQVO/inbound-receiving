# inbound-receiving

[![CI](https://github.com/IQVO/inbound-receiving/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/IQVO/inbound-receiving/actions/workflows/ci.yml?query=branch%3Adevelop) [![coverage gate](https://img.shields.io/badge/coverage%20gate-90%25-brightgreen)](https://github.com/IQVO/inbound-receiving/blob/develop/Makefile) [![Go](https://img.shields.io/badge/go-1.27.3-00ADD8?logo=go&logoColor=white)](https://github.com/IQVO/inbound-receiving/blob/develop/go.mod) [![release](https://img.shields.io/github/v/release/IQVO/inbound-receiving?sort=semver)](https://github.com/IQVO/inbound-receiving/releases) [![docs](https://img.shields.io/badge/docs-iqvo.github.io-blue)](https://iqvo.github.io/inbound-receiving/) [![license](https://img.shields.io/badge/license-MIT-blue)](https://github.com/IQVO/inbound-receiving/blob/develop/LICENSE)

Inbound Receiving is the WMS-tier bounded context of the warehouse-systems
fleet that owns the inbound dock workflow: the supplier's advance ship notice
(ASN), the carrier's dock appointment and the counted receipt, with the
short / over / damaged discrepancies found when a receipt closes. It does
**not** own the product catalogue (`product-master`), the warehouse map
(`facility-layout`) or putaway — stock hands over to `inventory-storage`
through published events (ADR 0003). Hexagonal Go, Postgres, REST, Kafka
(CloudEvents 1.0).

This repository holds the contracts, the domain model, the service (REST,
Postgres, transactional outbox, local-copy consumers), its packaging (image
and Helm chart), a read-only MCP server, a read-only A2A agent, the analytics
read side (projector and reports, ADR 0006) and the `warehouse-console`
micro-frontend remote `inbound_mfe` (`web/`). Go 1.27.2 (`go.mod`).
Contracts: [`apis/openapi.yaml`](apis/openapi.yaml) (REST) and
[`apis/asyncapi.yaml`](apis/asyncapi.yaml) (events). Agent guides:
`.claude/rules/`; harness: [`HARNESS.md`](HARNESS.md). Scaffolded from
[warehouse-harness-template](https://github.com/IQVO/warehouse-harness-template).

## Disclaimer

This project is a domain-driven design study. It is not associated with,
endorsed by or affiliated with any company, and it uses no company's code,
data, logos or trademarks. All names, figures and examples are illustrative
or synthetic.

---

## Documentation

Published site: **<https://iqvo.github.io/inbound-receiving/>** (built from
`docs/`; Pages publishes from `main`).

| Area | Pages |
|---|---|
| Overview | [Introduction](docs/docs/intro.md), [Quickstart](docs/docs/overview/quickstart.md), [Architecture and runtime](docs/docs/overview/runtime.md), [Receiving workflow](docs/docs/overview/receiving-workflow.md) |
| Operations | [Configuration (every env var per binary)](docs/docs/operations/configuration.md), [Runbook](docs/docs/operations/runbook.md), [Observability](docs/docs/operations/observability.md), [Troubleshooting](docs/docs/operations/troubleshooting.md) |
| Development | [Testing](docs/docs/development/testing.md) |
| MCP & A2A | [MCP tools](docs/docs/mcp/tools.md), [A2A skills (ADR 0016)](docs/adr/0016-a2a-inbound-adapter.md) |
| Ecosystem | [Upstream contracts](docs/docs/ecosystem/upstream-contracts.md), [Downstream consumers](docs/docs/ecosystem/downstream-consumers.md) |
| DDD | [Use cases](docs/docs/ddd/use-cases.md), [Subdomain classification](docs/docs/ddd/subdomain-classification.md), [DDD artifacts (ddd-crew pack)](docs/docs/ddd/ddd-artifacts.md) |
| Decisions | [ADR index](docs/adr/index.md) — 16 decisions |

---

## Architecture

Hexagonal / ports & adapters; dependencies point inward only.

```text
apis/                    openapi.yaml (REST), asyncapi.yaml (events)
cmd/api/                 the REST service — composition root
cmd/mcp/                 read-only MCP server (ADR 0005)
cmd/a2a/                 read-only A2A agent (ADR 0016)
cmd/inbound-projector/   analytics projector — the only analytical-DB writer (ADR 0006)
cmd/inbound-reports/     analytics reader (:8092)
internal/domain/         asn, appointment, receipt, shared (value types)
internal/application/    usecases, ports, repository, outbox, idempotency
internal/adapters/
  inbound/http/          chi handlers, RFC 7807 errors, idempotency middleware, otelchi
  inbound/kafka/         local-copy consumers (product, dock doors) + the projector's consumer
  inbound/mcp/           MCP tools (governance-tested: no write verbs)
  inbound/a2a/           A2A skills
  outbound/postgres/     pgxpool repos, pgtx UnitOfWork, outbox + relay, housekeeping sweeper
  outbound/kafka/        CloudEvents encoder + publisher
  outbound/outbox/       the transactional outbox relay
  outbound/analyticsstore/  analytical-DB projection and report queries (read-only pool)
  outbound/telemetry/    OpenTelemetry wiring (traces, metrics, /metrics)
  outbound/memory/, clock/, idgen/  test doubles and system ports
internal/architecture/   hexagonal and fleet fitness tests
internal/housekeeping/   sweeper intervals (ADR 0010)
internal/analytics/      the report model
migrations/              golang-migrate SQL (analytics/migrations for the analytical DB)
features/                godog scenarios
web/                     the inbound_mfe remote
charts/inbound-receiving/  Helm chart (values, templates, tests/)
docs/                    Docusaurus site (docs/docs) and the ADRs (docs/adr)
```

The domain aggregates:

| Package | Aggregate | Highlights |
|---|---|---|
| `asn` | `Asn` | lines numbered `1..n`, unique SKUs; `Registered -> Receiving -> Closed` or `Cancelled` |
| `appointment` | `DockAppointment` | window <= 4 h, no overlap per door (`Schedule`), check-in from 30 min before the window |
| `receipt` | `Receipt` | Good / Damaged counts per line, over-receipt allowed, discrepancies on close |
| `shared` | value types | SKU, quantity, reason |

The layering is enforced by the executable fitness tests in
`internal/architecture` (`make arch-test`, CI job `arch-test`) — including
`TestPostgresIntegrationTestsUseTestcontainers` and
`TestKafkaIntegrationTestsUseTestcontainers`, which fail the build if an
integration test stops using throwaway infrastructure.

---

## Domain events

All messages are **CloudEvents 1.0** in structured content mode; the type
convention is `com.warehouse.wms.inbound-receiving.<entity>.<EventName>` (the
`wms` subdomain segment). `cmd/api` writes every domain event **twice in one
transaction** (`FanoutEncoder`): to the integration topic and, under the same
CloudEvents id, to the analytics topic (ADR 0004, ADR 0006).

**Publishes** — 11 events on `warehouse.inbound-receiving.events` (each with
an `…Analytics` twin on `warehouse.inbound-receiving.analytics`):

| Entity | Events |
|---|---|
| `asn` | `ASNRegistered`, `ASNCancelled` |
| `dockappointment` | `DockAppointmentBooked`, `DockAppointmentCheckedIn`, `DockAppointmentCompleted`, `DockAppointmentCancelled` |
| `receipt` | `ReceiptOpened`, `ReceiptLineReceived`, `ReceiptCartonReceived`, `ReceiptClosed`, `CartonDiscrepancyRaised` |

Real types: `com.warehouse.wms.inbound-receiving.asn.ASNRegistered`,
`com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked`,
`com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived`.

**Consumes** (local copies, ADR 0003; every other type ignored):

| Topic | Messages | Consumer group |
|---|---|---|
| `warehouse.product-master.events` | `ProductRegistered` (known SKUs), `ProductPackLevelDeclared` / `ProductPackLevelRetired` (packaging view: pack level → eaches factor for case-level scans) | `PRODUCT_CONSUMER_GROUP` (SKUs), `PACKAGING_CONSUMER_GROUP` (packaging view) |
| `warehouse.facility.events` | `LocationSlotRegistered`, `LocationSlotDecommissioned` (known inbound doors) | `DOCK_DOOR_CONSUMER_GROUP` |

---

## Running it

Postgres and Kafka are the only runtime (ADR 0011): there is no in-memory
mode, `EVENT_PUBLISHER=log` is a boot error, and `cmd/api` refuses to boot
without `DATABASE_URL`, `KAFKA_BROKERS` and all three consumer groups — every
error names its variable. Migrations run at boot.

```sh
docker compose up -d --wait    # postgres:16 on localhost:5437 + confluent-local Kafka on localhost:9092
export DATABASE_URL='postgres://postgres:***@localhost:5437/postgres?sslmode=disable'
export KAFKA_BROKERS='localhost:9092'
export PRODUCT_CONSUMER_GROUP=inbound-product \
       DOCK_DOOR_CONSUMER_GROUP=inbound-doors \
       PACKAGING_CONSUMER_GROUP=inbound-packaging
go run ./cmd/api               # REST on :8080
```

`GET /readyz` is 200 only while Postgres answers a ping **and** a Kafka
broker answers a metadata request (verdict cached 3 s, bounded 2 s); it
returns 503 while draining. `/healthz` is liveness only.

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | REST listen address |
| `DATABASE_URL` | **required** | Postgres DSN; boot error when unset |
| `KAFKA_BROKERS` | **required** | Comma-separated brokers (relay, consumers, `/readyz`) |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka` is accepted; `log` is a boot error |
| `PRODUCT_MODE` | *(unset = `kafka`)* | Only `kafka`; `permissive`/`off` are boot errors |
| `PRODUCT_CONSUMER_GROUP` | **required** | Stable group of the product consumer |
| `DOCK_DOOR_MODE` | *(unset = `kafka`)* | Only `kafka`; `permissive`/`off` are boot errors |
| `DOCK_DOOR_CONSUMER_GROUP` | **required** | Stable group of the dock-door consumer |
| `PACKAGING_CONSUMER_GROUP` | **required** | Stable group of the packaging-view consumer |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Relay poll interval (Go duration or seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row (`0` = unlimited) |
| `CARTON_MAX_GROSS_WEIGHT_G` | `0` (unchecked) | CartonOverLimit weight limit in grams (ADR 0014) |
| `CARTON_MAX_LONGEST_SIDE_MM` | `0` (unchecked) | CartonOverLimit longest-side limit in mm (ADR 0014) |
| `HOUSEKEEPING_INTERVAL` | `1h` | Sweeper interval (`0` = sweeper off, ADR 0010) |
| `IDEMPOTENCY_KEY_TTL` | `24h` | `idempotency_keys` row lifetime (`0` = keep forever) |
| `OUTBOX_RETENTION` | `168h` | Published `outbox_events` retention (`0` = keep forever) |
| `PROCESSED_EVENTS_RETENTION` | `720h` | Dedupe-mark retention (`0` = keep forever) |
| `MIGRATIONS_DATABASE_URL` | *(unset = `DATABASE_URL`)* | Direct DSN for the boot migrations |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP/gRPC Collector endpoint |
| `SERVICE_VERSION` | `dev` | `service.version` resource attribute |
| `ENVIRONMENT` | `local` | `deployment.environment.name` |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait after flipping `/readyz` before closing |
| `CORS_ALLOWED_ORIGINS` | *(unset)* | Comma-separated browser origins allowed to call the API (the `web/` remote) |

The other binaries:

| Binary | Env vars |
|---|---|
| `cmd/mcp` | `MCP_ADDR` (`:8090`), `DATABASE_URL` (**required**), `MIGRATIONS_DATABASE_URL`, `LOG_LEVEL`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `cmd/a2a` | `A2A_ADDR` (`:8093`), `A2A_AGENT_URL` (**required**, absolute URL advertised on the Agent Card), `DATABASE_URL` (**required**), `MIGRATIONS_DATABASE_URL`, `LOG_LEVEL`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `cmd/inbound-projector` | `ANALYTICS_DATABASE_URL` (**required**), `KAFKA_BROKERS` (**required**), `ANALYTICS_CONSUMER_GROUP` (default `inbound-receiving-analytics`), `ADMIN_ADDR` (`:8091`), `HOUSEKEEPING_INTERVAL` (default `1h`), `PROCESSED_EVENTS_RETENTION`, `LOG_LEVEL`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `cmd/inbound-reports` | `ANALYTICS_DATABASE_URL` (**required**), `HTTP_ADDR` (`:8092`), `LOG_LEVEL`, `SERVICE_VERSION`, `OTEL_EXPORTER_OTLP_ENDPOINT` |

Every variable is tabulated per binary in
[Configuration](docs/docs/operations/configuration.md).

---

## Observability

The service wires OpenTelemetry in
`internal/adapters/outbound/telemetry` — an outbound adapter, the same tier
as `outbound/postgres`; the domain and application layers never import it
(ADR 0008).

- **Traces**: a `TracerProvider` exporting over OTLP/gRPC; `otelchi`
  middleware runs first on the chi router so every handler runs inside a
  span. The W3C trace-context + baggage propagator is installed, and trace
  context crosses the outbox onto the Kafka message (ADR 0009).
- **Metrics**: a `MeterProvider` over OTLP/gRPC (30 s export interval) plus
  a Prometheus scrape handler at **`GET /metrics`**. Exported: otelchi HTTP
  server request duration, Go runtime metrics, and the business counter
  **`inbound_receiving.receipts.units_received`** (ADR 0013), recorded in
  the receive-line use case.
- **Logs**: structured `log/slog` JSON on stdout; level from `LOG_LEVEL`.

Export is deliberately non-blocking: with no Collector listening at
`OTEL_EXPORTER_OTLP_ENDPOINT`, spans and metrics are dropped silently and
the service starts and serves exactly as it would without OTel.

---

## API

REST on `HTTP_ADDR` (default `:8080`), specified in `apis/openapi.yaml`.
Errors are **RFC 7807** `application/problem+json` throughout. 22 operations
over 19 paths:

| Method | Path | Use case |
|---|---|---|
| POST, GET | `/asns` | RegisterAsn, ListAsns |
| GET | `/asns/{asnNumber}` | GetAsn |
| GET | `/asns/{asnNumber}/cartons` | the ASN's carton tree (ADR 0012) |
| POST | `/asns/{asnNumber}/cancel` | CancelAsn |
| POST, GET | `/appointments` | BookAppointment, ListAppointments |
| GET | `/appointments/{appointmentId}` | GetAppointment |
| POST | `/appointments/{appointmentId}/check-in`, `/cancel` | CheckInAppointment, CancelAppointment |
| POST, GET | `/receipts` | OpenReceipt, ListReceipts |
| GET | `/receipts/{receiptId}` | GetReceipt |
| POST | `/receipts/{receiptId}/lines` | ReceiveLine |
| GET | `/receipts/{receiptId}/cartons/{lpn}/label` | printable carton label (ADR 0014) |
| POST | `/receipts/{receiptId}/scans` | case-level scan receiving |
| POST | `/receipts/{receiptId}/close` | CloseReceipt |
| GET | `/docks` | ListDocks |
| GET | `/healthz`, `/readyz` | liveness; readiness (503 while draining) |
| GET | `/metrics` | Prometheus scrape (not in the OpenAPI spec) |
| GET | `/reports/receiving-performance`, `/reports/freshness` | analytics — served by `cmd/inbound-reports`, not `cmd/api` |

**Idempotency**: every resource-creating `POST` (`/asns`, `/appointments`,
`/receipts`) **requires** an `Idempotency-Key` header (`400
idempotency-key-required` when missing); a retry with the same key and body
replays the original response, the same key with a different body is `422
idempotency-key-reused`. Action routes (cancel, check-in, receive line,
scans, close) *honour* the header when sent. Problem types are catalogued in
[Troubleshooting](docs/docs/operations/troubleshooting.md).

### Analytics data product (Receiving Performance)

ADR 0006. `cmd/inbound-projector` consumes
`warehouse.inbound-receiving.analytics` (group `ANALYTICS_CONSUMER_GROUP`,
default `inbound-receiving-analytics`; poison is dead-lettered byte-for-byte
to `...analytics.dlq`) into a **separate** analytical Postgres
(`ANALYTICS_DATABASE_URL`, embedded migrations in `analytics/migrations`) and
serves `/healthz` and `/readyz` on `:8091`. `cmd/inbound-reports` reads it
through a read-only pool and serves `GET /reports/receiving-performance`
(per UTC day over an optional `from`/`to`, default the last 30 days, at most
366) and `GET /reports/freshness` (`as_of`, `lag_seconds`). Neither opens
the OLTP database.

---

## Helm chart

```bash
docker build -t inbound-receiving:local .     # /app/api (default entrypoint), /app/mcp, /app/a2a, /app/inbound-projector, /app/inbound-reports
(cd web && docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/inbound-receiving-frontend:local .)   # the inbound_mfe remote (nginx)
helm lint charts/inbound-receiving -f charts/inbound-receiving/ci/all-components-values.yaml
python3 charts/inbound-receiving/tests/test_service_selectors.py
```

The image carries every `cmd/*` binary and runs as uid 1000. The chart
`charts/inbound-receiving` renders the api Deployment + Service (component
`api`; `database.url` or `database.existingSecret`, `kafka.brokers` and the
consumer groups are required; a direct `MIGRATIONS_DATABASE_URL` is read from
`database.migrationsExistingSecretKey`), an optional HPA and Gateway API
`HTTPRoute`/Ingress, the MCP Deployment + Service (component `mcp`,
`mcp.enabled`, default off), the A2A agent (component `a2a`, `a2a.enabled`,
default off; the Agent Card URL is `a2a.agentUrl`, default the in-cluster
Service DNS), the analytics projector and reports (components
`analytics-projector` / `analytics-reports`, `analytics.enabled`, default
off) and the web remote (component `frontend`, `frontend.enabled`, default
off). Every env var of `cmd/api` has a dedicated value
(`config.eventPublisher`, `kafka.brokers`, `config.productMode` /
`config.productConsumerGroup`, `config.dockDoorMode` /
`config.dockDoorConsumerGroup`, `config.outboxRelayInterval`, ...); the
chart refuses to render states that would silently do nothing or crash-loop
(no database, no brokers, no consumer group, `permissive`/`off` modes, the
`log` publisher; Postgres and Kafka are the only runtime, ADR 0011). Render
assertions live in `charts/inbound-receiving/tests/` (`render_test.sh`,
`test_service_selectors.py`, `test_kong_config.py`), run by the CI
`chart-tests` job; `helm-lint` lints the chart with the all-components CI
values. Images and charts publish to `ghcr.io/iqvo/inbound-receiving`.

### Running the MCP server in Kubernetes

`mcp.enabled=true` renders the MCP Deployment + Service on port `8090`
(ADR 0005). The server (official MCP Go SDK, Streamable HTTP only, served at
`/` and `/mcp`, `GET /healthz`) runs over the same read use cases and
database as the REST API; it starts no relay, no consumer, never dials
Kafka, and has no auth (fleet-wide). Tools, all read-only and snake_case:

| Tool | Filters |
|---|---|
| `get_asn` | `asn_number` |
| `list_asns` | `state`, `limit`, `cursor` |
| `get_appointment` | `appointment_id` |
| `list_appointments` | `door`, `state`, `from`, `to`, `limit`, `cursor` |
| `get_receipt` | `receipt_id` |
| `list_receipts` | `asn_number`, `state`, `limit`, `cursor` |
| `list_docks` | none |

Writes (register, cancel, book, check in, open, receive, close) stay on
REST; `internal/adapters/inbound/mcp/governance_test.go` fails the build on
a write-verb tool name. Details: [`.claude/rules/mcp.md`](.claude/rules/mcp.md).

### Running the A2A agent in Kubernetes

`a2a.enabled=true` renders the A2A Deployment + Service on port `8093`
(ADR 0016; official A2A Go SDK `a2aproject/a2a-go/v2`, JSON-RPC over HTTP,
served at `/` and `/a2a`, the Agent Card at
`/.well-known/agent-card.json`, `GET /healthz`). It is **read-only**, like
the MCP server: no outbox row, no relay, no consumer, never dials Kafka, no
outbound A2A client, no auth. `A2A_AGENT_URL` (the absolute URL advertised
on the card) is mandatory. Two curated skills, called as a `SendMessage`
data part `{"skill": "<id>", "arguments": {...}}` and answered in one
message:

| Skill | Arguments | Answers |
|---|---|---|
| `asn_receiving_status` | `asnNumber` | the ASN (state, lines) with its receipts and their Short / Over / Damaged discrepancies |
| `dock_schedule` | optional `door`, `state`, `from`, `to` (RFC 3339), `limit` (1..100) | dock appointments in window order, `truncated` when more exist |

Domain rejections come back in-band as an RFC 7807 `problem` with the REST
adapter's type namespace and statuses (`asn-not-found` 404,
`invalid-asn-number` and `invalid-query` 400, ...); unknown arguments are
refused. A2A carries delegation only: registering, booking, receiving and
closing stay on REST and leave as CloudEvents on Kafka.

---

## Operator micro-frontend (`web/`)

`inbound_mfe`, a Vite + React Module Federation remote that exposes
`./App`; the `warehouse-console` shell mounts it under
`/inbound-receiving/*`. Production asset base `/mfes/inbound-receiving/`;
standalone dev server on `:5193`:

```bash
cd web && npm ci && npm run dev   # http://localhost:5193
```

It calls `http://localhost:8080`, so start `cmd/api` with
`CORS_ALLOWED_ORIGINS=http://localhost:5193`. It needs a sibling
`warehouse-ui-kit` checkout (`file:../../warehouse-ui-kit`). CI job `web`:
lint, `tsc -b`, vitest, build. Carton screens (ASN carton tree, receipt
carton discrepancies, printable carton label) are described in
[the receiving workflow](docs/docs/overview/receiving-workflow.md).

---

## Local development / quality gate

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage (>= 90% over domain+application) + arch-test + bdd
make mutation     # gremlins on every package under internal/domain
make integration  # go test -tags=integration ./... -race -count=1 (needs only Docker)
```

`make check` / `check-all` deliberately exclude `integration` and
`mutation`. lefthook runs `fmt-check`, `vet` and `lint` on pre-commit and
`make check` on pre-push (`lefthook install` once per clone).

## Tests

- **Unit, handler and BDD** (`make test`, `make bdd`): no database; godog
  features in `features/`, handler tests on `httptest` with fake use cases.
- **Integration** (`-tags=integration`, `make integration`, CI job
  `integration`): every suite starts its own throwaway infrastructure with
  testcontainers (`postgres:16-alpine`, `confluentinc/confluent-local:7.6.1`).
  No `DATABASE_URL`, no docker compose, no CI service container and no
  `t.Skip` gate; `TestPostgresIntegrationTestsUseTestcontainers` and
  `TestKafkaIntegrationTestsUseTestcontainers` (`internal/architecture`) fail
  the build otherwise. Packages that share one container boot it once in a
  `TestMain` and isolate tests with a private database each (the use-case
  wiring, `pgtx`, REST and MCP suites clone it from a template database
  migrated once) or a unique Kafka topic each.

| Suite | What it proves against real infrastructure |
|---|---|
| `internal/application/usecases/usecases_wiring_integration_test.go` | the write use cases wired exactly like `cmd/api` (Postgres repos, UnitOfWork, CloudEvents encoder into the outbox): ASN lifecycle, appointment to closed receipt, door-overlap and unknown-ASN refusals |
| `internal/adapters/outbound/postgres/pgtx/pgtx_integration_test.go` | the pgx transaction carried through `pgtx`: save + outbox commit together, roll back together, nested `Do` joins via a savepoint |
| `internal/adapters/outbound/postgres/*_integration_test.go` | repositories (version guards, paging, one open receipt per ASN), use-case atomicity, concurrent bookings, the idempotency store, and the analytics fanout (both topics' rows under one id in one transaction) |
| `internal/adapters/outbound/kafka/publisher_integration_test.go` | the production `Encoder` + `RelaySink` publish the CloudEvents 1.0 contract to a real broker |
| `internal/adapters/outbound/outbox/relay_integration_test.go` | the relay drains Postgres outbox rows to Kafka with the right type and key |
| `internal/adapters/inbound/kafka/consumers_integration_test.go` | product and dock-door consumers fill the local copies; invalid and foreign messages skipped; redelivery deduped |
| `internal/adapters/inbound/kafka/analytics_consumer_integration_test.go` | the projector's consumer: garbage skipped, poison dead-lettered byte for byte, redelivery applied once, offsets committed |
| `internal/adapters/outbound/analyticsstore/postgres_integration_test.go` | the analytical store's contract, day windows, atomic dedupe + effect, and that the read-only pool cannot write |
| `internal/adapters/inbound/http/rest_api_integration_test.go` | the real chi router over real use cases and Postgres: ASN lifecycle and validation (RFC 7807), Idempotency-Key replay and reuse, receipt flow |
| `internal/adapters/inbound/mcp/streamable_http_integration_test.go` | MCP over the real Streamable HTTP transport with the SDK client: `tools/list` contract, `tools/call` round trips, domain and input errors as tool errors |
| `cmd/mcp/main_integration_test.go` | the `cmd/mcp` wiring reads what the API wrote and never writes |
| `cmd/a2a/main_integration_test.go` | the `cmd/a2a` wiring, driven by the SDK client through the real router, answers what the API wrote and never writes |
| `analytics_e2e_integration_test.go` | the whole ADR 0006 path: OLTP writes, relay to Kafka (with a lost ack republished under the same id), projector into a separate analytical Postgres, reports endpoints answering from it |

- **Mutation**: gremlins over `internal/domain` — `mutation-fast` on every
  push/PR, the exhaustive `mutation` run on schedule.
- **Web**: the remote has its own vitest suite (`cd web && npm test`).

CI (`.github/workflows/ci.yml`) runs on every push and PR: `lint`,
`guide-lint`, `complexity`, `test` (with the 90% coverage gate), `bdd`,
`integration`, `mutation-fast`, `api-lint`, `vuln` (govulncheck),
`helm-lint`, `chart-tests`, `docs-api-drift` (the docs REST reference is
regenerated from `apis/openapi.yaml` and must match what is committed),
`web`, `arch-test`, plus the scheduled `mutation` and advisory `drift`;
`trivy-scan` builds the image and blocks on CRITICAL/HIGH CVEs with a fix
(PR targets of `main`); `docker-publish` pushes to GHCR once green and
`release` tags the next version and publishes the chart.

## Architecture Decision Records

The ADR index lives at [docs/adr/index.md](docs/adr/index.md) — 16
decisions (0001–0016). Key ones: the
[bounded context](docs/adr/0001-inbound-receiving-bounded-context.md),
[aggregates and invariants](docs/adr/0002-aggregates-and-invariants.md),
[local copies and the handover to inventory-storage](docs/adr/0003-local-copies-and-handover.md),
[event catalogue](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md),
[MCP server](docs/adr/0005-mcp-server-adoption.md),
[analytics read side](docs/adr/0006-analytics-read-side.md) and the
[A2A inbound adapter](docs/adr/0016-a2a-inbound-adapter.md).

## License

MIT — see [LICENSE](LICENSE).
