---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 19
---

# Sequence diagrams

One diagram per command use case plus the local-copy consumers and the outbox
relay: inbound adapter, application service, aggregate, repository, outbox. The
`Idempotency-Key` middleware wraps the creating `POST`s and is drawn once, in the
first diagram. `UoW` is `ports.UnitOfWork` (one Postgres transaction).

## Register an ASN

```mermaid
sequenceDiagram
  autonumber
  actor Op as Operator
  participant H as HTTP adapter
  participant I as Idempotency middleware
  participant UC as RegisterAsn
  participant A as Asn aggregate
  participant R as AsnRepository
  participant O as Outbox

  Op->>H: POST /asns with Idempotency-Key
  H->>I: key and body hash
  alt key missing
    I-->>Op: 400 idempotency-key-required
  else same key, different body
    I-->>Op: 422 idempotency-key-reused
  else same key and body seen before
    I-->>Op: the original response replayed
  else new key
    I->>UC: Handle(RegisterAsnCommand)
    UC->>A: Register(number, supplierRef, lines, now)
    A-->>UC: Asn at version 1 and ASNRegistered
    alt invalid number, lines, SKU or quantity
      UC-->>Op: 400 invalid-asn-number and the other invalid-* slugs
    end
    alt PRODUCT_MODE=kafka and a SKU is not in known_skus
      UC-->>Op: 422 unknown-sku
    end
    UC->>R: Get(number) inside UoW
    alt number exists
      UC-->>Op: 409 asn-already-exists
    else free
      UC->>R: Save(asn, loadedVersion 0)
      UC->>O: Insert(encoded ASNRegistered)
      UC-->>I: Asn
      I-->>Op: 201 with ETag and Location
    end
  end
```

Source: `internal/adapters/inbound/http/{server,idempotency}.go`,
`internal/application/usecases/asn.go`, `internal/domain/asn/asn.go`.
Omits: the other POSTs behave the same way in the middleware; the response is
stored with the key in the same transaction.

## Book a dock appointment

```mermaid
sequenceDiagram
  autonumber
  actor Op as Dock planner
  participant H as HTTP adapter
  participant UC as BookAppointment
  participant D as DockAppointment aggregate
  participant Dir as DockDoorDirectory
  participant AR as AsnRepository
  participant PR as AppointmentRepository
  participant S as Schedule
  participant O as Outbox

  Op->>H: POST /appointments with Idempotency-Key
  H->>UC: Handle(BookAppointmentCommand)
  UC->>D: Book(id, door, carrier, window, asnNumbers, now)
  D-->>UC: appointment at version 1 and DockAppointmentBooked
  alt invalid window, door, carrier or ASN list
    UC-->>Op: 400 invalid-window, window-in-past and the other slugs
  end
  alt DOCK_DOOR_MODE=kafka
    UC->>Dir: Exists(door)
    Dir-->>UC: false
    UC-->>Op: 422 unknown-dock-door
  end
  UC->>PR: LockDoor(door) inside UoW
  loop each covered ASN
    UC->>AR: Get(number)
    alt not found
      UC-->>Op: 422 unknown-asn
    else not Registered or Receiving
      UC-->>Op: 409 asn-not-receivable
    end
  end
  UC->>PR: ActiveOnDoor(door)
  UC->>S: CheckNoOverlap(candidate, active)
  alt window overlaps an active booking
    S-->>UC: ErrWindowOverlap
    UC-->>Op: 409 door-window-overlap
  else free
    UC->>PR: Save(appointment, loadedVersion 0)
    UC->>O: Insert(encoded DockAppointmentBooked)
    UC-->>Op: 201 with ETag and Location
  end
```

Source: `internal/application/usecases/appointment.go`,
`internal/domain/appointment/appointment.go`,
`internal/adapters/outbound/postgres/appointment_repository.go` (`LockDoor` is
`pg_advisory_xact_lock`). Omits: the idempotency middleware (see the first
diagram).

## Check in and cancel an appointment

```mermaid
sequenceDiagram
  autonumber
  actor Op as Operator
  participant H as HTTP adapter
  participant UC as CheckInAppointment or CancelAppointment
  participant PR as AppointmentRepository
  participant D as DockAppointment aggregate
  participant O as Outbox

  Op->>H: POST /appointments/{id}/check-in or /cancel
  H->>UC: Handle(AppointmentActionCommand with If-Match)
  UC->>PR: Get(id) inside UoW
  alt not found
    UC-->>Op: 404 appointment-not-found
  end
  alt If-Match differs from the loaded version
    UC-->>Op: 412 version-mismatch
  end
  UC->>D: CheckIn(now) or Cancel(reason, now)
  alt not Booked
    D-->>UC: ErrNotBooked
    UC-->>Op: 409 appointment-not-booked
  else check-in outside 30 min before start .. end
    D-->>UC: ErrOutsideCheckInWindow
    UC-->>Op: 409 outside-check-in-window
  else accepted
    D-->>UC: DockAppointmentCheckedIn or DockAppointmentCancelled
    UC->>PR: Save(appointment, loadedVersion)
    alt lost the version race
      UC-->>Op: 409 concurrent-modification
    end
    UC->>O: Insert(encoded event)
    UC-->>Op: 200 with the new ETag
  end
```

Source: `internal/application/usecases/appointment.go` (`Writer.act`),
`internal/domain/appointment/appointment.go`. `ASN` cancel follows the same
shape (`CancelAsn`, `ErrAsnInProgress` / `ErrAsnTerminal` instead).

## Open a receipt

```mermaid
sequenceDiagram
  autonumber
  actor Dock as Dock staff
  participant H as HTTP adapter
  participant UC as OpenReceipt
  participant AR as AsnRepository
  participant PR as AppointmentRepository
  participant RR as ReceiptRepository
  participant RC as Receipt aggregate
  participant O as Outbox

  Dock->>H: POST /receipts with Idempotency-Key
  H->>UC: Handle(OpenReceiptCommand)
  UC->>AR: Get(asnNumber) inside UoW
  alt not found
    UC-->>Dock: 422 unknown-asn
  end
  opt appointmentId given
    UC->>PR: Get(appointmentId)
    alt not found
      UC-->>Dock: 422 unknown-appointment
    else ASN not covered
      UC-->>Dock: 422 asn-not-on-appointment
    else not CheckedIn
      UC-->>Dock: 409 appointment-not-checked-in
    end
  end
  UC->>RC: Open(id, asn.Snapshot(), appointmentId, door, now)
  alt ASN is Closed or Cancelled
    RC-->>UC: ErrAsnNotReceivable
    UC-->>Dock: 409 asn-not-receivable
  end
  UC->>RR: OpenByAsn(asnNumber)
  alt an open receipt exists
    UC-->>Dock: 409 receipt-already-open
  end
  UC->>AR: Save(asn.BeginReceiving, loadedVersion)
  UC->>RR: Save(receipt, loadedVersion 0)
  UC->>O: Insert(encoded ReceiptOpened)
  UC-->>Dock: 201 with ETag and Location
```

Source: `internal/application/usecases/receipt.go` (`OpenReceipt.open`),
`internal/domain/receipt/receipt.go`. The ASN save raises no event; the partial
unique index `receipts_one_open_per_asn` closes the race the `OpenByAsn` read
cannot.

## Receive a line

```mermaid
sequenceDiagram
  autonumber
  actor Dock as Dock staff
  participant H as HTTP adapter
  participant UC as ReceiveLine
  participant RR as ReceiptRepository
  participant RC as Receipt aggregate
  participant O as Outbox

  Dock->>H: POST /receipts/{id}/lines with Idempotency-Key
  H->>UC: Handle(ReceiveLineCommand)
  UC->>RC: ParseCondition(Good or Damaged)
  alt other value
    UC-->>Dock: 400 invalid-condition
  end
  UC->>RR: Get(id) inside UoW
  alt not found
    UC-->>Dock: 404 receipt-not-found
  end
  alt If-Match differs
    UC-->>Dock: 412 version-mismatch
  end
  UC->>RC: ReceiveLine(lineNo, qty, condition, now)
  alt receipt Closed
    RC-->>UC: ErrReceiptClosed
    UC-->>Dock: 409 receipt-closed
  else line not on the ASN
    RC-->>UC: ErrLineNotOnAsn
    UC-->>Dock: 422 line-not-on-asn
  else quantity out of bounds
    UC-->>Dock: 400 invalid-quantity
  else accepted, over-receipt included
    RC-->>UC: ReceiptLineReceived
    UC->>RR: Save(receipt, loadedVersion)
    UC->>O: Insert(encoded ReceiptLineReceived)
    UC-->>Dock: 201 with the receipt
  end
```

Source: `internal/application/usecases/receipt.go` (`ReceiveLine.Handle`),
`internal/domain/receipt/receipt.go`.

## Close a receipt

```mermaid
sequenceDiagram
  autonumber
  actor Dock as Dock staff
  participant H as HTTP adapter
  participant UC as CloseReceipt
  participant RR as ReceiptRepository
  participant RC as Receipt aggregate
  participant AR as AsnRepository
  participant PR as AppointmentRepository
  participant O as Outbox

  Dock->>H: POST /receipts/{id}/close
  H->>UC: Handle(CloseReceiptCommand)
  UC->>RR: Get(id) inside ONE UoW
  UC->>RC: Close(now)
  alt already Closed
    RC-->>UC: ErrReceiptClosed
    UC-->>Dock: 409 receipt-closed
  else closed
    RC-->>UC: ReceiptClosed with the discrepancies
  end
  UC->>AR: Get(asn), Complete(), Save(asn)
  opt the receipt came from an appointment
    UC->>PR: Get(appointment), Complete(now), Save(appointment)
    UC->>O: Insert(encoded DockAppointmentCompleted)
  end
  UC->>RR: Save(receipt, loadedVersion)
  UC->>O: Insert(encoded ReceiptClosed)
  UC-->>Dock: 200 with the closed receipt
```

Source: `internal/application/usecases/receipt.go` (`CloseReceipt.Handle`,
`completeAsn`, `completeAppointment`). The ASN completion raises no event. All
three saves and both outbox rows commit together or not at all.

## Local-copy consumer (product and dock door)

```mermaid
sequenceDiagram
  autonumber
  participant K as Kafka topic
  participant C as Consumer loop
  participant CE as cloudevents.Decode
  participant UC as Apply use case
  participant P as ProcessedEvents
  participant S as Copy store

  K->>C: FetchMessage
  C->>CE: Decode(value)
  alt not a valid CloudEvent
    C->>K: log WARN and CommitMessages
  else type not handled
    C->>K: CommitMessages
  else handled type
    C->>UC: Handle(eventId, payload)
    alt invalid value, ErrInvalidEvent
      C->>K: log WARN and CommitMessages
    else dock role is not Inbound or Both
      UC-->>C: ignored
      C->>K: CommitMessages
    else
      UC->>P: Claim(consumer, eventId) inside UoW
      alt already claimed
        UC-->>C: duplicate
      else new
        UC->>S: Upsert or Remove
        UC-->>C: applied
      end
      C->>K: CommitMessages
    end
  end
```

Source: `internal/adapters/inbound/kafka/{kafka,product_consumer,dock_door_consumer}.go`,
`internal/application/usecases/consumers.go`. A transient error from the use case
retries the same message with capped backoff and never commits past it.

## Outbox relay

```mermaid
sequenceDiagram
  autonumber
  participant R as Relay.Run
  participant S as OutboxRepo.Drain
  participant DB as Postgres outbox_events
  participant K as Kafka sink

  loop every OUTBOX_RELAY_INTERVAL
    R->>S: Drain(limit 100)
    S->>DB: SELECT unpublished ORDER BY id FOR UPDATE SKIP LOCKED
    loop each row in id order
      S->>K: Send(message)
      alt send fails
        S->>DB: UPDATE attempts + 1 and last_error
        S-->>R: error, stop the pass
      else sent
        S->>DB: UPDATE published_at = now()
      end
    end
    S-->>R: published count
  end
```

Source: `internal/adapters/outbound/outbox/relay.go`,
`internal/adapters/outbound/postgres/outbox_repository.go`,
`internal/adapters/outbound/kafka/relay_sink.go`. A full batch is followed
immediately by another pass. A crash between send and update republishes the row
with the same CloudEvents id, which is what lets consumers dedupe.
