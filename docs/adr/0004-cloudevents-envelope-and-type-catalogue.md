# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-08).

## Context

Every Kafka message in the fleet is a CloudEvents 1.0 event in structured mode
(fleet rule `.claude/rules/fleet/cloudevents.md`, warehouse-docs
`docs/strategic-design/event-standard-cloudevents.md`). This ADR is the
catalogue of every type inbound-receiving publishes or consumes;
`TestEventCatalogueMatchesContract` checks it against `apis/asyncapi.yaml`.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID minted once per domain event and persisted
  with the outbox row (stable across relay retries; consumers dedupe on it);
  `source` `/warehouse/inbound-receiving`; `subject` the id of the aggregate
  instance; `time` the occurred-at instant in UTC; `datacontenttype`
  `application/json`; `dataschema`
  `urn:warehouse:inbound-receiving:events:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; message key as
  in the table below.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.
- `data` is snake_case JSON; optional fields are omitted when unset;
  timestamps are RFC 3339 UTC.

### Published on `warehouse.inbound-receiving.events`

`<entity>` is the raising aggregate, lowercase, no separators: `asn`,
`dockappointment`, `receipt`.

| type | subject | Kafka key | raised when |
|---|---|---|---|
| `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | asn_number | asn_number | an ASN is registered |
| `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | asn_number | asn_number | a Registered ASN is cancelled |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked` | appointment_id | appointment_id | a door window is booked |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCheckedIn` | appointment_id | appointment_id | the carrier checks in |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCancelled` | appointment_id | appointment_id | a Booked appointment is cancelled |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCompleted` | appointment_id | appointment_id | the receipt opened from the appointment closes |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptOpened` | receipt_id | asn_number | receiving of an ASN starts |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | receipt_id | asn_number | a quantity is received against a line |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptClosed` | receipt_id | asn_number | receiving ends |

The receipt events are keyed by `asn_number`, so everything that happens while
one ASN is received stays ordered on one partition. `DockAppointmentCompleted`
is keyed by `appointment_id`, so its order relative to `ReceiptClosed` is not
guaranteed; consumers must not depend on it.

### Payloads (`data`)

| type (entity.Event) | fields |
|---|---|
| `asn.ASNRegistered` | `asn_number`, `supplier_ref`, `expected_arrival?`, `lines[]` = `{line_no, sku, expected_qty}` |
| `asn.ASNCancelled` | `asn_number`, `reason?` |
| `dockappointment.DockAppointmentBooked` | `appointment_id`, `door_code`, `carrier`, `window_start`, `window_end`, `asn_numbers[]` |
| `dockappointment.DockAppointmentCheckedIn` | `appointment_id`, `door_code`, `checked_in_at` |
| `dockappointment.DockAppointmentCancelled` | `appointment_id`, `door_code`, `reason?` |
| `dockappointment.DockAppointmentCompleted` | `appointment_id`, `door_code`, `completed_at` |
| `receipt.ReceiptOpened` | `receipt_id`, `asn_number`, `appointment_id?`, `door_code?`, `opened_at` |
| `receipt.ReceiptLineReceived` | `receipt_id`, `asn_number`, `line_no`, `sku`, `quantity`, `condition` (`Good` or `Damaged`), `received_at` |
| `receipt.ReceiptClosed` | `receipt_id`, `asn_number`, `closed_at`, `discrepancies[]` = `{line_no, sku, kind, expected_qty, received_qty, damaged_qty}`, `kind` in `Short`, `Over`, `Damaged` |

Payload rules:

- `ReceiptClosed.discrepancies` is present and empty (`[]`) when everything
  matched. A line can appear once per kind.
- `received_qty` is good + damaged; `damaged_qty` is the damaged part.
- `ReceiptLineReceived` is the handover event (ADR 0003): inventory-storage
  books `condition=Good` quantities only.
- A breaking payload change is a new `.v2` type and dataschema, never a
  mutation of v1.

### Consumed (ADR 0003)

| type | topic | consumer group env |
|---|---|---|
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `DOCK_DOOR_CONSUMER_GROUP` |

Every other type on those topics is ignored. A message that is not a valid
CloudEvent is logged and skipped, never retried forever.

### Analytics topic (ADR 0006)

Every published event above is ALSO written, in the same transaction and under
the same `id`, to `warehouse.inbound-receiving.analytics` (dead-letter topic
`warehouse.inbound-receiving.analytics.dlq`). The `type`, `subject`, Kafka key
and payload are identical to the integration message; only the `dataschema`
differs: `urn:warehouse:inbound-receiving:analytics:<EventName>:v1`. Only this
service's `cmd/inbound-projector` consumes it (consumer group from env
`ANALYTICS_CONSUMER_GROUP`); it is not an integration contract, and other
contexts keep reading `warehouse.inbound-receiving.events`. The projection and
its report are specified in ADR 0006.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is
  no shared Go code.
- The nine types are pinned: the fleet plan and inventory-storage's consumer
  build against these exact strings.
