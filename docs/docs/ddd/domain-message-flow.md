---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 15
---

# Domain message flow

Following ddd-crew [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling).
Arrows are prefixed `cmd:` (command), `evt:` (event) or `qry:` (query), and only
real messages appear: REST routes from `NewRouter`, CloudEvents types from
`apis/asyncapi.yaml`.

## Scenario 1: a delivery announced, appointed, received and handed over

```mermaid
sequenceDiagram
  autonumber
  actor Sup as Supplier-facing operator
  actor Plan as Dock planner
  actor Dock as Dock staff
  participant IR as inbound-receiving
  participant K as Kafka
  participant INV as inventory-storage

  Sup->>IR: cmd: POST /asns
  IR-->>K: evt: ASNRegistered
  Plan->>IR: cmd: POST /appointments
  IR-->>K: evt: DockAppointmentBooked
  Plan->>IR: cmd: POST /appointments/{id}/check-in
  IR-->>K: evt: DockAppointmentCheckedIn
  Dock->>IR: cmd: POST /receipts
  IR-->>K: evt: ReceiptOpened
  Dock->>IR: cmd: POST /receipts/{id}/lines
  IR-->>K: evt: ReceiptLineReceived
  K-->>INV: evt: ReceiptLineReceived (Good lines are booked as staged stock)
  Dock->>IR: cmd: POST /receipts/{id}/close
  IR-->>K: evt: ReceiptClosed
  IR-->>K: evt: DockAppointmentCompleted
```

Source: `internal/adapters/inbound/http/server.go`,
`internal/application/usecases/{asn,appointment,receipt}.go`,
`internal/adapters/outbound/kafka/encoder.go`, inventory-storage
`internal/adapters/inbound/kafka/inbound_receipt_consumer.go`.
Omits: the outbox relay hop between the use case and Kafka (every `evt:` leaves
through `outbox_events`), the repeated `ReceiveLine` calls, and the cancel paths.
`DockAppointmentCompleted` is raised only when the receipt was opened from an
appointment. Its key is `appointment_id`, so its order relative to
`ReceiptClosed` is not guaranteed.

## Scenario 2: the local copies behind the SKU and door checks

```mermaid
sequenceDiagram
  autonumber
  participant PM as product-master
  participant FL as facility-layout
  participant K as Kafka
  participant IR as inbound-receiving
  actor Op as Operator

  PM-->>K: evt: ProductRegistered
  K-->>IR: evt: ProductRegistered (known_skus upsert)
  FL-->>K: evt: LocationSlotRegistered (role Dock)
  K-->>IR: evt: LocationSlotRegistered (dock_doors upsert if Inbound or Both)
  FL-->>K: evt: LocationSlotDecommissioned
  K-->>IR: evt: LocationSlotDecommissioned (dock_doors delete)
  Op->>IR: cmd: POST /asns
  IR->>IR: qry: SkuDirectory.Exists (local, PRODUCT_MODE=kafka)
  Op->>IR: cmd: POST /appointments
  IR->>IR: qry: DockDoorDirectory.Exists (local, DOCK_DOOR_MODE=kafka)
  Op->>IR: qry: GET /docks
```

Source: `internal/adapters/inbound/kafka/{product_consumer,dock_door_consumer}.go`,
`internal/application/usecases/{consumers,asn,appointment,queries}.go`.
Omits: the `processed_events` claim and offset commits (see the
[sequence diagrams](/docs/ddd/sequence-diagrams)). The two `qry:` rows are
in-process port calls, not messages to a sibling.

## Scenario 3: a short and damaged delivery, with the discrepancy report

```mermaid
sequenceDiagram
  autonumber
  actor Dock as Dock staff
  participant IR as inbound-receiving
  participant K as Kafka

  Dock->>IR: cmd: POST /receipts (ASN-1001, line 1 expects 40)
  IR-->>K: evt: ReceiptOpened
  Dock->>IR: cmd: POST /receipts/{id}/lines (line 1, 30, Good)
  IR-->>K: evt: ReceiptLineReceived (Good, 30)
  Dock->>IR: cmd: POST /receipts/{id}/lines (line 1, 4, Damaged)
  IR-->>K: evt: ReceiptLineReceived (Damaged, 4)
  Dock->>IR: cmd: POST /receipts/{id}/close
  IR-->>K: evt: ReceiptClosed (Short and Damaged for line 1)
  Dock->>IR: qry: GET /receipts/{id}
```

Source: `Receipt.ReceiveLine` and `Receipt.Close` in
`internal/domain/receipt/receipt.go`; the numbers follow the `closeReceipt`
example in `apis/openapi.yaml`. Line 1 ends with `received_qty` 34 (30 good plus
4 damaged), so `Close` raises `Short` and `Damaged` for it, in that order.
