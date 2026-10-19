---
id: tools
title: MCP tools
sidebar_label: MCP tools
---

# MCP tools

`cmd/mcp` is a separate deployable that serves the
[Model Context Protocol](https://modelcontextprotocol.io) over **Streamable
HTTP only** (no stdio, no SSE), mounted at both `/mcp` and `/`, with
`GET /healthz` for probes. It is **read-only**: no write use case is wired, so
it can never insert into the outbox, and it never dials Kafka
([ADR 0005](/docs/adr/0005-mcp-server-adoption)). There is **no
authentication**; access control is the in-cluster ClusterIP Service
(`<release>-mcp:8090`, enabled by `mcp.enabled`).

The tools call the same read use cases as REST (`GetAsn`, `ListAsns`,
`GetAppointment`, `ListAppointments`, `GetReceipt`, `ListReceipts`,
`ListDocks`) over the same OLTP database. The list below was captured with a
live `tools/list` against `cmd/mcp` built from this repository
(go-sdk `v1.8.0`); the registry is also pinned by
`internal/adapters/inbound/mcp/testdata/tool_registry.golden.json`.

## Tool list

Every tool carries the annotations `readOnlyHint: true`,
`idempotentHint: true`, `openWorldHint: false`.

| Tool | Access | Input fields | Output fields |
|---|---|---|---|
| `get_asn` | read-only | `asn_number` (string, **required**, e.g. `ASN-1001`) | `asn_number`, `supplier_ref`, `expected_arrival` (omitted when none), `state` (`Registered`, `Receiving`, `Closed`, `Cancelled`), `lines` (`line_no`, `sku`, `expected_qty`), `version` |
| `list_asns` | read-only | `limit` (integer, 1..500; omitted or 0 = 100), `cursor` (string, the previous page's `next_cursor`), `state` (string) | `items` (as `get_asn`), `next_cursor` (absent on the last page); ascending number order |
| `get_appointment` | read-only | `appointment_id` (string, **required**, e.g. `appt-1b4e28ba-...`) | `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end` (UTC), `asn_numbers`, `state` (`Booked`, `CheckedIn`, `Completed`, `Cancelled`), `version` |
| `list_appointments` | read-only | `limit`, `cursor`, `door` (door code), `state`, `from` (RFC 3339: window ends after it), `to` (RFC 3339: window starts before it) | `items`, `next_cursor`; ascending window-start order |
| `get_receipt` | read-only | `receipt_id` (string, **required**, e.g. `rcpt-1b4e28ba-...`) | `receipt_id`, `asn_number`, `appointment_id` and `door_code` (omitted for a walk-in), `state` (`Open`, `Closed`), `lines` (`expected_qty`, `received_good`, `received_damaged`), `opened_at`, `closed_at` (omitted while open), `discrepancies` (kind `Short`, `Over`, `Damaged`; provisional while Open), `version` |
| `list_receipts` | read-only | `limit`, `cursor`, `asn_number`, `state` (`Open` or `Closed`) | `items`, `next_cursor`; ascending id order |
| `list_docks` | read-only | none | `mode` (always `kafka`), `items` (`door_code`, `dock_flow` `Inbound` or `Both`); ascending code order |

## Errors

A failed call returns a tool-level error whose text is
`<slug>: <detail>`, where the slug is the same as the REST problem `type`
suffix (`internal/adapters/inbound/mcp/errors.go`):

| Tool | Slugs |
|---|---|
| `get_asn` | `asn-not-found`, `invalid-asn-number` |
| `get_appointment` | `appointment-not-found`, `invalid-appointment-id` |
| `get_receipt` | `receipt-not-found`, `invalid-receipt-id` |
| `list_asns` | `invalid-query` (bad `limit`, `cursor` or `state`) |
| `list_appointments` | `invalid-query` (bad `limit`, `cursor`, `state` or instant), `invalid-door-code` |
| `list_receipts` | `invalid-query`, `invalid-asn-number` |

## Example session

Captured against a local `cmd/mcp` on an empty Postgres (`MCP_ADDR=:18090`):

```text
POST /mcp  {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",...}}
→ 200, header Mcp-Session-Id: PA7B45A5QKL25L2MWGCXLBO2CH
  result.capabilities = {"logging":{},"tools":{"listChanged":true}}
POST /mcp  {"jsonrpc":"2.0","method":"notifications/initialized"}       (same Mcp-Session-Id)
POST /mcp  {"jsonrpc":"2.0","id":2,"method":"tools/list"}                → the 7 tools above
POST /mcp  {"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_docks","arguments":{}}}
→ result.structuredContent = {"items":[],"mode":"kafka"}
```

Requests must send `Accept: application/json, text/event-stream`; responses
come back as `event: message` / `data: ...` frames.

## Operating notes

- The MCP session state lives in the process, and the Service has no session
  affinity, so the chart keeps `mcp.replicaCount: 1` and has no HPA for it.
- `DATABASE_URL` is required (there is no in-memory store); point it at the
  same database as `cmd/api`.
- Telemetry: spans and `http.server.request.duration` under service name
  `inbound-receiving-mcp`; individual tool calls do not get their own span
  (see [Observability](../operations/observability.md)).
