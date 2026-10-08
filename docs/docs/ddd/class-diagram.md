---
id: class-diagram
title: Class diagrams
sidebar_label: Class diagrams
sidebar_position: 17
---

# Class diagrams

UML class diagrams of the domain model and of the hexagonal ports with their
adapters. Type and method names are the real ones; unexported fields are shown
only where they carry an invariant. The domain is split in two diagrams to keep
each readable.

## Asn and DockAppointment

```mermaid
classDiagram
  direction LR

  class Asn {
    <<AggregateRoot>>
    -number Number
    -supplierRef string
    -expectedArrival time.Time
    -state State
    -version int64
    +Register(number, supplierRef, expectedArrival, lines, now)$ Asn
    +Rehydrate(...)$ Asn
    +Cancel(reason, now) Event[]
    +BeginReceiving() error
    +Complete() error
    +Snapshot() Snapshot
  }
  class AsnLine {
    <<ValueObject>>
    +LineNo() int
    +SKU() SKU
    +ExpectedQty() int64
  }
  class AsnState {
    <<Enumeration>>
    Registered
    Receiving
    Closed
    Cancelled
  }
  class AsnSnapshot {
    <<ValueObject>>
    +Number Number
    +State State
    +Lines Line[]
  }
  class SKU {
    <<ValueObject>>
    +NewSKU(value)$ SKU
  }

  class DockAppointment {
    <<AggregateRoot>>
    -id ID
    -doorCode DoorCode
    -carrier string
    -window Window
    -state State
    -version int64
    +Book(id, door, carrier, start, end, asnNumbers, now)$ DockAppointment
    +Rehydrate(...)$ DockAppointment
    +CheckIn(at) Event[]
    +Cancel(reason, now) Event[]
    +Complete(at) Event[]
  }
  class Window {
    <<ValueObject>>
    +NewWindow(start, end)$ Window
    +Overlaps(other) bool
  }
  class DoorCode {
    <<ValueObject>>
  }
  class AppointmentState {
    <<Enumeration>>
    Booked
    CheckedIn
    Completed
    Cancelled
  }
  class Schedule {
    <<DomainService>>
    +CheckNoOverlap(candidate, activeOnSameDoor) error
  }

  class ASNRegistered { <<DomainEvent>> }
  class ASNCancelled { <<DomainEvent>> }
  class DockAppointmentBooked { <<DomainEvent>> }
  class DockAppointmentCheckedIn { <<DomainEvent>> }
  class DockAppointmentCancelled { <<DomainEvent>> }
  class DockAppointmentCompleted { <<DomainEvent>> }

  Asn "1" *-- "1..*" AsnLine
  AsnLine --> SKU
  Asn --> AsnState
  Asn ..> AsnSnapshot : Snapshot()
  Asn ..> ASNRegistered : raises
  Asn ..> ASNCancelled : raises

  DockAppointment *-- Window
  DockAppointment --> DoorCode
  DockAppointment --> AppointmentState
  DockAppointment "1" o-- "1..*" Asn : asnNumbers by id
  Schedule ..> DockAppointment : checks
  DockAppointment ..> DockAppointmentBooked : raises
  DockAppointment ..> DockAppointmentCheckedIn : raises
  DockAppointment ..> DockAppointmentCancelled : raises
  DockAppointment ..> DockAppointmentCompleted : raises
```

Source: `internal/domain/asn/{asn,events}.go`,
`internal/domain/appointment/{appointment,events}.go`,
`internal/domain/shared/*.go`. Omits: getters, `Header`, the `Event` interfaces
and the sentinel errors. `DockAppointment` holds ASN **numbers** (by id), not
`Asn` objects.

## Receipt

```mermaid
classDiagram
  direction LR

  class Receipt {
    <<AggregateRoot>>
    -id ID
    -asnNumber Number
    -appointmentID ID
    -doorCode DoorCode
    -state State
    -openedAt time.Time
    -closedAt time.Time
    -version int64
    +Open(id, snapshot, appointmentID, door, now)$ Receipt
    +Rehydrate(Persisted)$ Receipt
    +ReceiveLine(lineNo, qty, condition, at) Event[]
    +Close(at) Event[]
    +Discrepancies() Discrepancy[]
  }
  class ReceiptLine {
    <<Entity>>
    -lineNo int
    -sku SKU
    -expectedQty int64
    -receivedGood int64
    -receivedDamaged int64
    +Received() int64
  }
  class Discrepancy {
    <<ValueObject>>
    +LineNo int
    +SKU SKU
    +Kind Kind
    +ExpectedQty int64
    +ReceivedQty int64
    +DamagedQty int64
  }
  class Kind {
    <<Enumeration>>
    Short
    Over
    Damaged
  }
  class Condition {
    <<Enumeration>>
    Good
    Damaged
  }
  class ReceiptState {
    <<Enumeration>>
    Open
    Closed
  }
  class AsnSnapshot {
    <<ValueObject>>
  }
  class ReceiptOpened { <<DomainEvent>> }
  class ReceiptLineReceived { <<DomainEvent>> }
  class ReceiptClosed { <<DomainEvent>> }

  Receipt "1" *-- "1..*" ReceiptLine
  Receipt --> ReceiptState
  Receipt ..> Discrepancy : computes on Close
  Discrepancy --> Kind
  ReceiptLine ..> Condition : counted per
  Receipt ..> AsnSnapshot : opened against
  Receipt ..> ReceiptOpened : raises
  Receipt ..> ReceiptLineReceived : raises
  Receipt ..> ReceiptClosed : raises
```

Source: `internal/domain/receipt/{receipt,events}.go`. Omits: getters, `Header`,
`LineState` and `Persisted` (the persisted shapes used by `Rehydrate`). The
`appointmentID` and `doorCode` references are by id and empty for a walk-in.

## Ports and adapters

```mermaid
classDiagram
  direction LR

  class UnitOfWork { <<port>> +Do(ctx, fn) error }
  class AsnRepository { <<Repository>> +Get +Save +List }
  class AppointmentRepository { <<Repository>> +Get +Save +List +ActiveOnDoor +LockDoor }
  class ReceiptRepository { <<Repository>> +Get +Save +List +OpenByAsn }
  class OutboxRepository { <<port>> +Insert }
  class EventEncoder { <<port>> +EncodeAsn +EncodeAppointment +EncodeReceipt }
  class ProcessedEvents { <<port>> +Claim }
  class SkuDirectory { <<port>> +Exists }
  class SkuStore { <<port>> +Upsert }
  class DockDoorDirectory { <<port>> +Exists +List }
  class DockDoorStore { <<port>> +Upsert +Remove }
  class IdempotencyStore { <<port>> +Do }

  class Postgres { <<adapter>> postgres/* }
  class Memory { <<adapter>> memory/* }
  class KafkaEncoder { <<adapter>> outbound/kafka Encoder }
  class OutboxRelay { <<adapter>> outbound/outbox Relay }
  class HttpServer { <<inbound adapter>> inbound/http Server }
  class ProductConsumer { <<inbound adapter>> inbound/kafka }
  class DockDoorConsumer { <<inbound adapter>> inbound/kafka }
  class UseCases { <<application>> usecases/* }

  HttpServer --> UseCases
  ProductConsumer --> UseCases
  DockDoorConsumer --> UseCases
  UseCases ..> UnitOfWork
  UseCases ..> AsnRepository
  UseCases ..> AppointmentRepository
  UseCases ..> ReceiptRepository
  UseCases ..> OutboxRepository
  UseCases ..> EventEncoder
  UseCases ..> ProcessedEvents
  UseCases ..> SkuDirectory
  UseCases ..> SkuStore
  UseCases ..> DockDoorDirectory
  UseCases ..> DockDoorStore
  HttpServer ..> IdempotencyStore
  Postgres ..|> UnitOfWork
  Postgres ..|> AsnRepository
  Postgres ..|> AppointmentRepository
  Postgres ..|> ReceiptRepository
  Postgres ..|> OutboxRepository
  Postgres ..|> ProcessedEvents
  Postgres ..|> IdempotencyStore
  Memory ..|> UnitOfWork
  Memory ..|> AsnRepository
  KafkaEncoder ..|> EventEncoder
  OutboxRelay ..> OutboxRepository : drains
```

Source: `internal/application/ports/*.go`, `cmd/api/main.go` (`adapters`,
`buildAdapters`, `buildServer`), the adapter packages under `internal/adapters`.
Omits: `Clock` and `IDGenerator` (trivial `clock.System`, `idgen.UUID`), the
in-memory implementations of every other port (the `Memory` adapter implements
the same ports as `Postgres`; only two edges are drawn), the Kafka relay sink,
telemetry and the Postgres pool. Adapters never import each other: only
`cmd/api` wires them to the use cases.
