---
id: intro
title: Introduction
slug: /intro
sidebar_position: 1
---

# Inbound Receiving

`inbound-receiving` is a **Supporting** bounded context in the `warehouse-systems`
fleet (GitHub org `IQVO`). It answers one question:

> *What was announced, when does the truck arrive, and what actually came off it?*

It owns the inbound dock workflow up to, and not including, stock placement:
the supplier's **advance ship notice** (ASN), the carrier's booked **dock
appointment** and the counted **receipt** with the discrepancies found when it
closes. It decides nothing about where received goods go: Good lines are handed
to `inventory-storage` as events and stow stays an RF action there
([ADR 0003](/docs/adr/0003-local-copies-and-handover)).

## Where it sits

| Property | Value |
| --- | --- |
| Subdomain classification | Supporting ([ADR 0001](/docs/adr/0001-inbound-receiving-bounded-context)) |
| Tier | `wms` (CloudEvents type prefix `com.warehouse.wms.inbound-receiving.*`; the fourth `wms` context after `facility-layout`, `inventory-storage` and `product-master`) |
| Language / style | Go backend, hexagonal architecture (ports and adapters) |
| Deployables | Four Go binaries in one image: `cmd/api` (REST `:8080`, outbox relay, local-copy consumers), `cmd/mcp` (read-only MCP `:8090`, [ADR 0005](/docs/adr/0005-mcp-server-adoption)), `cmd/inbound-projector` and `cmd/inbound-reports` (analytics read side, admin `:8091` and reports `:8092`, [ADR 0006](/docs/adr/0006-analytics-read-side)); plus the `web/` micro-frontend remote `inbound_mfe` |
| Inbound adapters | REST, MCP (Streamable HTTP), two Kafka consumers that feed local copies (`known_skus`, `dock_doors`) and the analytics-topic consumer of the projector |
| Outbound adapters | Postgres (or in-memory without `DATABASE_URL`), the Kafka outbox relay, and a separate analytical Postgres (`ANALYTICS_DATABASE_URL`) for the read side |
| Integration | Kafka CloudEvents 1.0 (structured mode), transactional outbox for publishing to `warehouse.inbound-receiving.events` and, under the same CloudEvents id, to the internal analytics topic |
| Auth | None on REST or MCP (fleet-wide revert of 2026-09-11) |

:::note Study project
Like the rest of the `warehouse-systems` fleet, this is a personal study
project exploring Domain-Driven Design, hexagonal architecture and AI-agent
harness engineering. It is not production software and carries no support
guarantee.
:::

:::info What ships on `develop`
Besides the REST service, `develop` ships the read-only MCP server
(`cmd/mcp`, ADR 0005), the analytics read side (`cmd/inbound-projector` and
`cmd/inbound-reports` over a separate analytical database, ADR 0006), the
`web/` remote `inbound_mfe` that `warehouse-console` mounts, one container
image (`Dockerfile`) and the Helm chart `charts/inbound-receiving`.
`warehouse-infra` deploys all of it on its `develop`. The pages of this site
describe what the code on `develop` does today and label every edge that is
not built *planned*.
:::

## What this context owns

Three aggregates, each its own consistency boundary
([ADR 0002](/docs/adr/0002-aggregates-and-invariants)):

- **Asn** (identity: ASN number): the supplier's announcement. Lines
  `[{lineNo, sku, expectedQty}]`, numbered `1..n`, unique SKUs. State
  `Registered -> Receiving -> Closed`, or `Registered -> Cancelled`.
- **DockAppointment** (identity: `appt-<uuid>`): a carrier's half-open door
  window of at most four hours covering one or more ASNs. State
  `Booked -> CheckedIn -> Completed`, or `Booked -> Cancelled`. No two active
  appointments may overlap on one door (the `Schedule` domain service).
- **Receipt** (identity: `rcpt-<uuid>`): the counted receiving of one ASN,
  opened against a snapshot of the ASN. Per line it counts Good and Damaged
  units; closing it computes the `Short`, `Over` and `Damaged` discrepancies.

They reference each other by id, never by object. Every aggregate carries a
`version` that starts at 1, grows by one per accepted change and guards the
repository write.

It does **not** own stock or placement (`inventory-storage`), yard and trailer
management, returns, putaway directives, quality inspection and quarantine,
supplier master data, purchase orders or EDI parsing (ADR 0001, "Explicitly out
of scope").

## Where to go next

- [Bounded context](/docs/overview/context): purpose, context map, the no-live-lookup rule.
- [Aggregates](/docs/overview/aggregates): the three aggregates and their invariants.
- [Receiving workflow](/docs/overview/receiving-workflow): announce, appoint, receive, reconcile.
- [DDD artifacts](/docs/ddd/ddd-artifacts): the ddd-crew pack (core domain chart, canvases, context map, EventStorming, class, ER and sequence diagrams).
- [Downstream consumers](/docs/ecosystem/downstream-consumers) and the [upstream contracts](/docs/ecosystem/upstream-contracts).
- [Runtime](/docs/overview/runtime): the four binaries, configuration, the outbox, the analytics read side and the web remote.
- [API reference](/docs/api-reference): REST (generated from `apis/openapi.yaml`, including the reports endpoints), MCP tools and the event catalogue.
- [Architecture decision records](/docs/adr): the index with each ADR's status.
