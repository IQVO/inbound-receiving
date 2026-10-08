---
paths:
  - "internal/domain/**"
  - "internal/application/**"
  - "features/**"
---

# Domain model: ubiquitous language, aggregates, events, use cases

## Ubiquitous Language (use these exact names)

- **ASN** (advance ship notice): the supplier's announcement of one delivery.
  Aggregate root `Asn`, identity = `asnNumber` (1..64 chars of `[A-Za-z0-9._-]`).
- **ASN line**: `{lineNo, sku, expectedQty}`. `lineNo` is exactly `1..n` in
  order, SKUs are unique across lines, `expectedQty` is 1..2147483647.
- **SKU**: 1..64 characters, no whitespace, control characters or `/` (a
  reference to product-master; this context never owns a product).
- **Dock appointment**: a carrier's booked door window covering >= 1 ASNs.
  Aggregate root `DockAppointment`, identity = `appt-<uuid>`.
- **Door code**: the code of an inbound dock door (a facility-layout slot with
  role `Dock`), 1..64 chars, no whitespace, control characters or `/`.
- **Window**: `[start, end)`, `end > start`, at most 4 hours
  (`appointment.MaxWindow`), a new booking must not start before the injected
  clock's now.
- **Receipt**: the counted receiving of one ASN. Aggregate root `Receipt`,
  identity = `rcpt-<uuid>`. Opened against an ASN **snapshot**.
- **Condition**: `Good` or `Damaged`. Only Good units are handed to
  inventory-storage.
- **Discrepancy**: found when a receipt closes, per line and per **kind**:
  `Short` (good+damaged < expected), `Over` (good+damaged > expected),
  `Damaged` (damaged > 0), with `expected_qty`, `received_qty` (= good +
  damaged) and `damaged_qty`.
- **Version**: starts at 1, +1 per accepted change; guards the repository write.

## Aggregates

- **Asn** (`internal/domain/asn`): >= 1 line, numbering and SKU uniqueness,
  non-blank supplier reference. State `Registered -> Receiving -> Closed`, or
  `Registered -> Cancelled`. `Cancel` only from `Registered`
  (`ErrAsnInProgress` from `Receiving`, `ErrAsnTerminal` from terminal states).
  `BeginReceiving` / `Complete` are internal transitions driven by the receipt
  use case and raise no event.
- **DockAppointment** (`internal/domain/appointment`): state `Booked ->
  CheckedIn -> Completed`, or `Booked -> Cancelled`. `CheckIn` only from
  `Booked` and only from `start - 30 min` to `end` inclusive
  (`ErrOutsideCheckInWindow`); `Cancel` only from `Booked`; `Complete` only
  from `CheckedIn`. Domain service **`Schedule.CheckNoOverlap`**: no two
  appointments in `Booked|CheckedIn` overlap on one door (half-open windows).
- **Receipt** (`internal/domain/receipt`, the richest aggregate): `Open` needs
  an ASN snapshot in `Registered|Receiving` (`ErrAsnNotReceivable`).
  `ReceiveLine` only while `Open` (`ErrReceiptClosed`), the line must exist
  (`ErrLineNotOnAsn`), quantity >= 1, condition `Good|Damaged`; **over-receipt
  is allowed**. `Close` computes the discrepancies. **One open receipt per ASN**
  is NOT domain code: it is an application check plus a partial unique index
  (`WHERE state = 'Open'`), ADR 0002.
- `internal/domain/shared`: `SKU`, `ValidateQuantity`, `ValidateReason`.

Every aggregate takes the clock as an argument, returns the events a command
raised (pull-style), exposes `Version()`, and rebuilds from storage through a
`Rehydrate` that re-validates (`asn.Rehydrate`, `appointment.Rehydrate`,
`receipt.Rehydrate(receipt.Persisted)`). Domain code imports only other domain
packages and the standard library.

## Domain events

- `asn`: `ASNRegistered` (supplier, expected arrival, lines), `ASNCancelled` (reason).
- `appointment`: `DockAppointmentBooked`, `DockAppointmentCheckedIn`,
  `DockAppointmentCancelled`, `DockAppointmentCompleted` (raised when the
  receipt opened from the appointment closes).
- `receipt`: `ReceiptOpened`, `ReceiptLineReceived` (the handover event),
  `ReceiptClosed` (discrepancies).

## Key use cases (application layer, built in the service phase)

- `RegisterAsn`, `CancelAsn`: SKU check against `known_skus` when
  `PRODUCT_MODE=kafka`.
- `BookAppointment`, `CheckInAppointment`, `CancelAppointment`: door check
  against `dock_doors` when `DOCK_DOOR_MODE=kafka`, `Schedule.CheckNoOverlap`
  under a per-door lock.
- `OpenReceipt`, `ReceiveLine`, `CloseReceipt`: `OpenReceipt` also calls
  `Asn.BeginReceiving`; `CloseReceipt` also calls `Asn.Complete` and
  `DockAppointment.Complete`, all in ONE unit of work with the outbox rows.
- `Get*` / `List*` reads (cursor pagination), `ListDocks`.

Every write: load, apply the command, save with the loaded version as the
optimistic guard, and enqueue the events in the outbox in ONE unit of work.
