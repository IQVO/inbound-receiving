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
| `type` | `com.warehouse.wms.inbound-receiving.<entity>.<EventName>` with `<entity>` one of `asn`, `receipt` (the `dockappointment` entity is retired, ADR 0019 and ADR 0022) |
| `subject` | the aggregate instance id (ASN number or receipt id) |
| `time` | domain occurred-at, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:warehouse:inbound-receiving:events:<EventName>:v1` |

The Kafka key is `asn_number` for `asn.*` and `receipt.*` events. A breaking payload change gets a
new `.v2` type and dataschema; an existing one is never mutated.

## Published

Topic `warehouse.inbound-receiving.events`, written by the outbox relay: ten types, the ones `apis/asyncapi.yaml` declares on that channel.

| Type suffix | Raised | Payload (`data`) |
| --- | --- | --- |
| `asn.ASNRegistered` | `POST /asns` | `asn_number`, `supplier_ref`, `owner_ref?`, `expected_arrival?`, `lines[]` = `{line_no, sku, expected_qty}`, `cartons[]?` |
| `asn.ASNCancelled` | `POST /asns/{asnNumber}/cancellations` | `asn_number`, `reason?` |
| `receipt.ReceiptOpened` | `POST /receipts` | `receipt_id`, `asn_number`, `appointment_id?`, `door_code?`, `opened_at` |
| `receipt.ReceiptLineReceived` | `POST /receipts/{receiptId}/lines` | `receipt_id`, `asn_number`, `line_no`, `sku`, `quantity`, `condition` (`Good` or `Damaged`), `received_at`, `owner_ref?`, `pack_gtin?`, `cases?`, `sscc?` |
| `receipt.ReceiptCartonReceived` | `POST /receipts/{receiptId}/scans` (a carton accepted; identity only) | `receipt_id`, `asn_number`, `owner_ref?`, `sscc?`, `parent_sscc?`, `sealed`, `lpn`, `contents[]` = `{line_no, sku, gtin, qty, lot?, expiry?, condition}`, `received_at`, and, additive and optional ([ADR 0024](/docs/adr/0024-gs1-label-receiving-validation-destination-earliest-best-before-and-receiver-assigned-sscc)), `sscc_source?`, `best_before?`, `expiry?`, `production_date?`, `packaging_date?`, `serial?`, `purchase_order?`, `ship_to_gln?`, `ship_for_gln?` |
| `receipt.CartonDiscrepancyRaised` | `POST /receipts/{receiptId}/scans` (a refused scan) and `POST /receipts/{receiptId}/closures` (`MissingCarton`, and the label findings `LabelNonCompliant` / `WrongDestination` of a scan, ADR 0024) | `receipt_id`, `sscc?`, `kind`, `expected?`, `actual?`, `at`, `w13_code?`, `cbv_disposition?`, `supplier_ref?` |
| `receipt.ReceiptClosed` | `POST /receipts/{receiptId}/closures` | `receipt_id`, `asn_number`, `closed_at`, `discrepancies[]` = `{line_no, sku, kind, expected_qty, received_qty, damaged_qty}` |
| `receipt.ReceiptStowCompleted` | no request: raised by the `visit-arrival` and `carton-stow` consumers, or by `POST /receipts/{receiptId}/closures`, once a closed receipt opened from an appointment has the gate arrival and a stow for every Good carton (ADR 0021) | `receipt_id`, `asn_number`, `appointment_id`, `arrived_at`, `closed_at`, `stowed_at`, `dock_to_stock_seconds`, `carton_count` |

| `receipt.ReceivingAdviceIssued` | no request: raised by `POST /receipts/{receiptId}/closures`, in the same transaction, right after `ReceiptClosed` (ADR 0023) | `receipt_id`, `asn_number`, `despatch_advice_ref`, `supplier_ref`, `owner_ref?`, `issued_at`, `last_carton_at`, `sla_seconds`, `within_sla`, `lines[]` = `{line_no, sku, gtin?, expected_qty, received_qty, accepted_qty, rejected_qty, held_qty, sscc_count, ssccs?, ssccs_truncated?, discrepancies[]}` (each `{kind, quantity, w13_code?, cbv_disposition}`), `lines_total`, `lines_truncated?`, `carton_findings[]`, `carton_findings_total` |
| `receipt.ReceivingAdviceOverdue` | no request: raised by the housekeeping sweep, once per Open receipt past `RECEIVING_ADVICE_SLA` (ADR 0023) | `receipt_id`, `asn_number`, `last_carton_at`, `due_at`, `sla_seconds`, `overdue_at` |

`?` marks a field omitted when unset. On `ReceiptClosed`, `kind` is `Short`, `Over` or `Damaged` (each with
`w13_code` and `cbv_disposition`, ADR 0023; `CartonDiscrepancyRaised` carries the carton kinds, listed on
[Domain events](/docs/ddd/domain-events));
`discrepancies` is present and empty when everything matched. `received_qty` is
good + damaged. `ReceiptLineReceived` is the **handover event**:
inventory-storage books `condition=Good` quantities only.

## Consumed

| Type | Topic | Group env | Mode |
| --- | --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` | `PRODUCT_MODE=kafka` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` | `DOCK_DOOR_MODE=kafka` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` | `DOCK_DOOR_MODE=kafka` |
| `com.warehouse.wms.product-master.product.ProductPackLevelDeclared`, `...ProductPackLevelRetired` | `warehouse.product-master.events` | `PACKAGING_CONSUMER_GROUP` | always on |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked`, `...DockAppointmentCancelled`, `...DockAppointmentRescheduled`, `...DockAppointmentDoorReassigned` | `warehouse.dock-scheduling.events` | `DOCK_APPOINTMENT_CONSUMER_GROUP` | always on |
| `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | `warehouse.yard-management.events` | `VISIT_ARRIVAL_CONSUMER_GROUP` | always on |
| `com.warehouse.wms.inventory-storage.container.ContainerStowed` | `warehouse.inventory.events` | `CARTON_STOW_CONSUMER_GROUP` | always on |

`PRODUCT_MODE` and `DOCK_DOOR_MODE` are `kafka` (the default and the only legal value) and every consumer
group is required: an unset group, `permissive` or `off` is a boot error. Every other type on those topics is ignored and a
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
- **Retired (ADR 0019, ADR 0022).** The four
  `dockappointment.DockAppointment{Booked,CheckedIn,Cancelled,Completed}` types
  (key `appointment_id`) moved to dock-scheduling. They are no longer raised and
  no longer declared in `apis/asyncapi.yaml`; the `api republish-appointments`
  command that re-announced them is removed. `ReceiptClosed` no longer implies
  a `DockAppointmentCompleted`.
- `warehouse-planning` no longer reads this topic: its inbound labour demand comes from dock-scheduling's appointments, and no sibling's dispatch code reads the four retired types (ADR 0022). The consumers of the eight current types are listed on [Downstream consumers](/docs/ecosystem/downstream-consumers).
