# ADR 0018: Agent authority levels and complete MCP surface

## Status

Accepted (2026-11-02). Amends [ADR 0005](./0005-mcp-server-adoption.md) (the
read-only MCP charter — unchanged: no write tool, ever) and
[ADR 0016](./0016-a2a-inbound-adapter.md) (the read-only A2A agent, which
gains only a tag). Implements the fleet's Agentic Standard v2
(AS-1, AS-3, AS-4) as pinned in the warehouse-docs decision page and the
`agent-authority` fleet rule; reference implementation workforce-management
(PR #235, ADR 0041).

## Context

The fleet is standardising what an agent call declares about itself so a
caller (model host, ops-agent, human approving a delegation) can gate it
without inspecting behaviour:

- **AS-1 authority levels.** Every MCP tool declares a level in its
  `mcp.Tool.Meta` under the key `com.warehouse/authority`; every A2A skill
  carries the same level as an `authority:Ln` tag. Levels are `L1` (pure read
  of this context's own state), `L2` (computed recommendation, changes
  nothing), `L3` (state change; caller accountable) and `L4` (an ADR-granted
  envelope above L3 — none here).
- **AS-4 stateless MCP transport.** The Streamable HTTP handler stops issuing
  `Mcp-Session-Id`, so the 2026-07-28 protocol revision's per-request
  handshake (`server/discover`) is answered while legacy `initialize` clients
  keep working.
- **AS-3 complete surface.** An MCP server that offers only tools is half an
  agent surface: it also offers at least one resource (scoped read model, no
  table dumps) and one prompt (the operator SOP).

inbound-receiving's MCP charter is **read-only** (ADR 0005): seven read
tools, no write use case wired, governance tests that fail the build on a
write-verb tool name. F3 (ASN auto-close over MCP) is explicitly deferred
fleet-wide.

## Decision

1. **Every tool is `L1`.** All seven tools (`get_asn`, `list_asns`,
   `get_appointment`, `list_appointments`, `get_receipt`, `list_receipts`,
   `list_docks`) are pure reads of this context's own state; none computes a
   recommendation, so none is `L2`, and the charter bars writes, so none is
   `L3`/`L4`. Each registers `Meta: authorityMeta(authorityL1)` through the
   fleet-pinned helper (`authorityMetaKey` + `authorityL1` constants), which
   `tools/list` returns verbatim in `_meta`. Only the `L1` constant exists:
   the `unused` sensor rejects constants nothing references, and an `L2`/`L3`
   tool cannot be registered here; the constant is added together with the
   tool on the day an ADR 0005 amendment ever charters one.
2. **Both A2A skills are `authority:L1`.** `asn_receiving_status` and
   `dock_schedule` front composed reads (`GetAsn`+`ListReceipts`,
   `ListAppointments`), so they carry the same level as the tools they front.
   They keep their `read` tag; no `write` tag exists in this context.
3. **The handler is stateless.** `Handler` builds the handler with
   `mcp.StreamableHTTPOptions{Stateless: true}`. Nothing in this adapter
   holds cross-call session state (it never
   did: every call is one use-case read). Raw-POST tests pin the wire facts:
   `server/discover` advertises `2026-07-28` with no session id,
   `tools/list` carries the authority key, and a legacy `initialize`
   (2025-06-18) still answers 200.
4. **One resource.** `warehouse://inbound/receipts/{receiptId}` — exactly one
   receipt (the same `GetReceipt` read model and JSON as `get_receipt`),
   never a dump of a table. A malformed or non-matching URI is rejected, not
   defaulted; an unknown id surfaces the `receipt-not-found` slug. Resources
   are reads by nature and need no tool Meta.
5. **One prompt.** `receive_inbound_shipment` — the operator SOP for working
   an inbound shipment: read the ASN, find the dock window, follow the
   receipts and their discrepancies, and escalate closure, claims and
   cancellations to humans (this surface is read-only; the SOP names tools
   and never performs a write).
6. **The registry golden carries the level.**
   `testdata/tool_registry.golden.json` is regenerated and now pins
   `_meta: {"com.warehouse/authority": "L1"}` on every tool, so a level
   change is a reviewed golden update.
7. **A static fitness sensor enforces it.** The harness template's
   `internal/architecture/mcp_authority_fitness_test.go` is copied verbatim
   (its only addition is the `mcpAdapterDir` constant, which this repo's
   `mcp_optional_inputs_test.go` variant does not define): every `mcp.Tool`
   literal must declare a readable level, a write tool must be `L3`+ (an ADR
   comment for `L4`), every `a2a.AgentSkill` literal needs exactly one
   `authority:Ln` tag, and tools-only servers fail until a resource and a
   prompt exist (no `.agent-surface-pending` marker here: the surface is
   complete).

## Consequences

- A model host can gate every tool and skill of this context as a pure read
  (`L1`); nothing on the agent surface changes state, matching the charter.
- Clients that rely on `Mcp-Session-Id` see it disappear; no caller of this
  server did (its clients negotiate per-request or re-initialize), and the
  legacy `initialize` path keeps answering, verified by the raw-POST tests
  and the existing Streamable HTTP integration tests.
- Adding any tool, resource, prompt, skill or authority change now trips the
  golden, the governance tests and the authority sensor — all three must move
  together, in one reviewed PR.
- Writes stay where ADR 0005 put them: REST, idempotency key, version guard,
  transactional outbox, CloudEvents. If the fleet ever charters F3 (ASN
  auto-close), that is a new ADR amending 0005, an `L3` declaration with
  `initiated_by` attribution, and a governance/golden update — not a quiet
  edit here.
