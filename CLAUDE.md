# CLAUDE.md — inbound-receiving

`inbound-receiving` is a Supporting bounded context (`wms` tier) of the
`warehouse-systems` fleet (GitHub org `IQVO`, Go module
`github.com/claudioed/inbound-receiving`). It owns the inbound dock workflow up
to, and not including, stock placement: the supplier's advance ship notice
(`Asn`), the carrier's booked door window (`DockAppointment`) and the counted
`Receipt` with the `Short` / `Over` / `Damaged` discrepancies computed when it
closes. Good units are handed to `inventory-storage` as events; stow stays
there. It ships four binaries (`cmd/api` REST + outbox relay + two local-copy
consumers, `cmd/mcp` read-only MCP, `cmd/inbound-projector` and
`cmd/inbound-reports` for the analytics read side) and the `web/`
micro-frontend remote `inbound_mfe`.

## Hard rules (each one is fitness-tested or cost an incident)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal
   but domain; `internal/application` only on domain and application; inbound
   and outbound adapters never depend on each other; only `cmd/` wires layers.
   The MCP adapter is imported by `cmd/mcp` only. `make arch-test` enforces it.
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or consumed
   (structured mode, no flat envelope, no envelope toggle). Build/decode ONLY
   through `internal/adapters/kafka/cloudevents/`; `type` is
   `com.warehouse.wms.inbound-receiving.<asn|dockappointment|receipt>.<EventName>`;
   a breaking payload change is a new `.v2` type. Catalogue:
   `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`; payloads:
   `apis/asyncapi.yaml`; `TestEventCatalogueMatchesContract` checks both.
3. **Events leave through the transactional outbox only.** Aggregate saves and
   the encoded CloudEvents commit in one `ports.UnitOfWork`; the relay in
   `cmd/api` publishes. `FanoutEncoder` writes every event twice (integration
   topic + `warehouse.inbound-receiving.analytics`) under ONE CloudEvents id
   (ADR 0006). Never write to Kafka from a handler or use case.
4. **Three aggregates, three consistency boundaries** (ADR 0002). They
   reference each other by id; every save is guarded by `version`
   (409 `concurrent-modification`, 412 `version-mismatch` on a stale
   `If-Match`). `CloseReceipt` is the one use case that saves receipt, ASN and
   appointment in a single unit of work.
5. **No outbound calls to sibling contexts, ever.** Product SKUs and inbound
   dock doors are LOCAL COPIES fed from `warehouse.product-master.events` and
   `warehouse.facility.events` (ADR 0003), gated by `PRODUCT_MODE` /
   `DOCK_DOOR_MODE` (`permissive` default, `kafka` enforces). Consumer group ids
   come from env (`PRODUCT_CONSUMER_GROUP`, `DOCK_DOOR_CONSUMER_GROUP`,
   `ANALYTICS_CONSUMER_GROUP`), never literals
   (`TestKafkaConsumerGroupNeverHardcodedInline`).
6. **Resource-creating POSTs require `Idempotency-Key`** (`/asns`,
   `/appointments`, `/receipts`, `/receipts/{id}/lines`); action POSTs honour
   it when sent. Same key + different request = 422 `idempotency-key-reused`.
7. **The analytics read side never touches the OLTP database.**
   `cmd/inbound-projector` is the only writer of the analytical database
   (`ANALYTICS_DATABASE_URL`); `cmd/inbound-reports` reads it through a
   read-only pool. Poison analytics messages go to
   `warehouse.inbound-receiving.analytics.dlq`.
8. **No auth layer on REST, MCP or reports** (fleet-wide revert 2026-09-11):
   never add bearer/JWT/API-key middleware
   (`TestNoAuthMiddlewareReintroduced`). MCP is read-only (seven tools).
9. **Integration tests use testcontainers only**: no `DATABASE_URL`, no
   hardcoded broker, no `t.Skip` gate (fitness-tested).
10. **An ADR comes first** for a new cross-context contract, a new aggregate,
    a new consumed topic or any auth. ADRs live in `docs/adr/` (second
    Docusaurus instance); add each to `docs/adr/index.md` with the body's own
    Status. ADR bodies are immutable after acceptance.

## Commands

```bash
make check-fast   # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check        # fmt-check vet build lint test (lefthook pre-push)
make check-all    # check + coverage (90% gate on domain+application) + arch-test + bdd
make integration  # real Postgres/Kafka via testcontainers (needs Docker)
make mutation     # gremlins on each internal/domain package (CI mutation-fast, thresholds 99/99)
make guide-lint   # these guides: skills load, references resolve, context budget
```

Run locally: `go run ./cmd/api` (REST `:8080`; in-memory adapters without
`DATABASE_URL`, outbox drained to the log without `EVENT_PUBLISHER=kafka`);
`go run ./cmd/mcp` (`:8090`, its own empty store without `DATABASE_URL`);
`cmd/inbound-projector` (admin `:8091`) and `cmd/inbound-reports` (`:8092`)
need `ANALYTICS_DATABASE_URL`. Every env var: `docs/docs/operations/configuration.md`.

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`: ubiquitous language, aggregates, events,
  use cases. `.claude/rules/rest-api.md`: endpoints and error slugs.
  `.claude/rules/integration-events.md`: published/consumed events, outbox,
  local copies. `.claude/rules/mcp.md`: the MCP adapter.
- `.claude/rules/fleet/*.md`: fleet-wide rules copied from the harness
  template; never edit them here.
- `docs/adr/`: decisions. `apis/openapi.yaml`, `apis/asyncapi.yaml`: contracts.
  `docs/docs/`: the site (operations runbook, observability, troubleshooting,
  testing). `HARNESS.md`: what each sensor runs and why.

harness-template: v4
(hooks, guide-lint).

## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching
paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `internal/domain/**`, `internal/application/**`, `features/**` | `.claude/rules/domain-model.md` |
| `internal/adapters/**/kafka/**`, `apis/asyncapi*` | `.claude/rules/integration-events.md` |
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml` | `.claude/rules/rest-api.md` |
| `internal/adapters/inbound/mcp/**`, `cmd/mcp/**` | `.claude/rules/mcp.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode)
block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to
generated files, and feed gofmt/vet findings back after each edit. Before
saying "done" run `make check-fast`; the full gate is `make check-all`.
`HARNESS_OFF=1` disables the hooks when debugging the harness itself.
