# ADR 0005: MCP server adoption (read-only, unauthenticated, additive)

## Status

Accepted (2026-10-08).

## Context

The fleet exposes every bounded context to `warehouse-ops-agent` (and any
other MCP client) through a second, independent deployable binary that serves
the Model Context Protocol over Streamable HTTP, alongside the REST API and
never bundled into the same process (warehouse-planning ADR 0008 and
product-master ADR 0005 record the same decision for those contexts).
inbound-receiving owns the inbound dock workflow (ASNs, dock appointments,
receipts and their discrepancies) that agents ask about constantly ("is the
8:00 truck at door 3 checked in?", "which receipts closed short today?"), while
every change to that data must leave as a versioned CloudEvent through the
transactional outbox (ADR 0004) so the downstream contexts stay consistent.

## Decision

1. `cmd/mcp` is a **second composition root**, independent of `cmd/api`: it
   wires the SAME read use cases (`usecases.GetAsn`, `ListAsns`,
   `GetAppointment`, `ListAppointments`, `GetReceipt`, `ListReceipts`,
   `ListDocks`) to the SAME repositories (in-memory when `DATABASE_URL` is
   unset, Postgres otherwise) through a dedicated inbound adapter
   (`internal/adapters/inbound/mcp`) instead of the REST router. It is built on
   the official SDK (`github.com/modelcontextprotocol/go-sdk`), Streamable HTTP
   only (no stdio, no SSE), listens on `MCP_ADDR` (default `:8090`), serves the
   MCP endpoint at both `/` and `/mcp`, and answers `GET /healthz` for the
   probes.
2. The surface is **read-only: 7 tools**, each backed by an existing read use
   case and returning the REST body with snake_case names:
   `get_asn`, `list_asns` (filter `state`), `get_appointment`,
   `list_appointments` (filters `door`, `state`, `from`, `to`), `get_receipt`,
   `list_receipts` (filters `asn_number`, `state`) and `list_docks` (no
   arguments; the inbound dock doors of the local copy of ADR 0003 and the
   mode it runs in). Lists page with the opaque cursor of the REST API
   (`limit` 1..500, default 100, `next_cursor` absent on the last page).
   Errors are tool results (`isError: true`) whose text starts with the REST
   problem slug (`asn-not-found`, `appointment-not-found`, `receipt-not-found`,
   `invalid-asn-number`, `invalid-appointment-id`, `invalid-receipt-id`,
   `invalid-door-code`, `invalid-query`); anything untyped is logged and
   reported as a generic `internal-error`.
3. **No write tool.** Registering and cancelling ASNs, booking, checking in
   and cancelling appointments, and opening, receiving into and closing
   receipts stay REST-only: those are the operations whose events other
   contexts consume, whose idempotency is keyed by the `Idempotency-Key`
   header, and whose version guard is the `If-Match` header; an
   agent-initiated write path would be a second front door to the outbox with
   no reviewed use. `cmd/mcp` wires no write use case at all, so it cannot
   insert into the outbox. `internal/adapters/inbound/mcp/governance_test.go`
   fails the build if any advertised tool name contains a write verb
   (`register`, `book`, `check`, `cancel`, `open`, `receive`, `close`,
   `create`, `update`, `delete`, `set`), if a tool is not annotated read-only,
   or if the surface exceeds the 7-tool budget; the tool-registry golden
   (`testdata/tool_registry.golden.json`) pins names, descriptions,
   annotations and input/output schemas byte for byte.
4. **No authentication of any kind**, matching the fleet-wide decision
   (reverted 2026-09-11) that REST and MCP are both unauthenticated; access
   control is the in-cluster `ClusterIP` boundary.
   `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware appears.
5. `cmd/mcp` **never starts the outbox relay, never runs the local-copy
   consumers and never dials Kafka**: it reads the same Postgres database
   `cmd/api` writes and has no `EVENT_PUBLISHER`, `KAFKA_BROKERS` or consumer
   group. The one mode-like variable it reads is `DOCK_DOOR_MODE`, only so
   `list_docks` reports the same `mode` as `GET /docks` (permissive or
   kafka); it starts nothing. It runs the idempotent embedded migrations on
   boot (through `MIGRATIONS_DATABASE_URL` when set, like `cmd/api`), so it
   can start against a fresh database; golang-migrate's advisory lock makes
   concurrent starts safe.
6. `TestMCPAdapterDependencyRule` keeps the adapter additive: it depends only
   on the application and domain layers, and nothing else imports it.
7. Packaging: the image (`Dockerfile`) builds every `cmd/*` binary, so
   `/app/mcp` ships next to `/app/api`; the chart
   (`charts/inbound-receiving`) renders an `mcp` Deployment + ClusterIP
   Service (component `mcp`) only when `mcp.enabled=true` (default false), and
   the chart selector test proves each Service selects exactly one
   Deployment. No HPA for mcp: the SDK keeps per-process session state.

## Consequences

- An agent can answer inbound-dock questions without a REST client, and can
  never change an ASN, appointment or receipt, or emit an event, by doing so.
- Adding a tool (or any write tool) is a reviewed act: a new ADR or an
  amendment of this one, the golden updated with `-update`, and the budget in
  `governance_test.go` raised in the same change.
- Two binaries now read the inbound tables; the mcp one holds no write
  credentials beyond what the shared `DATABASE_URL` grants. A read-only
  database role for it is possible later without code changes.
- `list_receipts` and `get_receipt` show discrepancies as the receipt stands
  (same as the REST body): provisional while the receipt is Open, final once
  Closed.

## Alternatives considered and rejected

- **Mirror every REST operation as a tool (read + write)**: rejected; see
  point 3. Writes stay where the outbox, the idempotency keys, the version
  guard and the API contract are reviewed.
- **Bundle MCP into `cmd/api`'s HTTP server**: couples the two surfaces'
  scaling and failure domains; the fleet convention is two deployables.
- **Bearer-token auth on MCP only**: rejected fleet-wide in the 2026-09-11
  revert.
