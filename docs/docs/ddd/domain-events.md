---
id: domain-events
title: Domain events
sidebar_label: Domain events
sidebar_position: 21
---

# Domain events

:::caution[DockAppointment moved (ADR 0019); the four types are gone (ADR 0022)]
The dock appointment is owned by dock-scheduling since [ADR 0019](/docs/adr/0019-dockappointment-moves-to-dock-scheduling). This context no longer raises the four `dockappointment.DockAppointment*` types (`Booked`, `CheckedIn`, `Cancelled`, `Completed`): [ADR 0022](/docs/adr/0022-stage-e-removes-the-backfill-the-legacy-encoders-and-replay) removed the `api republish-appointments` backfill, the legacy encoders and the domain `Replay`, and retired the four types from `apis/asyncapi.yaml` on both channels, so the integration topic carries `asn.*` and `receipt.*` types only. What this context keeps is a read-only local copy fed by dock-scheduling's `DockAppointmentBooked`, `DockAppointmentCancelled`, `DockAppointmentRescheduled` and `DockAppointmentDoorReassigned` (a different prefix, `dock-scheduling.dockappointment`; see Consumed below). Where other DDD pages still describe the appointment commands, rules or events, read them as the pre-transfer design; those pages are not yet rewritten.
:::

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

Raised by the aggregates (`internal/domain/asn/events.go`,
`internal/domain/receipt/events.go`), encoded by the encoder
(`internal/adapters/outbound/kafka/encoder.go`, payload structs in
`encoder_payloads.go`) and inserted into `outbox_events` inside the use case's unit
of work; the relay in `cmd/api` publishes them. Common attributes:
`source=/warehouse/inbound-receiving`, `time` = domain occurred-at (UTC),
`datacontenttype=application/json`,
`dataschema=urn:warehouse:inbound-receiving:events:<EventName>:v1`. Topic
`warehouse.inbound-receiving.events` for all ten (the `<entity>` segment is `asn`
or `receipt`: `asnRow` and `receiptRow` in the encoder reject every other event, and
`apis/asyncapi.yaml` declares exactly these ten on the integration channel). The
partitioner is the `Hash` balancer on the Kafka key.

| Full CloudEvents type | `subject` / Kafka key | Producer use case | Payload (`data`) | Known consumers |
| --- | --- | --- | --- | --- |
| `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | `asn_number` / `asn_number` | `RegisterAsn` | `asn_number`, `supplier_ref`, `owner_ref?`, `expected_arrival?`, `lines[]` = `{line_no, sku, expected_qty}`, `cartons[]?` | `dock-scheduling` (`ASN_CONSUMER_GROUP`), `yard-management` (`INBOUND_RECEIVING_CONSUMER_GROUP`) |
| `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | `asn_number` / `asn_number` | `CancelAsn` | `asn_number`, `reason?` | `dock-scheduling`, `yard-management` (same groups) |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptOpened` | `receipt_id` / `asn_number` | `OpenReceipt` | `receipt_id`, `asn_number`, `appointment_id?`, `door_code?`, `opened_at` | `yard-management` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | `receipt_id` / `asn_number` | `ReceiveLine`, `ReceiveScan` (a case or each scan) | `receipt_id`, `asn_number`, `line_no`, `sku`, `quantity`, `condition` (`Good` or `Damaged`), `received_at`, `owner_ref?`, `pack_gtin?`, `cases?`, `sscc?` | `inventory-storage` (`INBOUND_RECEIPT_CONSUMER_GROUP`, books `Good` only) |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptCartonReceived` | `receipt_id` / `asn_number` | `ReceiveScan` (a carton accepted; identity only, books no quantity) | `receipt_id`, `asn_number`, `owner_ref?`, `sscc?`, `parent_sscc?`, `sealed`, `lpn`, `contents[]` = `{line_no, sku, gtin, qty, lot?, expiry?, condition}`, `received_at`, and, additive and optional ([ADR 0024](/docs/adr/0024-gs1-label-receiving-validation-destination-earliest-best-before-and-receiver-assigned-sscc)), `sscc_source?` (`supplier` / `receiver`), `best_before?`, `expiry?`, `production_date?`, `packaging_date?`, `serial?`, `purchase_order?`, `ship_to_gln?`, `ship_for_gln?` | `inventory-storage`, `network-fulfillment` (`INBOUND_RECEIPT_CONSUMER_GROUP` in each) |
| `com.warehouse.wms.inbound-receiving.receipt.CartonDiscrepancyRaised` | `receipt_id` / `asn_number` | `ReceiveScan` (a refused scan, one event per finding) and `CloseReceipt` (a `MissingCarton` finding for a manifest carton never accepted) | `receipt_id`, `sscc?`, `kind`, `expected?`, `actual?`, `at`, `w13_code?`, `cbv_disposition?`, `supplier_ref?` (label findings) | `exception-management` (`CARTON_DISCREPANCY_CONSUMER_GROUP`), `network-fulfillment` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptClosed` | `receipt_id` / `asn_number` | `CloseReceipt` | `receipt_id`, `asn_number`, `closed_at`, `discrepancies[]` = `{line_no, sku, kind, expected_qty, received_qty, damaged_qty}` | `yard-management`, `network-fulfillment` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptStowCompleted` | `receipt_id` / `asn_number` | `ApplyVisitCheckedIn`, `ApplyContainerStowed`, `CloseReceipt` (whichever completes the evidence; ADR 0021) | `receipt_id`, `asn_number`, `appointment_id`, `arrived_at`, `closed_at`, `stowed_at`, `dock_to_stock_seconds`, `carton_count` | none yet (the projector folds the analytics copy) |
| `com.warehouse.wms.inbound-receiving.receipt.ReceivingAdviceIssued` | `receipt_id` / `asn_number` | `CloseReceipt` (same transaction, right after `ReceiptClosed`, once per receipt; ADR 0023) | `receipt_id`, `asn_number`, `despatch_advice_ref`, `supplier_ref`, `owner_ref?`, `issued_at`, `last_carton_at`, `sla_seconds`, `within_sla`, `lines[]` (per line quantities, bounded SSCC list, `earliest_best_before?`, `earliest_expiry?`, `discrepancies[]` = `{kind, quantity, w13_code?, cbv_disposition}`), `carton_findings[]` | none yet (partner-connectivity planned, D-3) |
| `com.warehouse.wms.inbound-receiving.receipt.ReceivingAdviceOverdue` | `receipt_id` / `asn_number` | the housekeeping sweep, once per Open receipt past `RECEIVING_ADVICE_SLA` (ADR 0023) | `receipt_id`, `asn_number`, `last_carton_at`, `due_at`, `sla_seconds`, `overdue_at` | none yet |

`?` marks a field omitted when unset. The line-level discrepancy `kind` on
`ReceiptClosed` is `Short`, `Over` or `Damaged`; the `kind` on
`CartonDiscrepancyRaised` is one of the carton kinds (`UnexpectedCarton`,
`MissingCarton`, `DuplicateSscc`, `UnknownGtin`, `CaseQtyMismatch`,
`MixedCaseDeclaredAsCasePack`, `UnlabelledCarton`, `CartonOverLimit`,
`MixedCartonContentMismatch`, `LabelNonCompliant`, `WrongDestination`; `cartonKinds` in
`internal/domain/receipt/carton_findings.go`). `discrepancies` is present and empty
(`[]`) when everything matched, and a line can appear once per kind; `received_qty`
is good + damaged. Every receipt event, and both ASN events, is keyed by
`asn_number`, so everything that happens while one ASN is received stays ordered on
one partition. The four `dockappointment.DockAppointment*` types are not in this
table because nothing raises them any more (ADR 0022); the known-consumer column was
read from the dispatch code (full-type constants) on each sibling's `origin/develop`
on 2026-11-10, and no sibling code dispatches on them. A breaking payload change is a
new `.v2` type and dataschema, never a mutation of v1.

Example (the handover message):

```json
{"specversion":"1.0","id":"3f8f6c2e-9b1a-4d6e-8a52-0c7d1e4b9a10","source":"/warehouse/inbound-receiving","type":"com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived","subject":"rcpt-123e4567-e89b-12d3-a456-426614174000","time":"2026-10-08T14:00:00Z","datacontenttype":"application/json","dataschema":"urn:warehouse:inbound-receiving:events:ReceiptLineReceived:v1","data":{"receipt_id":"rcpt-123e4567-e89b-12d3-a456-426614174000","asn_number":"ASN-1001","line_no":1,"sku":"SKU-1","quantity":40,"condition":"Good","received_at":"2026-10-08T14:00:00Z"}}
```

## Consumed

| Full CloudEvents type | Topic | Consumer | Group env | Effect |
| --- | --- | --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `ProductConsumer` → `ApplyProductRegistered` | `PRODUCT_CONSUMER_GROUP` (required) | upsert the SKU into `known_skus` (uses `data.sku` only) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `DockDoorConsumer` → `ApplyLocationSlotRegistered` | `DOCK_DOOR_CONSUMER_GROUP` (required) | upsert into `dock_doors` when `role=Dock` and `dockFlow` is `Inbound` or `Both`; ignore otherwise |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `DockDoorConsumer` → `ApplyLocationSlotDecommissioned` | `DOCK_DOOR_CONSUMER_GROUP` | delete the row for `locationCode`; a code that is not a door is a no-op |
| `com.warehouse.wms.product-master.product.ProductPackLevelDeclared` | `warehouse.product-master.events` | `PackagingConsumer` → `ApplyPackLevelDeclared` | `PACKAGING_CONSUMER_GROUP` (required) | upsert the declared pack level into the packaging view |
| `com.warehouse.wms.product-master.product.ProductPackLevelRetired` | `warehouse.product-master.events` | `PackagingConsumer` → `ApplyPackLevelRetired` | `PACKAGING_CONSUMER_GROUP` | retire the pack level in the packaging view |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked` | `warehouse.dock-scheduling.events` | `DockAppointmentConsumer` → `ApplyDockAppointmentBooked` | `DOCK_APPOINTMENT_CONSUMER_GROUP` (required) | upsert the `appointments` copy when `direction=INBOUND` (ADR 0019); outbound is ignored |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentCancelled` | `warehouse.dock-scheduling.events` | `DockAppointmentConsumer` → `ApplyDockAppointmentCancelled` | `DOCK_APPOINTMENT_CONSUMER_GROUP` | mark the stored appointment cancelled |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled` | `warehouse.dock-scheduling.events` | `DockAppointmentConsumer` → `ApplyDockAppointmentRescheduled` | `DOCK_APPOINTMENT_CONSUMER_GROUP` | move the copy's door and window (ADR 0020) |
| `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned` | `warehouse.dock-scheduling.events` | `DockAppointmentConsumer` → `ApplyDockAppointmentDoorReassigned` | `DOCK_APPOINTMENT_CONSUMER_GROUP` | move the copy's door (ADR 0020) |
| `com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` | `warehouse.yard-management.events` | `VisitArrivalConsumer` → `ApplyVisitCheckedIn` | `VISIT_ARRIVAL_CONSUMER_GROUP` (required) | store the earliest `arrived_at` per appointment into `visit_arrivals`, then evaluate the dock-to-stock fact (ADR 0021) |
| `com.warehouse.wms.inventory-storage.container.ContainerStowed` | `warehouse.inventory.events` | `CartonStowConsumer` → `ApplyContainerStowed` | `CARTON_STOW_CONSUMER_GROUP` (required) | store the earliest stow time per `lpn` into `carton_stows`, then evaluate the dock-to-stock fact (ADR 0021) |

Facility-layout payloads are camelCase (`locationCode`, `role`, `dockFlow`);
product-master's are snake_case. The six consumers always run (`startConsumers` in `cmd/api/workers.go`: product, packaging, dock door, dock appointment, visit arrival, carton stow). Each dispatches on the FULL `type` and
ignores every other type on its topic, claims the CloudEvents `id` in
`processed_events` in the SAME transaction as the effect, commits the offset only
after that, and skips with a WARN anything that is not a valid CloudEvent. See
[Upstream contracts](/docs/ecosystem/upstream-contracts).

## Analytics stream (internal)

Since ADR 0006 every published event is also written, in the same unit of
work and under the same CloudEvents `id`, `type`, `subject` and Kafka key, to
`warehouse.inbound-receiving.analytics` (DLQ
`warehouse.inbound-receiving.analytics.dlq`) with
`dataschema=urn:warehouse:inbound-receiving:analytics:<EventName>:v1` and a
payload byte-for-byte equal to the integration payload (`FanoutEncoder`,
`internal/adapters/outbound/kafka/analytics_encoder.go`). The topic therefore
carries the same ten types as the integration topic; `apis/asyncapi.yaml`
declares ten analytics messages. Its only consumer is this context's own
`inbound-projector` (group `ANALYTICS_CONSUMER_GROUP`, default
`inbound-receiving-analytics`); it is not an integration contract, so sibling
contexts keep reading `warehouse.inbound-receiving.events`.

The projector folds eleven kinds (`report.Kinds()`): seven of the ten current types
(`ASNRegistered`, `ASNCancelled`, `ReceiptOpened`, `ReceiptLineReceived`,
`ReceiptClosed`, `ReceiptStowCompleted`, `ReceivingAdviceIssued`) and the four historical
`dockappointment.DockAppointment*` types. It does not fold `ReceiptCartonReceived`,
`CartonDiscrepancyRaised` or `ReceivingAdviceOverdue`: an unknown type is ignored (a DEBUG line). The four
historical types stay in the projector on purpose ([ADR 0022](/docs/adr/0022-stage-e-removes-the-backfill-the-legacy-encoders-and-replay)):
a stream is append-only, so a rebuild from the topic may still read them, and the
`appointments` block of the receiving-performance report stays frozen. Nothing
produces new ones.
