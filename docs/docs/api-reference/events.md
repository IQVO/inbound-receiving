---
id: events
title: Events (AsyncAPI)
sidebar_position: 99
---

# Events

Summary of `apis/asyncapi.yaml` (AsyncAPI 2.6.0, version 1.0.0). The file is the
authority for payload fields;
[ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) is the type
catalogue.

## Envelope

Every message, published or consumed, is a **CloudEvents 1.0** event in
structured content mode with the Kafka header
`content-type: application/cloudevents+json; charset=UTF-8`.

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | UUID minted once per domain event at encode time and persisted with the outbox row; consumers dedupe on it |
| `source` | `/warehouse/inbound-receiving` |
| `type` | `com.warehouse.wms.inbound-receiving.<entity>.<EventName>` with `<entity>` one of `asn`, `dockappointment`, `receipt` |
| `subject` | the aggregate instance id (ASN number, appointment id or receipt id) |
| `time` | domain occurred-at, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:warehouse:inbound-receiving:events:<EventName>:v1` |

The Kafka key is `asn_number` for `asn.*` and `receipt.*` events and
`appointment_id` for `dockappointment.*` events. A breaking payload change gets a
new `.v2` type and dataschema; an existing one is never mutated.

## Published

Topic `warehouse.inbound-receiving.events`, written by the outbox relay.

| Type suffix | Raised | Payload (`data`) |
| --- | --- | --- |
| `asn.ASNRegistered` | `POST /asns` | `asn_number`, `supplier_ref`, `expected_arrival?`, `lines[]` = `{line_no, sku, expected_qty}` |
| `asn.ASNCancelled` | `POST /asns/{asnNumber}/cancellations` | `asn_number`, `reason?` |
| `dockappointment.DockAppointmentBooked` | `POST /appointments` | `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers[]` |
| `dockappointment.DockAppointmentCheckedIn` | `POST /appointments/{appointmentId}/check-ins` | `appointment_id`, `door_code`, `checked_in_at` |
| `dockappointment.DockAppointmentCancelled` | `POST /appointments/{appointmentId}/cancellations` | `appointment_id`, `door_code`, `reason?` |
| `dockappointment.DockAppointmentCompleted` | `POST /receipts/{receiptId}/closures` for a receipt opened from an appointment | `appointment_id`, `door_code`, `completed_at` |
| `receipt.ReceiptOpened` | `POST /receipts` | `receipt_id`, `asn_number`, `appointment_id?`, `door_code?`, `opened_at` |
| `receipt.ReceiptLineReceived` | `POST /receipts/{receiptId}/lines` | `receipt_id`, `asn_number`, `line_no`, `sku`, `quantity`, `condition` (`Good` or `Damaged`), `received_at` |
| `receipt.ReceiptClosed` | `POST /receipts/{receiptId}/closures` | `receipt_id`, `asn_number`, `closed_at`, `discrepancies[]` = `{line_no, sku, kind, expected_qty, received_qty, damaged_qty}` |

`?` marks a field omitted when unset. `kind` is `Short`, `Over` or `Damaged`;
`discrepancies` is present and empty when everything matched. `received_qty` is
good + damaged. `ReceiptLineReceived` is the **handover event**:
inventory-storage books `condition=Good` quantities only.

## Consumed

| Type | Topic | Group env | Mode |
| --- | --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` | `PRODUCT_MODE=kafka` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` | `DOCK_DOOR_MODE=kafka` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` | `DOCK_DOOR_MODE=kafka` |

Both modes are `kafka` (the default and the only legal value) and both consumer
groups are required: an unset group, `permissive` or `off` is a boot error. Every other type on those topics is ignored and a
message that is not a valid CloudEvent is logged and skipped. The full list with
producer use cases and consumers is on [Domain events](/docs/ddd/domain-events).

## Analytics stream

Every event above is also written to `warehouse.inbound-receiving.analytics`
(DLQ `warehouse.inbound-receiving.analytics.dlq`) with the same `type`, `id`,
subject, key and payload; only the `dataschema` differs
(`urn:warehouse:inbound-receiving:analytics:<EventName>:v1`). It is consumed
only by this service's `inbound-projector` (group from env
`ANALYTICS_CONSUMER_GROUP`) and is not an integration contract (ADR 0006).

## Consumer rules

- Dispatch on the **full** `type`; ignore unknown types.
- Dedupe on the CloudEvents `id` (the same `id` is republished on a relay retry).
- Do not depend on the order of `DockAppointmentCompleted` relative to
  `ReceiptClosed`: they are keyed differently.
- `warehouse-planning` consumes `DockAppointmentBooked` and
  `DockAppointmentCancelled` as inbound-labour demand (its group
  `warehouse-planning-inbound-demand`, [ADR 0017](https://github.com/IQVO/warehouse-planning/blob/develop/docs/adr/0017-inbound-labour-demand-and-observed-capacity-feedback.md),
  `internal/adapters/inbound/kafka/inbound_demand_consumer.go` there). It keys
  its read model on `appointment_id` and is order-independent, so a Cancelled
  that overtakes its Booked is safe. It ignores `CheckedIn` and `Completed`.
