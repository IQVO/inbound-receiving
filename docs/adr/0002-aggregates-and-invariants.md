# ADR 0002: aggregates and invariants

## Status

Accepted (2026-10-08).

## Context

ADR 0001 gives the context three concepts: the supplier's announcement, the
carrier's booked window and the counted receiving. This ADR fixes their
aggregate boundaries and invariants. The test for a boundary is "which rules
must hold in one transaction"; everything else is referenced by id and
reconciled by use cases and events.

## Decision

Three aggregates, hand-written in `internal/domain/{asn,appointment,receipt}`
(plus `internal/domain/shared` for the SKU, quantity and reason value types).
Each exposes `Version()`, returns the events a command raised (pull-style, no
global event bus), takes the clock as an argument and rebuilds from persisted
state through an explicit `Rehydrate` that re-validates every invariant.
Version starts at 1, increases by one per accepted change and guards the
repository write (optimistic concurrency, 409 on a race).

### Asn (identity: ASN number)

- Number: 1..64 characters of `[A-Za-z0-9._-]`. Supplier reference:
  non-blank, at most 64 characters, no control characters.
- Expected arrival: optional.
- Lines `[{lineNo, sku, expectedQty}]`: at least one; `lineNo` is exactly
  `1..n` in order; SKUs are unique across lines; `expectedQty` is 1..2147483647.
- State `Registered -> Receiving -> Closed`, or `Registered -> Cancelled`.
  `Cancelled` and `Closed` are terminal.
- `Cancel(reason)` only from `Registered`: from `Receiving` it is
  `ErrAsnInProgress`, from a terminal state `ErrAsnTerminal`.
- `BeginReceiving` and `Complete` are internal transitions driven by the
  receipt use case; they raise no event. `BeginReceiving` on an ASN that is
  already `Receiving` changes nothing.
- Events: `ASNRegistered`, `ASNCancelled`.

Why the lines live inside the ASN: the SKU-uniqueness and numbering rules are
about the set of lines, and a cancel must see the whole document.

### DockAppointment (identity: `appt-<uuid>`)

- Door code: 1..64 characters, no whitespace, control characters or `/`.
  Carrier: non-blank, at most 100 characters.
- Window `[start, end)`: `end > start`, duration at most 4 hours
  (`MaxWindow`); a new booking's `start` must not be before the injected
  clock's now (a window starting exactly now is fine). Rehydrating a past
  window is allowed.
- ASN numbers: at least one, unique.
- State `Booked -> CheckedIn -> Completed`, or `Booked -> Cancelled`.
- `CheckIn(at)` only from `Booked`, and only from `start - 30 min` to `end`
  (both ends inclusive), else `ErrOutsideCheckInWindow`. `Cancel(reason)` only
  from `Booked`. `Complete(at)` only from `CheckedIn`; it is called by the
  receipt use case when the receipt opened from that appointment closes.
- Events: `DockAppointmentBooked`, `DockAppointmentCheckedIn`,
  `DockAppointmentCancelled`, `DockAppointmentCompleted`.
- **Domain service `Schedule.CheckNoOverlap(candidate, activeOnSameDoor)`**:
  no two appointments in `Booked` or `CheckedIn` may overlap on the same
  door. Windows are half-open, so `[10:00, 12:00)` and `[12:00, 14:00)` are
  compatible. The rule spans aggregates, so it is a domain service the book
  use case calls with what the repository found for that door, not a method
  of one appointment. Cancelled and Completed appointments, other doors and
  the candidate itself are ignored. Races between two concurrent bookings
  are closed by the use case taking a per-door lock (a Postgres advisory
  lock) around the check and the insert, not by the domain.

### Receipt (identity: `rcpt-<uuid>`, the richest aggregate)

- Opened against an ASN **snapshot** (number, state, lines with expected
  quantity), with an optional appointment id and door code (a walk-in has
  neither).
- `Open(snapshot, ...)` requires the ASN to be `Registered` or `Receiving`,
  else `ErrAsnNotReceivable`. The lines mirror the ASN's and never change
  shape.
- Lines `[{lineNo, sku, expectedQty, receivedGood, receivedDamaged}]`.
- `ReceiveLine(lineNo, qty, condition, at)`: only on an `Open` receipt
  (`ErrReceiptClosed`); the line must exist (`ErrLineNotOnAsn`); `qty` is
  1..2147483647 and the line's running total stays within that bound;
  `condition` is `Good` or `Damaged`. **Over-receipt is accepted**: it is
  physically there, so refusing it would hide stock. It shows as a discrepancy
  on close.
- `Close(at)` only on an `Open` receipt. It computes the discrepancies per
  line, in line order, one entry per kind in the order `Short`, `Over`,
  `Damaged`, each carrying `expected_qty`, `received_qty` (= good + damaged)
  and `damaged_qty`:
  - `Short`: `received < expected` (a line nothing was received for is Short;
    closing a receipt with nothing received is allowed),
  - `Over`: `received > expected`,
  - `Damaged`: `damaged > 0`.
  A line can carry several kinds (for example Short and Damaged).
- Events: `ReceiptOpened`, `ReceiptLineReceived`, `ReceiptClosed`.

### Cross-aggregate invariants that are not domain code

- **One open receipt per ASN.** Two receipts for one ASN would double-count
  against the same expected quantities. The invariant spans aggregates, so it
  is an application and database rule: the open-receipt use case checks it
  and a **partial unique index** on `receipts (asn_number) WHERE state = 'Open'`
  closes the race. A violation surfaces as 409 `receipt-already-open`.
- **Door double-booking** is the per-door lock plus `Schedule` above.
- **Closing a receipt** completes the ASN, completes the appointment (if any)
  and raises `ReceiptClosed`, `DockAppointmentCompleted` in one unit of work
  with the outbox rows.

### Policy objects

The 4-hour window cap, the 30-minute check-in lead time and the three
discrepancy kinds are named constants in the domain packages
(`appointment.MaxWindow`, `appointment.CheckInLeadTime`, `receipt.Kind*`). A
tolerance policy (accept up to x % over-receipt silently) is a later,
separately decided change; v1 reports every difference.

## Consequences

- The three aggregates never hold each other. The receipt use case loads the
  ASN, copies its snapshot, and drives `BeginReceiving` and `Complete`.
- A receipt that is opened for an ASN whose state changed concurrently fails on
  the ASN's version guard, not on a domain rule.
- Hand-written domain code, no ORM: repositories map rows to the `Rehydrate`
  inputs (`asn.LineInput`, `receipt.Persisted`).
