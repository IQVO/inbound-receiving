---
id: upstream-contracts
title: Upstream contracts
sidebar_position: 2
---

# Upstream contracts

`inbound-receiving` consumes **four** upstream contracts from three producers
(product-master's products and pack levels, facility-layout's dock doors and,
since [ADR 0019](/docs/adr/0019-dockappointment-moves-to-dock-scheduling),
dock-scheduling's appointments), all to build local copies so it never calls a
sibling at request time ([ADR 0003](/docs/adr/0003-local-copies-and-handover)).
Each was read from the producer's `apis/asyncapi.yaml` on `origin/develop` and
no contract contradicted the design. Messages are matched on the **full**
CloudEvents `type`; unknown types are ignored. Every consumer always runs
([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)) under its
own required consumer group.

## product-master: `ProductRegistered`

| Item | Value |
| --- | --- |
| Topic | `warehouse.product-master.events` (`ProductTopic` in `internal/adapters/inbound/kafka/product_consumer.go`) |
| Type | `com.warehouse.wms.product-master.product.ProductRegistered` (`TypeProductRegistered`), byte-identical to product-master's AsyncAPI |
| Consumer group | env `PRODUCT_CONSUMER_GROUP` (required); the consumer always runs: since ADR 0011 the only `PRODUCT_MODE` is `kafka`, and an unset group is a boot error (`loadConfig`) |
| Fields used | `data.sku` only (`{sku, description, version}` are published; subject and key = the SKU) |
| Local copy | `known_skus` (`sku`, `first_seen_at`): existence only, so a replayed or reordered message is harmless |
| Idempotency | claim of the CloudEvents `id` in `processed_events` under consumer `product-registry`, in the same unit of work as the upsert |
| Effect | `RegisterAsn` rejects a SKU not in `known_skus` with `422 unknown-sku` |
| Invalid input | not a CloudEvent, malformed payload, invalid SKU: WARN and commit past |
| Transient failure | retry the same message, 200 ms doubling to 5 s, until it succeeds; no dead-letter topic |

## product-master: pack levels (packaging view)

| Item | Value |
| --- | --- |
| Topic | `warehouse.product-master.events` (`ProductTopic`), read by a second consumer, `PackagingConsumer` in `packaging_consumer.go`, under its own group |
| Types | `com.warehouse.wms.product-master.product.ProductPackLevelDeclared` and `...ProductPackLevelRetired` (`TypePackLevelDeclared`, `TypePackLevelRetired`), byte-identical to product-master's AsyncAPI; `ProductRegistered` belongs to the product consumer and `ProductPackLevelMeasured` is ignored |
| Consumer group | env `PACKAGING_CONSUMER_GROUP` (required); the consumer always runs; a new group replays the topic from the start, so the view is complete on first run |
| Fields used | Declared: `sku`, `level`, `gtin`, `child_gtin`, `qty_of_child`, `units_per_pack`, `effective_from`, `version`. Retired: `gtin`, `effective_to`, `version` (snake_case; key = the SKU) |
| Local copy | the PackagingView ([ADR 0013](/docs/adr/0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view)): GTIN to SKU, level and units per pack |
| Rule | a declared level is upserted by its GTIN; a retired level is end-dated at `effective_to`; an event older than the stored version is ignored, and a retirement of an unknown GTIN changes nothing |
| Idempotency | claim of the CloudEvents `id` under consumer `packaging-view`, in the same unit of work |
| Effect | `ReceiveScan` resolves a scanned GTIN to its SKU and pack level through the view (ADR 0013); an unknown GTIN is rejected and books nothing |
| Invalid input | WARN and commit past; transient failures retry |

## facility-layout: dock doors

| Item | Value |
| --- | --- |
| Topic | `warehouse.facility.events` (`FacilityTopic` in `dock_door_consumer.go`) |
| Types | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` and `...LocationSlotDecommissioned`, byte-identical to facility-layout's AsyncAPI |
| Consumer group | env `DOCK_DOOR_CONSUMER_GROUP` (required); the consumer always runs: since ADR 0011 the only `DOCK_DOOR_MODE` is `kafka`, and an unset group is a boot error |
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
| Types | `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked`, `...DockAppointmentCancelled` (ADR 0019), `...DockAppointmentRescheduled` and `...DockAppointmentDoorReassigned` ([ADR 0020](/docs/adr/0020-appointment-copy-follows-reschedules-and-door-reassignments)); every other type on the topic (`NoShow`, check-in, closures) is committed past. `DoorReassigned` is being moved onto the integration topic by dock-scheduling; until then it never arrives and nothing depends on it |
| Consumer group | env `DOCK_APPOINTMENT_CONSUMER_GROUP`; the consumer always runs; unset is a boot error |
| Fields used | Booked: `appointment_id`, `direction` (only `INBOUND` is copied), `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers[]`, `version`. Cancelled: `appointment_id`, `version`. Rescheduled: `appointment_id`, `door_code`, `window_start`, `window_end`, `version` (`previous_door_code` is logged; `site_id`, `previous_window_*`, `arrival_tolerance_minutes` are ignored). DoorReassigned: `appointment_id`, `to_door_code`, `version` (`from_door_code` is logged). All snake_case; key = `appointment_id` |
| Local copy | `appointments` + `appointment_asns` |
| Rule | an event applies only when its `version` is greater than the stored one (legacy rows are version 0, so any dock-scheduling version wins); all four types share that version line. Rescheduled moves door and window, DoorReassigned moves the door; neither creates a row, so one for an appointment not in the copy (an `OUTBOUND` one, or not seen yet) is ignored, its claim committed and logged at DEBUG. No later event revives a cancelled appointment: its data is refreshed, the state stays `Cancelled` |
| Idempotency | claim of the CloudEvents `id` under consumer `dock-appointment-copy`, in the same unit of work as the update |
| No loop | applying an event writes nothing to the outbox |
| Effect | `OpenReceipt` with an `appointmentId` requires it to exist (`422 unknown-appointment`), to cover the ASN (`422 asn-not-on-appointment`) and not be cancelled (`409 appointment-cancelled`), and takes the copy's current door. A receipt already open keeps its door (a physical fact); only receipts opened after a reschedule or reassignment take the new one. Deprecated `GET /appointments` reads the copy |
| Invalid input | empty ids, `version < 1`, empty door, `window_end <= window_start`: WARN and commit past; transient failures retry |

## Modes

| Copy | Mode env | Group env | Default |
| --- | --- | --- | --- |
| `known_skus` | `PRODUCT_MODE` = `kafka` (only value) | `PRODUCT_CONSUMER_GROUP` (required) | `kafka` |
| PackagingView | none (always on) | `PACKAGING_CONSUMER_GROUP` (required) | n/a |
| `dock_doors` | `DOCK_DOOR_MODE` = `kafka` (only value) | `DOCK_DOOR_CONSUMER_GROUP` (required) | `kafka` |
| `appointments` | none (always on) | `DOCK_APPOINTMENT_CONSUMER_GROUP` (required) | n/a |

The consumers always run and the copies are always consulted: `permissive` and
`off` are boot errors since [ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime).
The first start under a new group replays the topic from the first offset;
readiness does not wait for it, so writes can answer `422` until the copies
have replayed.
The copies are eventually consistent: a SKU registered seconds ago may be
rejected until its event is consumed, and the operator retries.

## Not consumed

Nothing else. `inbound-receiving` has no outbound HTTP client and reads no other
topic (ADR 0001, "No live cross-context lookup, in either direction").

## ADR 0003 vs the code

ADR 0003 says the `known_skus` row "keeps the highest `version`". The schema and
`ApplyProductRegistered` only store existence (`known_skus` has no `version`
column, `SkuStore.Upsert(ctx, sku)` takes only the SKU, and the insert is
`ON CONFLICT (sku) DO NOTHING`). Because only existence matters, replays and
reordering are harmless either way; this page follows the code.
