---
id: domain-events
title: Domain events
sidebar_label: Domain events
sidebar_position: 21
---

# Domain events

Every event this context publishes or consumes. All are CloudEvents 1.0 in
**structured** content mode (`internal/adapters/kafka/cloudevents`: the whole
event is the JSON message value, Kafka header
`content-type: application/cloudevents+json; charset=UTF-8`). Payload fields come
from `apis/asyncapi.yaml` and the payload structs in
`internal/adapters/outbound/kafka/encoder.go`; the
[event catalogue](/docs/api-reference/events) summarises the AsyncAPI file and
[ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) is the type
catalogue (checked against the AsyncAPI file by
`TestEventCatalogueMatchesContract`).

## Published

Raised by the aggregates (`internal/domain/*/events.go`), encoded by the encoder
and inserted into `outbox_events` inside the use case's unit of work; the relay in
`cmd/api` publishes them. Common attributes: `source=/warehouse/inbound-receiving`,
`time` = domain occurred-at (UTC), `datacontenttype=application/json`,
`dataschema=urn:warehouse:inbound-receiving:events:<EventName>:v1`. Topic
`warehouse.inbound-receiving.events` for all nine. The partitioner is the `Hash`
balancer on the Kafka key.

| Full CloudEvents type | `subject` / Kafka key | Producer use case | Payload (`data`) | Known consumers |
| --- | --- | --- | --- | --- |
| `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | `asn_number` / `asn_number` | `RegisterAsn` | `asn_number`, `supplier_ref`, `expected_arrival?`, `lines[]` = `{line_no, sku, expected_qty}` | none |
| `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | `asn_number` / `asn_number` | `CancelAsn` | `asn_number`, `reason?` | none |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked` | `appointment_id` / `appointment_id` | `BookAppointment` | `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers[]` | none (`warehouse-planning` is **planned**) |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCheckedIn` | `appointment_id` / `appointment_id` | `CheckInAppointment` | `appointment_id`, `door_code`, `checked_in_at` | none |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCancelled` | `appointment_id` / `appointment_id` | `CancelAppointment` | `appointment_id`, `door_code`, `reason?` | none |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCompleted` | `appointment_id` / `appointment_id` | `CloseReceipt` (receipt opened from an appointment) | `appointment_id`, `door_code`, `completed_at` | none |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptOpened` | `receipt_id` / `asn_number` | `OpenReceipt` | `receipt_id`, `asn_number`, `appointment_id?`, `door_code?`, `opened_at` | none |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | `receipt_id` / `asn_number` | `ReceiveLine` | `receipt_id`, `asn_number`, `line_no`, `sku`, `quantity`, `condition` (`Good` or `Damaged`), `received_at` | `inventory-storage` (`INBOUND_RECEIPT_CONSUMER_GROUP`, books `Good` only) |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptClosed` | `receipt_id` / `asn_number` | `CloseReceipt` | `receipt_id`, `asn_number`, `closed_at`, `discrepancies[]` = `{line_no, sku, kind, expected_qty, received_qty, damaged_qty}` | none |

`?` marks a field omitted when unset. `kind` is `Short`, `Over` or `Damaged`.
`discrepancies` is present and empty (`[]`) when everything matched, and a line
can appear once per kind; `received_qty` is good + damaged. Receipt events are
keyed by `asn_number` so everything that happens while one ASN is received stays
ordered on one partition. `DockAppointmentCompleted` is keyed by
`appointment_id`, so its order relative to `ReceiptClosed` is **not guaranteed**:
consumers must not depend on it. A breaking payload change is a new `.v2` type
and dataschema, never a mutation of v1.

Example (the handover message):

```json
{"specversion":"1.0","id":"3f8f6c2e-9b1a-4d6e-8a52-0c7d1e4b9a10","source":"/warehouse/inbound-receiving","type":"com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived","subject":"rcpt-123e4567-e89b-12d3-a456-426614174000","time":"2026-10-08T14:00:00Z","datacontenttype":"application/json","dataschema":"urn:warehouse:inbound-receiving:events:ReceiptLineReceived:v1","data":{"receipt_id":"rcpt-123e4567-e89b-12d3-a456-426614174000","asn_number":"ASN-1001","line_no":1,"sku":"SKU-1","quantity":40,"condition":"Good","received_at":"2026-10-08T14:00:00Z"}}
```

## Consumed

| Full CloudEvents type | Topic | Consumer | Group env | Effect |
| --- | --- | --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `ProductConsumer` → `ApplyProductRegistered` | `PRODUCT_CONSUMER_GROUP` (only when `PRODUCT_MODE=kafka`) | upsert the SKU into `known_skus` (uses `data.sku` only) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `DockDoorConsumer` → `ApplyLocationSlotRegistered` | `DOCK_DOOR_CONSUMER_GROUP` (only when `DOCK_DOOR_MODE=kafka`) | upsert into `dock_doors` when `role=Dock` and `dockFlow` is `Inbound` or `Both`; ignore otherwise |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `DockDoorConsumer` → `ApplyLocationSlotDecommissioned` | `DOCK_DOOR_CONSUMER_GROUP` | delete the row for `locationCode`; a code that is not a door is a no-op |

Facility-layout payloads are camelCase (`locationCode`, `role`, `dockFlow`);
product-master's are snake_case. Each consumer dispatches on the FULL `type` and
ignores every other type on its topic, claims the CloudEvents `id` in
`processed_events` in the SAME transaction as the effect, commits the offset only
after that, and skips with a WARN anything that is not a valid CloudEvent. See
[Upstream contracts](/docs/ecosystem/upstream-contracts).

## Reserved, not built

`warehouse.inbound-receiving.analytics` is reserved by ADR 0004 for a later
analytics read side. Nothing publishes to it on `develop`.
