---
id: bounded-context-canvas
title: Bounded context canvas
sidebar_label: Bounded context canvas
sidebar_position: 12
---

# Bounded context canvas

Following the ddd-crew [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas).
Every message row below maps to a route in `NewRouter`
(`internal/adapters/inbound/http/server.go`) or to a topic and CloudEvents type
in `internal/adapters/inbound/kafka/{product_consumer,dock_door_consumer}.go` /
`internal/adapters/outbound/kafka/encoder.go`.

## Name

**Inbound Receiving** (`inbound-receiving`, Go module
`github.com/claudioed/inbound-receiving`, GitHub `IQVO/inbound-receiving`).

## Purpose

Own the inbound dock workflow up to, and not including, stock placement: the
supplier's announcement of a delivery (ASN), the carrier's booked door window
(dock appointment) and the counted receipt with the discrepancies found when it
closes. Publish every accepted change so `inventory-storage` can book received
Good units as staged stock without a request-time call. It answers no "where"
or "how many are in stock" question (ADR 0001).

## Strategic Classification

| Axis | Verdict | Evidence |
| --- | --- | --- |
| Domain | **Supporting** | ADR 0001 "Classification"; see the [core domain chart](/docs/ddd/core-domain-chart) |
| Business model | **Cost reduction / compliance**: an accurate receipt and discrepancy record | warehouse-docs contexts table; the receipt is the dock's source of truth for what arrived |
| Evolution | **Custom-built** (genesis) | Created 2026-10-08 (ADRs 0001-0004) |

## Domain Roles

| Role | Applies? | Notes |
| --- | --- | --- |
| Execution context | **Yes** (primary, document-level) | Dock staff record what arrived; it moves no stock itself. |
| Specification context | Partly | The ASN is the supplier's specification of what should arrive. |
| Gateway / Open Host Service | **Yes** | REST plus the `warehouse.inbound-receiving.events` Published Language. |
| Analysis context | No | Discrepancy is a per-line comparison on close. |
| Analytics / reporting | No (planned, ADR 0004 reserves `warehouse.inbound-receiving.analytics`) | No read side on `develop`. |

## Inbound Communication

| Collaborator | Message | Type | Channel | Relationship |
| --- | --- | --- | --- | --- |
| Supplier-facing operator | Register ASN | Command | REST `POST /asns` (`Idempotency-Key` required) | This context is the OHS |
| Operator | Cancel ASN | Command | REST `POST /asns/{asnNumber}/cancel` | OHS |
| Dock planner | Book dock appointment | Command | REST `POST /appointments` (`Idempotency-Key` required) | OHS |
| Dock planner / gate | Check in, cancel appointment | Command | REST `POST /appointments/{appointmentId}/check-in`, `.../cancel` | OHS |
| Dock staff | Open receipt | Command | REST `POST /receipts` (`Idempotency-Key` required) | OHS |
| Dock staff | Receive a quantity against a line | Command | REST `POST /receipts/{receiptId}/lines` (`Idempotency-Key` required) | OHS |
| Dock staff | Close receipt | Command | REST `POST /receipts/{receiptId}/close` | OHS |
| Operator, console, agent | Get / list ASNs, appointments, receipts | Query | REST `GET /asns`, `/asns/{asnNumber}`, `/appointments`, `/appointments/{appointmentId}`, `/receipts`, `/receipts/{receiptId}` | OHS |
| Operator, console, agent | List known inbound dock doors | Query | REST `GET /docks` | OHS |
| `product-master` | `ProductRegistered` | Event | Kafka `warehouse.product-master.events`, `com.warehouse.wms.product-master.product.ProductRegistered` | Conformist with a local copy (`known_skus`); group `PRODUCT_CONSUMER_GROUP` |
| `facility-layout` | `LocationSlotRegistered` | Event | Kafka `warehouse.facility.events`, `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | Conformist with a local copy (`dock_doors`); group `DOCK_DOOR_CONSUMER_GROUP` |
| `facility-layout` | `LocationSlotDecommissioned` | Event | Kafka `warehouse.facility.events`, `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | Conformist; same group |

No REST endpoint is authenticated (fleet-wide revert of 2026-09-11;
`TestNoAuthMiddlewareReintroduced` fails the build if auth middleware returns).
An MCP read-only server for `warehouse-ops-agent` is *planned*, not built.

## Outbound Communication

The context makes **no** synchronous call to any sibling and has no outbound
HTTP client (ADR 0001). Everything outbound leaves through the transactional
outbox and the relay in `cmd/api`.

| Collaborator | Message | Type | Channel | Relationship |
| --- | --- | --- | --- | --- |
| `inventory-storage` | `ReceiptLineReceived` | Event | Kafka `warehouse.inbound-receiving.events`, `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | This context upstream, OHS + Published Language; inventory-storage books `condition=Good` only (its ADR 0037) |
| none today | `ASNRegistered`, `ASNCancelled` | Event | same topic, `...asn.*` | OHS + PL |
| `warehouse-planning` (**planned, not built**) | `DockAppointmentBooked` | Event | same topic, `...dockappointment.DockAppointmentBooked` | OHS + PL; would be inbound-labor demand, needs an inbound process path in its CapacityPlan first |
| none today | `DockAppointmentCheckedIn`, `DockAppointmentCancelled`, `DockAppointmentCompleted` | Event | same topic, `...dockappointment.*` | OHS + PL |
| none today | `ReceiptOpened`, `ReceiptClosed` | Event | same topic, `...receipt.*` | OHS + PL |

## Ubiquitous Language

Full glossary with code identifiers: [Ubiquitous language](/docs/ddd/ubiquitous-language).
Top terms: **ASN**, **Dock appointment**, **Door**, **Window**, **Check-in**,
**Receipt**, **Line**, **Condition** (Good / Damaged), **Discrepancy** (Short /
Over / Damaged), **Walk-in**, **Snapshot**, **Local copy**, **Version**.

## Business Decisions

1. A receipt is opened against a snapshot of the ASN; the ASN must be
   `Registered` or `Receiving` (ADR 0002).
2. Over-receipt is accepted, never refused: it is physically there, and shows as
   an `Over` discrepancy on close (ADR 0002).
3. Closing a receipt with nothing received is allowed; every line is then `Short`
   (ADR 0002).
4. A door window is half-open, at most 4 hours, may not start in the past, and
   two active appointments may not overlap on one door (ADR 0002).
5. Check-in is allowed from 30 minutes before the window starts until it ends
   (ADR 0002).
6. At most one open receipt per ASN (ADR 0002).
7. Only `Good` units are handed to `inventory-storage`; `Damaged` units are
   recorded and counted in the discrepancies but not booked, and stow stays an
   RF action there (ADR 0003).
8. Every difference is reported; there is no tolerance policy in v1 (ADR 0002).
9. SKU and door checks read local copies and fail open until the cluster flips
   `PRODUCT_MODE` / `DOCK_DOOR_MODE` to `kafka` (ADR 0003).

## Assumptions

- ASNs arrive through the REST API already structured: no EDI parsing, purchase
  orders or supplier master data here (ADR 0001).
- Consumers of the receipt events can live with the ordering caveat: receipt
  events are keyed by `asn_number`, `DockAppointmentCompleted` by
  `appointment_id`, so their relative order is not guaranteed (ADR 0004).
- The far side handles its own failures (inventory-storage skips and logs); this
  context has no compensation flow in v1 (ADR 0003).

## Verification Metrics

- Golden exact-JSON tests per published type (`TestEncoderGoldenAsnEvents`,
  `TestEncoderGoldenAppointmentEvents`, `TestEncoderGoldenReceiptEvents`,
  `internal/adapters/outbound/kafka/encoder_test.go`).
- `TestEventCatalogueMatchesContract` checks ADR 0004 against
  `apis/asyncapi.yaml`; `TestHexagonalArchitecture` enforces the layering.
- Outbox atomicity and relay delivery proven with testcontainers Postgres and
  Kafka (`TestUseCasesCommitStateAndOutboxAtomically`,
  `TestRelayRealPostgresAndKafkaPublishesCloudEventsWithTheRightTypeAndKey`,
  `TestProductConsumerEndToEnd`, `TestDockDoorConsumerEndToEnd`).
- One open receipt per ASN proven against Postgres
  (`TestReceiptRepoAtMostOneOpenReceiptPerAsn`).
- No business metric is emitted yet.

## Open Questions

- When does `warehouse-planning` get an inbound process path so
  `DockAppointmentBooked` can become labor demand (planned, its own ADR there)?
- A tolerance policy for over-receipt, and a quarantine flow for Damaged units,
  are later, separately decided changes (ADR 0002, ADR 0001).
- Which later phases (MCP read tools, analytics read side, console remote) land
  first?
