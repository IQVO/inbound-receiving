# inbound-receiving

Inbound Receiving is the WMS-tier bounded context of the warehouse-systems
fleet that owns the inbound dock workflow: the supplier's advance ship notice
(ASN), the carrier's dock appointment and the counted receipt, with the
short / over / damaged discrepancies found when a receipt closes. Hexagonal Go,
Postgres, REST, Kafka (CloudEvents 1.0).

This repository holds the contracts, the domain model, the service (REST,
Postgres, transactional outbox, local-copy consumers), its packaging (image and
Helm chart), a read-only MCP server, the analytics read side (projector and
reports, [ADR 0006](docs/adr/0006-analytics-read-side.md)) and the
`warehouse-console` micro-frontend remote `inbound_mfe` (`web/`). Go 1.27.2
(`go.mod`). Docs site: `docs/` (Docusaurus; ADR index in
[`docs/adr/index.md`](docs/adr/index.md)).

- Why it exists and how it relates to the other contexts:
  [ADR 0001](docs/adr/0001-inbound-receiving-bounded-context.md)
- Aggregates and invariants:
  [ADR 0002](docs/adr/0002-aggregates-and-invariants.md)
- Local copies, consumed contracts and the handover to inventory-storage:
  [ADR 0003](docs/adr/0003-local-copies-and-handover.md)
- Event catalogue: [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md)
- MCP server (read-only, unauthenticated, additive):
  [ADR 0005](docs/adr/0005-mcp-server-adoption.md)
- Analytics read side (analytics stream, projector, reports):
  [ADR 0006](docs/adr/0006-analytics-read-side.md)
- Contracts: [`apis/openapi.yaml`](apis/openapi.yaml) (REST),
  [`apis/asyncapi.yaml`](apis/asyncapi.yaml) (events on
  `warehouse.inbound-receiving.events` and the consumed channels)
- Agent guides: `.claude/rules/` ; harness: [`HARNESS.md`](HARNESS.md)

## Domain (`internal/domain`)

| Package | Aggregate | Highlights |
|---|---|---|
| `asn` | `Asn` | lines numbered `1..n`, unique SKUs; `Registered -> Receiving -> Closed` or `Cancelled` |
| `appointment` | `DockAppointment` | window <= 4 h, no overlap per door (`Schedule`), check-in from 30 min before the window |
| `receipt` | `Receipt` | Good / Damaged counts per line, over-receipt allowed, discrepancies on close |
| `shared` | value types | SKU, quantity, reason |

## Endpoints (summary)

| Method | Path | Use case |
|---|---|---|
| POST, GET | `/asns` | RegisterAsn, ListAsns |
| GET | `/asns/{asnNumber}` | GetAsn |
| POST | `/asns/{asnNumber}/cancel` | CancelAsn |
| POST, GET | `/appointments` | BookAppointment, ListAppointments |
| GET | `/appointments/{appointmentId}` | GetAppointment |
| POST | `/appointments/{appointmentId}/check-in`, `/cancel` | CheckInAppointment, CancelAppointment |
| POST, GET | `/receipts` | OpenReceipt, ListReceipts |
| GET | `/receipts/{receiptId}` | GetReceipt |
| POST | `/receipts/{receiptId}/lines` | ReceiveLine |
| POST | `/receipts/{receiptId}/close` | CloseReceipt |
| GET | `/docks` | ListDocks |
| GET | `/healthz`, `/readyz`, `/metrics` | liveness, readiness (503 while draining), Prometheus |

`cmd/inbound-reports` (`:8092`) serves the analytics endpoints from the
analytical database: `GET /reports/receiving-performance` (per UTC day over an
optional `from` / `to`, default the last 30 days, at most 366) and
`GET /reports/freshness` (`as_of`, `lag_seconds`), plus `GET /healthz`.

## Analytics read side (ADR 0006)

`cmd/api` writes every domain event twice in one transaction (`FanoutEncoder`):
to `warehouse.inbound-receiving.events` and, under the same CloudEvents id, to
`warehouse.inbound-receiving.analytics`. `cmd/inbound-projector` consumes the
analytics topic (group `ANALYTICS_CONSUMER_GROUP`, default
`inbound-receiving-analytics`; poison goes to `...analytics.dlq`) into a
SEPARATE analytical Postgres (`ANALYTICS_DATABASE_URL`, embedded migrations in
`analytics/migrations`) and serves `/healthz` and `/readyz` on `:8091`.
`cmd/inbound-reports` reads it through a read-only pool. Neither opens the OLTP
database. Both need `ANALYTICS_DATABASE_URL`; the projector also needs
`KAFKA_BROKERS`.

## Web remote (`web/`)

`inbound_mfe`, a Vite + React Module Federation remote that exposes `./App`;
the `warehouse-console` shell mounts it under `/inbound-receiving/*`. Production
asset base `/mfes/inbound-receiving/`; standalone dev server on `:5193`
(`cd web && npm ci && npm run dev`; it calls `http://localhost:8080`, so start
`cmd/api` with `CORS_ALLOWED_ORIGINS=http://localhost:5193`). It needs a sibling
`warehouse-ui-kit` checkout (`file:../../warehouse-ui-kit`). CI job `web`:
lint, `tsc -b`, vitest, build.

## MCP server (read-only)

`cmd/mcp` is a second deployable (official MCP Go SDK, Streamable HTTP only,
`:8090`, served at `/` and `/mcp`, `GET /healthz`) over the same read use cases
and database as the REST API. It starts no relay, no consumer and never dials
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

Writes (register, cancel, book, check in, open, receive, close) stay on REST;
`internal/adapters/inbound/mcp/governance_test.go` fails the build on a
write-verb tool name. Details: [`.claude/rules/mcp.md`](.claude/rules/mcp.md).

```bash
DOCK_DOOR_MODE=permissive go run ./cmd/mcp   # in-memory without DATABASE_URL
```

## Packaging

```bash
docker build -t inbound-receiving:local .     # /app/api (default entrypoint), /app/mcp, /app/inbound-projector, /app/inbound-reports
(cd web && docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/inbound-receiving-frontend:local .)   # the inbound_mfe remote (nginx)
helm lint charts/inbound-receiving -f charts/inbound-receiving/ci/all-components-values.yaml
python3 charts/inbound-receiving/tests/test_service_selectors.py
```

The image carries every `cmd/*` binary and runs as uid 1000. The chart
`charts/inbound-receiving` renders the api Deployment + Service (component
`api`; `database.url` or `database.existingSecret` is required, a direct
`MIGRATIONS_DATABASE_URL` is read from `database.migrationsExistingSecretKey`),
an optional HPA and Gateway API `HTTPRoute`/Ingress, the MCP Deployment +
Service (component `mcp`, `mcp.enabled`, default off), the analytics
projector and reports (components `analytics-projector` / `analytics-reports`,
`analytics.enabled`, default off) and the web remote (component `frontend`,
`frontend.enabled`, default off). Every env var of
`cmd/api` has a dedicated value (`config.eventPublisher`, `kafka.brokers`,
`config.productMode` / `config.productConsumerGroup`, `config.dockDoorMode` /
`config.dockDoorConsumerGroup`, `config.outboxRelayInterval`, ...); the chart
refuses to render states that would silently do nothing or crash-loop (no
database, a Kafka mode without brokers or a consumer group, a group set while
its mode is permissive). Images and charts publish to
`ghcr.io/iqvo/inbound-receiving`.

## Quality gates

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage (>= 90 % over domain+application) + arch-test + bdd
make mutation     # gremlins on every package under internal/domain
make integration  # go test -tags=integration ./... -race -count=1 (needs only Docker)
```

lefthook runs `fmt-check`, `vet` and `lint` on pre-commit and `make check` on
pre-push (`lefthook install` once per clone).

## Testing

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
| `analytics_e2e_integration_test.go` | the whole ADR 0006 path: OLTP writes, relay to Kafka (with a lost ack republished under the same id), projector into a separate analytical Postgres, reports endpoints answering from it |

`make check` / `check-all` deliberately exclude `integration` and `mutation`.
The web remote has its own vitest suite (`cd web && npm test`).

Scaffolded from [warehouse-harness-template](https://github.com/IQVO/warehouse-harness-template).
Study project: not production software.
