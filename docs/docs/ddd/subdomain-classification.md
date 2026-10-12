---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
---

# Subdomain classification

## Verdict

**inbound-receiving is a Supporting subdomain in the `wms` tier.**

| Aspect | Value | Source |
|---|---|---|
| Strategic class | **Supporting** | [ADR 0001](/docs/adr/0001-inbound-receiving-bounded-context) "Classification"; fleet table in warehouse-docs |
| Tier / CloudEvents subdomain | `wms` | ADR 0001; every published type is `com.warehouse.wms.inbound-receiving.<entity>.<EventName>` (`internal/adapters/kafka/cloudevents`) |
| Business model | cost reduction / compliance: an accurate receipt and discrepancy record | warehouse-docs subdomain classification |
| Evolution | custom-built (genesis) | warehouse-docs subdomain classification |

## Reasoning

- **Not Generic.** Every warehouse receives goods, but the rules that matter
  here differ per retailer: what counts as a discrepancy (`Short`, `Over`,
  `Damaged`), how long a dock window may be and when check-in is allowed (from
  30 minutes before the window until it ends), whether an ASN line must name a
  known SKU. An off-the-shelf receiving module would have to be bent to these
  rules, and the local copies of product-master's SKUs and facility-layout's
  inbound doors tie it to the fleet's own vocabulary.
- **Not Core.** Nobody chooses a warehouse for how it records a receipt. The
  appointment window and the discrepancy kinds are replaceable policy objects
  (ADR 0001), and the context makes no throughput or allocation decision: the
  differentiating decisions (release, dispatch, reservations) live in the Core
  contexts downstream.
- **What it still must get right.** The receipt is the source of truth for what
  physically arrived; `inventory-storage` books stock from its
  `ReceiptLineReceived` events. That makes correctness (idempotent writes,
  transactional outbox, version guards) important even though the subdomain is
  not differentiating.
- **Tier.** Receiving books units into the WMS stock ledger; it is "what and
  where", not task execution, so its events use the `wms` segment, like
  `facility-layout`, `inventory-storage`, `product-master` and
  `slotting-optimization`.

## Neighbours

Classifications copied from warehouse-docs
`docs/strategic-design/subdomain-classification.md` on `origin/main`; the
relationship column is from this repo's [integration pages](../ecosystem/upstream-contracts.md).

| Context | Class | Tier | Relationship to inbound-receiving |
|---|---|---|---|
| `product-master` | Supporting | `wms` | upstream: `ProductRegistered` on `warehouse.product-master.events` feeds the `known_skus` local copy (`PRODUCT_MODE=kafka`) |
| `facility-layout` | Generic | `wms` | upstream: `LocationSlotRegistered` / `LocationSlotDecommissioned` on `warehouse.facility.events` feed the `dock_doors` local copy (`DOCK_DOOR_MODE=kafka`) |
| `inventory-storage` | Core | `wms` | downstream: consumes `ReceiptLineReceived` and books `Good` units as received stock |
| `warehouse-ops-agent` | Supporting | `wes` | downstream reader: calls the seven read-only MCP tools |
| `warehouse-planning` | Core | `wes` | candidate downstream (`DockAppointmentBooked` as inbound labor demand): planned, not built |

The fleet count in warehouse-docs is five Core, seven Supporting, two Generic
and one Generic/Supporting; inbound-receiving is one of the seven Supporting
contexts.

See also the [Core domain chart](./core-domain-chart.md) and the
[Bounded context canvas](./bounded-context-canvas.md).
