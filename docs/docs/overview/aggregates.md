---
id: aggregates
title: Aggregates
sidebar_position: 2
---

# Aggregates

Three aggregates carry the model, each its own consistency boundary
([ADR 0002](/docs/adr/0002-aggregates-and-invariants)). Domain code lives in
`internal/domain/{asn,appointment,receipt}` plus `internal/domain/shared` (SKU,
quantity and reason value types) and does no I/O. Every aggregate exposes
`Version()`, returns the events a command raised (pull-style, no global bus),
takes the clock as an argument and is rebuilt from persisted state through an
explicit `Rehydrate` that re-validates every invariant. `Version` starts at 1,
grows by one per accepted change and guards the repository write.

## Asn

Package `internal/domain/asn` (`asn.go`, `events.go`).

- **Identity**: `Number`, 1..64 characters of `[A-Za-z0-9._-]` (`NewNumber`,
  `ErrInvalidNumber`).
- **Why the lines live inside the ASN**: the SKU-uniqueness and numbering rules
  are about the set of lines, and a cancel must see the whole document.
- **State**: `Registered -> Receiving -> Closed`, or `Registered -> Cancelled`.

| Method | Raises | Rule |
| --- | --- | --- |
| `Register(number, supplierRef, expectedArrival, lines, now)` | `ASNRegistered` (version 1) | at least one line, `lineNo` exactly `1..n` in order, SKUs unique, `expectedQty` 1..2147483647 |
| `Cancel(reason, now)` | `ASNCancelled` | only from `Registered`; from `Receiving` it is `ErrAsnInProgress`, from a terminal state `ErrAsnTerminal` |
| `BeginReceiving()` | none | internal, driven by the open-receipt use case; already `Receiving` changes nothing |
| `Complete()` | none | internal, driven by the close-receipt use case; only from `Receiving` (`ErrAsnNotReceiving`) |

Supplier reference: non-blank, at most 64 characters, no control characters
(`ErrInvalidSupplierRef`). `Snapshot()` returns the read-only copy a receipt is
opened against.

## DockAppointment

Package `internal/domain/appointment` (`appointment.go`, `events.go`).

- **Identity**: `ID`, `appt-` followed by a lowercase UUID.
- **State**: `Booked -> CheckedIn -> Completed`, or `Booked -> Cancelled`.

| Method | Raises | Rule |
| --- | --- | --- |
| `Book(id, door, carrier, start, end, asnNumbers, now)` | `DockAppointmentBooked` (version 1) | window `[start, end)`, `end > start`, at most `MaxWindow` (4 h); `start` not before the injected clock's now (`ErrWindowInPast`); at least one ASN, none twice |
| `CheckIn(at)` | `DockAppointmentCheckedIn` | only from `Booked` (`ErrNotBooked`), only from `start - CheckInLeadTime` (30 min) to `end`, both inclusive (`ErrOutsideCheckInWindow`) |
| `Cancel(reason, now)` | `DockAppointmentCancelled` | only from `Booked` |
| `Complete(at)` | `DockAppointmentCompleted` | only from `CheckedIn` (`ErrNotCheckedIn`); called when the receipt opened from the appointment closes |

Door code: 1..64 characters, no whitespace, control characters or `/`.
Carrier: non-blank, at most 100 characters. Rehydrating a past window is
allowed.

**`Schedule.CheckNoOverlap(candidate, activeOnSameDoor)`** is a domain service:
no two appointments in `Booked` or `CheckedIn` may overlap on one door. Windows
are half-open, so `[10:00, 12:00)` and `[12:00, 14:00)` are compatible.
Cancelled and Completed appointments, other doors and the candidate itself are
ignored. The rule spans aggregates, so the book use case calls it with what the
repository found for that door; races between concurrent bookings are closed by
a per-door Postgres advisory lock taken around the check and the insert
(`pg_advisory_xact_lock` in `appointment_repository.go`), not by the domain.

## Receipt

Package `internal/domain/receipt` (`receipt.go`, `events.go`), the richest
aggregate.

- **Identity**: `ID`, `rcpt-` followed by a lowercase UUID.
- **State**: `Open -> Closed`.
- **Opened against a snapshot** of the ASN (number, state, lines with expected
  quantity) with an optional appointment id and door code; a walk-in delivery
  has neither. The lines mirror the ASN's and never change shape.
- Each line carries `receivedGood` and `receivedDamaged`.

| Method | Raises | Rule |
| --- | --- | --- |
| `Open(id, snapshot, appointmentID, door, now)` | `ReceiptOpened` (version 1) | the ASN must be `Registered` or `Receiving` (`ErrAsnNotReceivable`) |
| `ReceiveLine(lineNo, qty, condition, at)` | `ReceiptLineReceived` | only on an `Open` receipt (`ErrReceiptClosed`); the line must exist (`ErrLineNotOnAsn`); `qty` 1..2147483647 and the line total stays within that bound; condition `Good` or `Damaged` (`ErrInvalidCondition`) |
| `Close(at)` | `ReceiptClosed` | only on an `Open` receipt; computes the discrepancies |

**Over-receipt is accepted**: the units are physically there, so refusing them
would hide stock. It shows as an `Over` discrepancy on close. Closing a receipt
with nothing received is allowed and every line is then `Short`.

### Discrepancies

`Discrepancies()` returns, per line in line order, one entry per kind in the
order `Short`, `Over`, `Damaged`, each with `expected_qty`, `received_qty`
(good + damaged) and `damaged_qty`:

| Kind | When |
| --- | --- |
| `Short` | `received < expected` (a line nothing was received for is Short) |
| `Over` | `received > expected` |
| `Damaged` | `damaged > 0` |

A line can carry several kinds, for example `Short` and `Damaged`. The result is
never nil: an empty list means everything matched.

## Invariants that are not domain code

| Invariant | Enforced by |
| --- | --- |
| One open receipt per ASN | the open-receipt use case (`requireNoOpenReceipt`, `409 receipt-already-open`) and the partial unique index `receipts_one_open_per_asn ON receipts (asn_number) WHERE state = 'Open'` |
| No door double-booking | `Schedule` plus the per-door advisory lock in the book use case |
| Closing a receipt completes the ASN and the appointment | `CloseReceipt` runs the three changes and their outbox rows in one unit of work |

The tolerances are named constants, not configuration: `appointment.MaxWindow`,
`appointment.CheckInLeadTime` and `receipt.Kind*`. A tolerance policy (accept up
to x % over-receipt silently) is a later, separately decided change; v1 reports
every difference.

Source: `internal/domain/asn/asn.go`, `internal/domain/appointment/appointment.go`,
`internal/domain/receipt/receipt.go`, `internal/application/usecases/*.go`,
`internal/adapters/outbound/postgres/migrations/0001_inbound_receiving.up.sql`.
