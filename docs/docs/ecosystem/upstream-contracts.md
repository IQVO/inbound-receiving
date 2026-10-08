---
id: upstream-contracts
title: Upstream contracts
sidebar_position: 2
---

# Upstream contracts

`inbound-receiving` consumes **two** upstream contracts, both to build local
copies so it never calls a sibling at request time
([ADR 0003](/docs/adr/0003-local-copies-and-handover)). Each was read from the
producer's `apis/asyncapi.yaml` on `origin/develop` on 2026-10-08 and no contract
contradicted the design. Messages are matched on the **full** CloudEvents `type`;
unknown types are ignored.

## product-master: `ProductRegistered`

| Item | Value |
| --- | --- |
| Topic | `warehouse.product-master.events` (`ProductTopic` in `internal/adapters/inbound/kafka/product_consumer.go`) |
| Type | `com.warehouse.wms.product-master.product.ProductRegistered` (`TypeProductRegistered`), byte-identical to product-master's AsyncAPI |
| Consumer group | env `PRODUCT_CONSUMER_GROUP`; the consumer runs only when `PRODUCT_MODE=kafka`; an unset group with that mode is a boot error (`loadConfig`) |
| Fields used | `data.sku` only (`{sku, description, version}` are published; subject and key = the SKU) |
| Local copy | `known_skus` (`sku`, `first_seen_at`): existence only, so a replayed or reordered message is harmless |
| Idempotency | claim of the CloudEvents `id` in `processed_events` under consumer `product-registry`, in the same unit of work as the upsert |
| Effect | `RegisterAsn` rejects a SKU not in `known_skus` with `422 unknown-sku`, only in `kafka` mode |
| Invalid input | not a CloudEvent, malformed payload, invalid SKU: WARN and commit past |
| Transient failure | retry the same message, 200 ms doubling to 5 s, until it succeeds; no dead-letter topic |

## facility-layout: dock doors

| Item | Value |
| --- | --- |
| Topic | `warehouse.facility.events` (`FacilityTopic` in `dock_door_consumer.go`) |
| Types | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` and `...LocationSlotDecommissioned`, byte-identical to facility-layout's AsyncAPI |
| Consumer group | env `DOCK_DOOR_CONSUMER_GROUP`; the consumer runs only when `DOCK_DOOR_MODE=kafka`; an unset group with that mode is a boot error |
| Fields used | `locationCode`, `role`, `dockFlow` (camelCase: facility-layout carries its domain event struct verbatim); key = `locationCode` |
| Local copy | `dock_doors` (`door_code`, `dock_flow`, `updated_at`) |
| Rule | `LocationSlotRegistered` with `role=Dock` and `dockFlow` `Inbound` or `Both` upserts a row. Any other registration (including an absent `role`, which means `Storage`, and `Dock` with `dockFlow=Outbound`) is ignored. `LocationSlotDecommissioned` deletes the row for that code |
| Idempotency | claim of the CloudEvents `id` under consumer `dock-door-registry`, in the same unit of work |
| Effect | `BookAppointment` rejects a door not in `dock_doors` with `422 unknown-dock-door`, only in `kafka` mode; `GET /docks` lists the known doors |
| Invalid input | WARN and commit past |

## Modes

| Copy | Mode env | Group env | Default |
| --- | --- | --- | --- |
| `known_skus` | `PRODUCT_MODE` = `kafka` or `permissive` | `PRODUCT_CONSUMER_GROUP` | `permissive` |
| `dock_doors` | `DOCK_DOOR_MODE` = `kafka` or `permissive` | `DOCK_DOOR_CONSUMER_GROUP` | `permissive` |

`permissive` does not consult the copy, so every SKU and door code is accepted
(fail-open) and the consumer is not started. The first start under a new group
replays the topic from the first offset; readiness does not wait for it, which is
why the cluster is meant to flip to `kafka` only after the copies have replayed.
Both copies are eventually consistent: a SKU registered seconds ago may be
rejected in `kafka` mode until its event is consumed, and the operator retries.

## Not consumed

Nothing else. `inbound-receiving` has no outbound HTTP client and reads no other
topic (ADR 0001, "No live cross-context lookup, in either direction").

## ADR 0003 vs the code

ADR 0003 says the `known_skus` row "keeps the highest `version`". The schema and
`ApplyProductRegistered` only store existence (`known_skus` has no `version`
column, `SkuStore.Upsert(ctx, sku)` takes only the SKU, and the insert is
`ON CONFLICT (sku) DO NOTHING`). Because only existence matters, replays and
reordering are harmless either way; this page follows the code.
