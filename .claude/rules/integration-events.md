---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

This service PUBLISHES the inbound dock workflow on
`warehouse.inbound-receiving.events` (through the transactional outbox) and
CONSUMES two producers' events into local copies (ADR 0003):
product-master's `ProductRegistered` from `warehouse.product-master.events`
(`known_skus`) and facility-layout's `LocationSlotRegistered` /
`LocationSlotDecommissioned` from `warehouse.facility.events` (`dock_doors`).
The analytics topic `warehouse.inbound-receiving.analytics` is reserved for a
later phase and not produced yet.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.<ctx>.events` AND analytics `warehouse.<ctx>.analytics`) is a
CloudEvents 1.0 event in structured content mode. This is a hard fleet rule,
not a preference — there is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` — copy it from this template's
  `templates/cloudevents/cloudevents.go.tmpl` (+ its test) and change only
  the per-repo constants. Transport stays `segmentio/kafka-go` (no sdk-go
  protocol/client packages, no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries
  the same id); `source=/warehouse/<repo>`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:<repo>:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`
  (this repo: `com.warehouse.wms.inbound-receiving`). The SAME `type` names the
  occurrence on both the integration and the analytics topic; `dataschema`
  names the payload shape. Breaking payload change => new `.v2` type + new
  dataschema version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via
  `DataAs`, dedupe on `id`, and DLQ/skip — WARN log + commit past, never
  crash, never block the partition, never fall back to parsing a legacy
  shape — anything that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes +
  the `content-type` header); a legacy-flat-message-rejected test per
  consumer; Kafka integration tests via testcontainers only.

Full standard, subdomain table and the fleet's cross-service type
catalogue: warehouse-docs `docs/strategic-design/event-standard-cloudevents.md`.
This repo's ADR: `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`.

### Published types

Topic `warehouse.inbound-receiving.events`. The `<entity>` segment is the
raising aggregate, lowercase, no separators: `asn`, `dockappointment`,
`receipt`. `subject` is the aggregate id; the Kafka key is shown per row.
`data` is snake_case, optional fields are omitted when unset.

| `type` | Kafka key | `dataschema` |
| --- | --- | --- |
| `com.warehouse.wms.inbound-receiving.asn.ASNRegistered` | `asn_number` | `urn:warehouse:inbound-receiving:events:ASNRegistered:v1` |
| `com.warehouse.wms.inbound-receiving.asn.ASNCancelled` | `asn_number` | `urn:warehouse:inbound-receiving:events:ASNCancelled:v1` |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentBooked` | `appointment_id` | `urn:warehouse:inbound-receiving:events:DockAppointmentBooked:v1` |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCheckedIn` | `appointment_id` | `urn:warehouse:inbound-receiving:events:DockAppointmentCheckedIn:v1` |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCancelled` | `appointment_id` | `urn:warehouse:inbound-receiving:events:DockAppointmentCancelled:v1` |
| `com.warehouse.wms.inbound-receiving.dockappointment.DockAppointmentCompleted` | `appointment_id` | `urn:warehouse:inbound-receiving:events:DockAppointmentCompleted:v1` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptOpened` | `asn_number` | `urn:warehouse:inbound-receiving:events:ReceiptOpened:v1` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | `asn_number` | `urn:warehouse:inbound-receiving:events:ReceiptLineReceived:v1` |
| `com.warehouse.wms.inbound-receiving.receipt.ReceiptClosed` | `asn_number` | `urn:warehouse:inbound-receiving:events:ReceiptClosed:v1` |

`ReceiptLineReceived` is the handover event inventory-storage consumes (Good
quantities only, ADR 0003). `ReceiptClosed.discrepancies` is present and `[]`
when everything matched. Golden exact-JSON tests pin every type.

### Consumed types

| `type` | topic | producer |
| --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductRegistered` | `warehouse.product-master.events` | product-master |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | facility-layout |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | facility-layout |

Facility-layout `data` is camelCase (`locationCode`, `role?`, `dockFlow?`, ...;
an absent `role` means `Storage`); product-master `data` is snake_case
(`sku`, `description`, `version`). Only `role=Dock` with `dockFlow` `Inbound`
or `Both` becomes a door. Dedupe on `id` in the SAME DB transaction as the
effect; commit the offset only after it (FetchMessage + CommitMessages).

## Consumer group id

Both consumers are optional local copies selected by mode env vars, default
`permissive` (consumer not started): `PRODUCT_MODE` with group id env
`PRODUCT_CONSUMER_GROUP`, and `DOCK_DOOR_MODE` with group id env
`DOCK_DOOR_CONSUMER_GROUP`. Mode `kafka` with an unset group id is a BOOT
ERROR. The groups are STABLE (the copy is a durable table, offsets are
committed). Any consumer group id MUST be env-configurable, never a hardcoded string literal --
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this (a real incident:
wes-work-planning's hardcoded group id let a locally-run e2e-tests process
silently collide with the live in-cluster Deployment's consumer group on
the shared fleet Kafka broker).

If a consumer replays from FirstOffset on every start to build an
in-memory read model (rather than resuming from a committed offset), the
group id must additionally be UNIQUE PER PROCESS INSTANCE (hostname+PID+
timestamp), not just configurable -- see HARNESS.md's Kafka section for
why a shared group breaks that pattern specifically. (Not the case here: the
copies live in Postgres.)
