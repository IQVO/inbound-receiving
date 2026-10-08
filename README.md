# inbound-receiving

Inbound Receiving is the WMS-tier bounded context of the warehouse-systems
fleet that owns the inbound dock workflow: the supplier's advance ship notice
(ASN), the carrier's dock appointment and the counted receipt, with the
short / over / damaged discrepancies found when a receipt closes. Hexagonal Go,
Postgres, REST, Kafka (CloudEvents 1.0).

This repository holds the contracts, the domain model, the service (REST,
Postgres, transactional outbox, local-copy consumers), its packaging (image and
Helm chart) and a read-only MCP server; analytics and web layers follow in later
phases.

- Why it exists and how it relates to the other contexts:
  [ADR 0001](docs/adr/0001-inbound-receiving-bounded-context.md)
- Aggregates and invariants:
  [ADR 0002](docs/adr/0002-aggregates-and-invariants.md)
- Local copies, consumed contracts and the handover to inventory-storage:
  [ADR 0003](docs/adr/0003-local-copies-and-handover.md)
- Event catalogue: [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md)
- MCP server (read-only, unauthenticated, additive):
  [ADR 0005](docs/adr/0005-mcp-server-adoption.md)
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
docker build -t inbound-receiving:local .     # /app/api (default entrypoint) and /app/mcp
helm lint charts/inbound-receiving -f charts/inbound-receiving/ci/all-components-values.yaml
python3 charts/inbound-receiving/tests/test_service_selectors.py
```

The image carries every `cmd/*` binary and runs as uid 1000. The chart
`charts/inbound-receiving` renders the api Deployment + Service (component
`api`; `database.url` or `database.existingSecret` is required, a direct
`MIGRATIONS_DATABASE_URL` is read from `database.migrationsExistingSecretKey`),
an optional HPA and Gateway API `HTTPRoute`/Ingress, and the MCP Deployment +
Service (component `mcp`, `mcp.enabled`, default off). Every env var of
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
```

Scaffolded from [warehouse-harness-template](https://github.com/IQVO/warehouse-harness-template).
Study project: not production software.
