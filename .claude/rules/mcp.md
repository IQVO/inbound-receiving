---
paths:
  - "internal/adapters/inbound/mcp/**"
  - "cmd/mcp/**"
  - "charts/inbound-receiving/templates/mcp-*.yaml"
---

# MCP server (inbound adapter, read-only)

One MCP server for this bounded context, an additive inbound adapter over the
SAME read use cases the REST adapter calls. Decision record:
`docs/adr/0005-mcp-server-adoption.md`; the short fleet rule is
`.claude/rules/fleet/no-auth-and-mcp.md`.

- Code: `internal/adapters/inbound/mcp/` (tools, error mapping) and the
  composition root `cmd/mcp/` (env, repositories, router, graceful shutdown).
  Built on the official SDK `github.com/modelcontextprotocol/go-sdk` (v1.8.0).
- Transport: **Streamable HTTP only** (no stdio, no SSE). Listens on
  `MCP_ADDR` (default `:8090`), served at **`/` and `/mcp`**; `GET /healthz`
  -> `200 {"status":"ok"}`.
- **Read-only.** `Deps` holds only the seven read use cases (`GetAsn`,
  `ListAsns`, `GetAppointment`, `ListAppointments`, `GetReceipt`,
  `ListReceipts`, `ListDocks`); never add a write use case, a repository
  `Save`, the outbox or an encoder to it. Writes (register/cancel an ASN,
  book/check in/cancel an appointment, open/receive/close a receipt) are
  REST-only because their events feed other contexts and their idempotency and
  version guards live on the REST contract.
- **No auth of any kind** (fleet-wide revert 2026-09-11).
  `TestNoAuthMiddlewareReintroduced` fails CI if it is reintroduced.
- Architecture: the adapter depends ONLY on `internal/application` and
  `internal/domain`; nothing depends on it (`TestMCPAdapterDependencyRule`).
- Env (`cmd/mcp`): `MCP_ADDR`, `DATABASE_URL` (unset -> in-memory
  repositories), `MIGRATIONS_DATABASE_URL` (direct DSN for the migration step
  only), `DOCK_DOOR_MODE` (permissive | kafka; ONLY the `mode` `list_docks`
  reports, it starts no consumer), `OTEL_EXPORTER_OTLP_ENDPOINT`, `LOG_LEVEL`,
  `SERVICE_VERSION`, `ENVIRONMENT`. It runs the idempotent embedded migrations
  on start and uses `internal/bootretry` for the Postgres dial. It does NOT
  start the outbox relay, does NOT run the local-copy consumers and does NOT
  dial Kafka.
- Chart: `mcp.enabled` (default false) renders
  `charts/inbound-receiving/templates/mcp-deployment.yaml` and
  `charts/inbound-receiving/templates/mcp-service.yaml` (component `mcp`,
  command `/app/mcp`, probes on `/healthz`). Keep the env there in sync with
  `cmd/mcp/main.go`'s header; `charts/inbound-receiving/tests/test_service_selectors.py`
  asserts the mcp pod gets no Kafka/relay/consumer env.

## Tools (7; budget is 7)

Arguments are snake_case. Results are the REST bodies with snake_case names.
Failures are tool errors (`isError: true`) whose text is `<slug>: <message>`
with the REST problem slugs; unexpected infrastructure errors are logged and
reported as a generic `internal-error`. Lists page with the REST cursor
(`limit` 1..500, default 100; `next_cursor` is absent on the last page).

| Tool | Backed by | Arguments | Result | Errors |
|---|---|---|---|---|
| `get_asn` | `GetAsn` | `asn_number` | `asn_number`, `supplier_ref`, `expected_arrival` (omitted when none), `state`, `lines[]` (`line_no`, `sku`, `expected_qty`), `version` | `invalid-asn-number`, `asn-not-found` |
| `list_asns` | `ListAsns` | optional `limit`, `cursor`, `state` (Registered, Receiving, Closed, Cancelled) | `items[]` (never null), `next_cursor` | `invalid-query` |
| `get_appointment` | `GetAppointment` | `appointment_id` | `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers`, `state`, `version` | `invalid-appointment-id`, `appointment-not-found` |
| `list_appointments` | `ListAppointments` | optional `limit`, `cursor`, `door`, `state` (Booked, CheckedIn, Completed, Cancelled), `from`, `to` (RFC 3339) | `items[]`, `next_cursor` | `invalid-query` |
| `get_receipt` | `GetReceipt` | `receipt_id` | `receipt_id`, `asn_number`, `appointment_id` / `door_code` / `closed_at` (omitted when unset), `state`, `lines[]` (+ `received_good`, `received_damaged`), `opened_at`, `discrepancies[]` (Short, Over, Damaged), `version` | `invalid-receipt-id`, `receipt-not-found` |
| `list_receipts` | `ListReceipts` | optional `limit`, `cursor`, `asn_number`, `state` (Open, Closed) | `items[]`, `next_cursor` | `invalid-query` |
| `list_docks` | `ListDocks` | none | `mode`, `items[]` (`door_code`, `dock_flow`) | none |

## Tests

- `internal/adapters/inbound/mcp/governance_test.go`: `TestToolSurface` pins
  the exact set, the budget, read-only annotations, descriptions and
  documented snake_case arguments (only `list_docks` may have none), and fails
  on any write-verb tool name (`register`, `book`, `check`, `cancel`, `open`,
  `receive`, `close`, `create`, `update`, `delete`, `set`);
  `TestWriteVerbSensorFailsOnWriteNames` proves that check can fail;
  `TestToolRegistryGolden` pins the advertised registry against
  `testdata/tool_registry.golden.json`.
- `tools_test.go` drives every tool through the SDK in-memory transport over
  in-memory repos (seeded with the write use cases, as REST would) and proves
  reads add no outbox row and leak no infrastructure error text.
- `cmd/mcp/main_test.go`: `/healthz`, both mount paths over real Streamable
  HTTP, no auth required. `cmd/mcp/main_integration_test.go`
  (`-tags=integration`, testcontainers Postgres): migrations on a fresh DB,
  reads what the api's use cases wrote, outbox unchanged by reads.

## Adding a tool

Only a read tool, only with an ADR 0005 amendment: a typed input struct
(snake_case `json` tags + `jsonschema:"..."` on every field), a `Deps` method
calling an existing read use case, registration in `registerTools` with the
read-only annotations, errors through `mapError`, `wantTools` and `maxTools`
in `governance_test.go` updated, and the golden regenerated with
`go test ./internal/adapters/inbound/mcp -run TestToolRegistryGolden -update`.
