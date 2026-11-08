---
id: upstream-contracts
title: Upstream contracts
sidebar_position: 2
---

# Upstream contracts

`inbound-receiving` consumes **three** upstream contracts (the third, dock-scheduling, since
[ADR 0019](/docs/adr/0019-dockappointment-moves-to-dock-scheduling)), all to build local
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
| Effect | `GET /docks` lists the known doors; `OpenReceipt` keeps the door code of its appointment |
| Invalid input | WARN and commit past |

## dock-scheduling: appointments

| Item | Value |
| --- | --- |
| Topic | `warehouse.dock-scheduling.events` (`DockSchedulingTopic` in `dock_appointment_consumer.go`) |
| Types | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked` and `...DockAppointmentCancelled` only; every other type on the topic (`Rescheduled`, `NoShow`, closures) is committed past. Pinned from the contract; re-check against dock-scheduling's AsyncAPI when it lands on `develop` |
| Consumer group | env `DOCK_APPOINTMENT_CONSUMER_GROUP`; the consumer always runs; unset is a boot error |
| Fields used | `appointment_id`, `direction` (only `INBOUND` is copied), `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers[]`, `version` (snake_case; key = `appointment_id`) |
| Local copy | `appointments` + `appointment_asns` |
| Rule | an event applies only when its `version` is greater than the stored one (legacy rows are version 0, so any dock-scheduling version wins); a later Booked never revives a cancelled appointment |
| Idempotency | claim of the CloudEvents `id` under consumer `dock-appointment-copy`, in the same unit of work as the upsert |
| No loop | applying an event writes nothing to the outbox |
| Effect | `OpenReceipt` with an `appointmentId` requires it to exist (`422 unknown-appointment`), to cover the ASN (`422 asn-not-on-appointment`) and not be cancelled (`409 appointment-cancelled`); deprecated `GET /appointments` reads the copy |
| Invalid input | WARN and commit past; transient failures retry |

## Modes

| Copy | Mode env | Group env | Default |
| --- | --- | --- | --- |
| `known_skus` | `PRODUCT_MODE` = `kafka` (only value) | `PRODUCT_CONSUMER_GROUP` (required) | `kafka` |
| `dock_doors` | `DOCK_DOOR_MODE` = `kafka` (only value) | `DOCK_DOOR_CONSUMER_GROUP` (required) | `kafka` |
| `appointments` | none (always on) | `DOCK_APPOINTMENT_CONSUMER_GROUP` (required) | n/a |

The consumers always run and the copies are always consulted: `permissive` and
`off` are boot errors since [ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime).
The first start under a new group replays the topic from the first offset;
readiness does not wait for it, so writes can answer `422` until the copies
have replayed.
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
