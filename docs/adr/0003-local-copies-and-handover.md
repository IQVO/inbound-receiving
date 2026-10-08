# ADR 0003: local copies, consumed contracts and the handover to inventory-storage

## Status

Accepted (2026-10-08).

## Context

Receiving needs two facts it does not own: does this SKU exist (product-master)
and is this an inbound dock door (facility-layout). It also hands received
goods to the stock ledger (inventory-storage). The fleet rule (ADR 0001, and
product-master ADR 0001) is no live cross-context lookup: contexts keep a
local copy built from the producer's published events and never call a sibling
at request time. This ADR fixes those copies, the contracts they are built
from (each read from the producer's `apis/asyncapi.yaml` on `origin/develop`
on 2026-10-08, not from a design document) and the handover.

## Decision

### Consumed contracts

| type | topic | key fields consumed |
|---|---|---|
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `data` = `{sku, description, version}`; subject and key = the SKU |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `data` = `{eventName, eventType, occurredAt, locationCode, aisleId, zoneId, locationType, role?, dockFlow?, activities?, maxWeightKg, maxVolumeM3}`; key = `locationCode`. An absent `role` means `Storage`. `dockFlow` (`Inbound`, `Outbound`, `Both`) is present only for `role=Dock` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `data` = `{eventName, eventType, occurredAt, locationCode}`; key = `locationCode` |

Facility-layout payloads are camelCase (they carry the domain event struct
verbatim); product-master's are snake_case. Consumers dispatch on the FULL
`type`, ignore every other type on those topics, dedupe on the CloudEvents
`id` in the same database transaction as the effect, commit the Kafka offset
only after that transaction (FetchMessage then CommitMessages), and skip with
a WARN, committing past, anything that is not a valid CloudEvent. No contract
contradicted the design on 2026-10-08.

### Local copies

- **`known_skus`**: one row per SKU, upserted from `ProductRegistered`. Only
  existence matters, so a replayed or reordered message is harmless (the row
  keeps the highest `version`).
- **`dock_doors`**: one row per door code. `LocationSlotRegistered` with
  `role=Dock` and `dockFlow` in `Inbound` or `Both` upserts a row; any other
  registration (including `role` absent, and `Dock` with `dockFlow=Outbound`)
  is ignored. `LocationSlotDecommissioned` deletes the row for that
  `locationCode` (a decommission for a code that is not a door is a no-op).

### Modes and consumer groups

| copy | mode env | consumer group env |
|---|---|---|
| `known_skus` | `PRODUCT_MODE` = `kafka` or `permissive` | `PRODUCT_CONSUMER_GROUP` |
| `dock_doors` | `DOCK_DOOR_MODE` = `kafka` or `permissive` | `DOCK_DOOR_CONSUMER_GROUP` |

- `permissive` is the default. The copy is not consulted: every SKU and every
  door code is accepted (fail-open). The cluster sets `kafka` once the copies
  have replayed.
- `kafka`: registering an ASN with a SKU that is not in `known_skus` is a 422
  `unknown-sku`; booking a door that is not in `dock_doors` is a 422
  `unknown-dock-door`. The consumer runs under a stable group (the copy is a
  durable table, offsets are committed) whose id comes from the env var
  above, never from a literal. **An unset group id with mode `kafka` is a boot
  error**, not a silent no-op. With mode `permissive` the consumer is not
  started and the group id is ignored.
- The first start under a new group replays the topic from the first offset
  (`FirstOffset`); readiness does not wait for the replay, which is why the
  cluster flips to `kafka` only afterwards.

### Handover to inventory-storage

When a line is received, `inbound-receiving` publishes `ReceiptLineReceived`
(through the transactional outbox, `warehouse.inbound-receiving.events`).
`inventory-storage` consumes it and, for `condition=Good` only, runs its
existing ReceiveStock use case with `(sku, quantity)`. The rest follows from
the existing staged-receipt seam: the units are staged and Unlocated until
someone stows them.

- **Event-driven, never synchronous.** No REST or MCP call goes in either
  direction, and there is no acknowledgement event back. The CloudEvents `id`
  is the idempotency key on the consuming side; duplicates are harmless.
- **Good only.** `Damaged` units are recorded on the receipt (and counted in
  its discrepancies) but are NOT booked as receivable stock. Quarantine is a
  later ADR.
- **Stow stays an RF action** (item scan plus location scan in
  inventory-storage). Receiving prescribes no location.
- **Failure on the far side** (for example inventory-storage rejects the SKU)
  is that consumer's skip-and-log; this context has no compensation flow in v1.
  The receipt's counts remain the source of truth for the dock.
- inventory-storage records its side as its own ADR; its newest ADR on
  `origin/develop` on 2026-10-08 is 0036, so the handover ADR takes the next
  free number there (0037 unless another change takes it first). This repo
  does not cite a number it does not own.

Example of the message inventory-storage consumes:

```json
{"specversion":"1.0","id":"3f8f6c2e-9b1a-4d6e-8a52-0c7d1e4b9a10","source":"/warehouse/inbound-receiving","type":"com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived","subject":"rcpt-123e4567-e89b-12d3-a456-426614174000","time":"2026-10-08T14:00:00Z","datacontenttype":"application/json","dataschema":"urn:warehouse:inbound-receiving:events:ReceiptLineReceived:v1","data":{"receipt_id":"rcpt-123e4567-e89b-12d3-a456-426614174000","asn_number":"ASN-1001","line_no":1,"sku":"SKU-1","quantity":40,"condition":"Good","received_at":"2026-10-08T14:00:00Z"}}
```

### Planned, not built

`warehouse-planning` consuming `DockAppointmentBooked` as inbound-labor demand
is a documented future edge. Its CapacityPlan has no inbound process path, so
that is a separate ADR there. Nothing in this repository depends on it, and no
document may describe it as live.

### Exclusions

Yard and trailer management, returns, putaway directives and quality
inspection stay out of scope (ADR 0001).

## Consequences

- Both copies are eventually consistent. A SKU registered seconds ago may be
  rejected in `kafka` mode until its event is consumed; the operator retries.
- An ASN accepted in `permissive` mode may carry a SKU that product-master
  never registered. inventory-storage's own checks still apply downstream.
- The producer contracts are pinned in `apis/asyncapi.yaml`'s consumed
  channels; a producer's breaking change is a new versioned type and a change
  here, never a silent drift.
