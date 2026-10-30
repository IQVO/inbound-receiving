# ADR 0016: A2A inbound adapter (read-only, unauthenticated, additive)

## Status

Accepted (2026-10-29). Follows [ADR 0005](./0005-mcp-server-adoption.md) (the
MCP adapter this one is modelled on), [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(CloudEvents on every Kafka message) and
[ADR 0011](./0011-postgres-and-kafka-are-the-only-runtime.md) (Postgres and
Kafka are the only runtime). Mirrors the fleet's A2A rollout
(workforce-management ADR 0039, wes-work-planning ADR 0046,
process-path-management ADR 0039).

## Context

The fleet is adopting the Agent2Agent protocol so that a bounded context can
**delegate** a goal to another context's agent. inbound-receiving owns the
inbound dock workflow (ASNs, dock appointments, receipts and their
discrepancies) and already exposes it to agents over MCP (seven read-only
tools) and to services over REST.

Three transports now carry three different things and must not overlap:

| Transport | Carries |
|---|---|
| Kafka + CloudEvents | **facts** between contexts (ADR 0004) |
| MCP | an agent's **tools** |
| A2A | **delegation** of a goal to another context's agent |

Two repository rules constrain the design:

- **CLAUDE.md rule 5: no outbound calls to sibling contexts, ever.** That
  concerns outbound clients. An inbound A2A server is allowed; an outbound A2A
  client is not.
- **The MCP surface is entirely read-only** ([ADR 0005](./0005-mcp-server-adoption.md),
  CLAUDE.md rule 8): registering and cancelling ASNs, booking, checking in and
  cancelling appointments, and opening, receiving into and closing receipts are
  REST-only, because their events feed other contexts and their idempotency and
  version guards live on the REST contract.

## Decision

Add `internal/adapters/inbound/a2a`, a third driving adapter over the **same**
read use cases (`GetAsn`, `ListReceipts`, `ListAppointments`) the REST and MCP
adapters use, built on the official `a2aproject/a2a-go/v2` SDK (pinned
`v2.7.0`) and served by a new `cmd/a2a` binary (JSON-RPC 2.0 over HTTP, port
**8093**, the Agent Card at `/.well-known/agent-card.json`, the endpoint
mounted at `/` and `/a2a`, `GET /healthz`).

### The agent is read-only, like the MCP charter

An A2A skill may change state only if the context's MCP surface already offers
that write. It offers none, so this agent offers none: it never widens what the
context exposes. Asking this agent for a write (`register_asn`, `open_receipt`,
`close_receipt`, ...) is an `unknown-skill` problem and writes nothing
(`TestWireOffersNoWriteSkill`). Two fitness tests enforce it:
`TestA2AAdapterIsReadOnlyAndHasNoOutboundClient` allows the adapter to name
only the three read use cases (an allowlist, so a new write use case is refused
however it is named) and refuses the unit of work, the clock, the outbox, a
repository `Save` and an outbound A2A client;
`TestA2AReadOnlyBinaryWiresNoWriteUseCase` applies the same allowlist to
`cmd/a2a`.

### Two curated skills, not one per endpoint

| Skill | Kind | What it does | Derived from |
|---|---|---|---|
| `asn_receiving_status` | read | one ASN by `asnNumber` (supplier, expected arrival, state, lines) together with the receipts counted against it (state, door, per-line good and damaged counts) and their Short, Over or Damaged discrepancies, provisional while a receipt is Open and final once Closed | MCP `get_asn` + `list_receipts` (`asn_number`) |
| `dock_schedule` | read | the carrier dock appointments in window-start order, optionally by `door`, `state` and a `from`/`to` range (RFC 3339), at most `limit` entries (1..100, default 50), with `truncated` when more exist | MCP `list_appointments` |

Both are goal-shaped: "where does this ASN stand in the dock workflow" and
"what is booked at the docks". Not skills, on purpose: `get_appointment`,
`get_receipt` and the paged `list_asns` (a delegating agent reaches them
through the receipts and the schedule), and `list_docks`, which reports the
local copy of the facility-layout doors that the delegating agent's own
context owns (ADR 0003). Two skills is the fleet's ceiling.

`dock_schedule` has no `cursor`: it is a bounded view, never a feed. A caller
that sees `truncated: true` narrows the door, state or time range. A2A
delegates a goal; it does not synchronise data.

### Message-only, answered in-band

Both skills are transactional request/response reads, so the agent is
message-only (no Task lifecycle). Domain rejections and malformed requests are
**answered** as RFC 7807-shaped `problem`s inside the message envelope, never
raised as JSON-RPC transport errors, using the REST adapter's type namespace
(`https://errors.inbound-receiving.warehouse-systems.dev/<slug>`) and the same
slugs and statuses for the outcomes a read can have:

| Status | Problem type | When |
|---|---|---|
| `404` | `asn-not-found` | no ASN has that number (same type as REST `GET /asns/{asnNumber}`) |
| `400` | `invalid-asn-number` | a malformed `asnNumber` |
| `400` | `invalid-query` | `dock_schedule` filters the use case refuses: unknown `state`, malformed `door`, `from` or `to` that is not an RFC 3339 instant |
| `400` | `invalid-argument` | missing or blank `asnNumber`, an unknown or mistyped argument, `limit` outside 1..100 |
| `400` | `malformed-skill-request`, `unknown-skill` | the message is not a skill request, or names a skill the card does not list |
| `500` | `internal-error` | anything unrecognised; the answer carries no underlying error text |

REST's `409`, `412`, `422` and `503` classes belong to writes and to
dependencies this read-only agent does not have, so they are not mapped. The
`404` and `500` answers carry adapter-owned detail text: the repository's
internal error text and any infrastructure error text never reach an agent
client (tests assert both). Arguments are camelCase (`asnNumber`), unlike the
snake_case MCP tools; the use cases' error texts already use that spelling
(`asnNumber: ...`) and none names a deprecated alias, so there is nothing to
translate.

### Strict input

Unknown argument names are **rejected**, not ignored (a misspelled `doorr`
must not silently widen the schedule to every door), and data after the JSON
document is rejected. Go's `encoding/json` matches names case-insensitively, so
`DOOR` still reaches `door`; only genuinely unknown names are refused. A text
part holding the same JSON is accepted. An explicit `limit: 0` is refused
rather than read as the default.

### Facts still travel on Kafka; nothing calls out

A2A never becomes a data-sync channel, and this agent has nothing to publish:
it writes no outbox row, so `cmd/a2a` has no encoder, no `UnitOfWork`, no
`EVENT_PUBLISHER` and no Kafka dependency (like `cmd/mcp`), runs no local-copy
consumer and never starts the relay (only `cmd/api` drains `outbox_events`,
ADR 0004 and ADR 0006). Every change to an ASN, appointment or receipt still
leaves as a CloudEvent through the REST write path. The adapter contains no
outbound A2A client and calls no other context.

### Unauthenticated, like REST and MCP

The card declares no security schemes: this surface is unauthenticated by the
same fleet-wide decision as REST and MCP (CLAUDE.md rule 8,
`TestNoAuthMiddlewareReintroduced`). Schemes are added to the card, not a
redesign, if that decision is ever reversed.

## Consequences

- A fourth binary in the same image (`/app/a2a`; the Dockerfile builds every
  `cmd/*`) and an `a2a.*` chart block (`<release>-a2a`, component `a2a`,
  default **off**), exposed through Kong at `/api/a2a/inbound-receiving`
  rewritten to `/`. `A2A_AGENT_URL` (absolute `http(s)` URL advertised on the
  card) is mandatory at boot; the chart defaults it to the in-cluster Service
  DNS and `a2a.agentUrl` overrides it.
- The binary reads the same `DATABASE_URL` as `cmd/api` and, like `cmd/mcp`,
  applies the idempotent embedded migrations at boot. It reads no Kafka
  variable and, unlike `cmd/mcp`, no `DOCK_DOOR_MODE`: it offers no
  `list_docks`.
- Delegating *to* this agent does not change who decides: this context reports
  where an ASN stands and what is booked, and never decides stow or putaway.
- Port 8093 is the fleet's A2A port (8090 is MCP, 8091 the projector admin and
  8092 the reports API in this chart).
- Not in this decision: any write skill (the MCP charter stays the ceiling),
  a skill over `list_docks`, carton-level detail, paging of the schedule, and
  an outbound A2A client.
