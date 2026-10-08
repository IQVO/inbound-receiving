---
id: use-cases
title: Use cases
sidebar_position: 2
---

# Use cases

Application services in `internal/application/usecases`. The eight write use
cases embed `Writer` (`writer.go`), which runs load, command, version-guarded
save and outbox insert in one `ports.UnitOfWork`. The three consumer use cases
(`consumers.go`) claim the CloudEvents id and apply the effect in one unit of
work.

## Commands

| Use case | Inbound | What it does |
| --- | --- | --- |
| `RegisterAsn` | `POST /asns` (`Idempotency-Key` required) | Builds the ASN (`asn.Register`), with `PRODUCT_MODE=kafka` checks every line's SKU against `known_skus` (`422 unknown-sku`), then saves it unless the number exists (`409 asn-already-exists`). `201`, `ASNRegistered`. |
| `CancelAsn` | `POST /asns/{asnNumber}/cancel` | Loads the ASN, checks `If-Match`, cancels a `Registered` one. `200`, `ASNCancelled`. `409 asn-in-progress` while `Receiving`, `409 asn-terminal` when `Closed` or `Cancelled`. |
| `BookAppointment` | `POST /appointments` (`Idempotency-Key` required) | Builds the appointment (`appointment.Book`), with `DOCK_DOOR_MODE=kafka` checks the door against `dock_doors` (`422 unknown-dock-door`), then in one unit of work takes the per-door advisory lock, requires every covered ASN to exist (`422 unknown-asn`) and be `Registered` or `Receiving` (`409 asn-not-receivable`), reads the active appointments of the door and runs `Schedule.CheckNoOverlap` (`409 door-window-overlap`). `201`, `DockAppointmentBooked`. |
| `CheckInAppointment` | `POST /appointments/{appointmentId}/check-in` | Loads, checks `If-Match`, checks in with the server clock. `200`, `DockAppointmentCheckedIn`. `409 appointment-not-booked`, `409 outside-check-in-window`. |
| `CancelAppointment` | `POST /appointments/{appointmentId}/cancel` | Cancels a `Booked` appointment. `200`, `DockAppointmentCancelled`. `409 appointment-not-booked` otherwise. |
| `OpenReceipt` | `POST /receipts` (`Idempotency-Key` required) | In one unit of work: loads the ASN (`422 unknown-asn`), validates the optional appointment (`422 unknown-appointment`, `422 asn-not-on-appointment`, `409 appointment-not-checked-in`) and takes its door, opens the receipt against the ASN snapshot, requires no open receipt for the ASN (`409 receipt-already-open`), moves the ASN to `Receiving` and saves both. `201`, `ReceiptOpened`. |
| `ReceiveLine` | `POST /receipts/{receiptId}/lines` (`Idempotency-Key` required, not naturally idempotent) | Loads the receipt, checks `If-Match`, records the quantity as `Good` or `Damaged`. `201`, `ReceiptLineReceived`. `409 receipt-closed`, `422 line-not-on-asn`, `400 invalid-condition`. |
| `CloseReceipt` | `POST /receipts/{receiptId}/close` | In ONE unit of work: closes the receipt (computing the discrepancies), completes the ASN and, when the receipt came from an appointment, completes the appointment. `200`, `ReceiptClosed` and `DockAppointmentCompleted`. `409 receipt-closed`. |

## Queries

| Use case | Inbound | What it does |
| --- | --- | --- |
| `GetAsn` | `GET /asns/{asnNumber}` | One ASN with lines, state and version (also the `ETag`); `404 asn-not-found`. |
| `ListAsns` | `GET /asns` | Pages in ascending ASN-number order, optional `state` filter. |
| `GetAppointment` | `GET /appointments/{appointmentId}` | One appointment; `404 appointment-not-found`. |
| `ListAppointments` | `GET /appointments` | Pages by `windowStart` then id; filters `door`, `state`, and `from` / `to` (windows overlapping `[from, to)`). |
| `GetReceipt` | `GET /receipts/{receiptId}` | One receipt with the discrepancies as they stand now; `404 receipt-not-found`. |
| `ListReceipts` | `GET /receipts` | Pages by receipt id; filters `asnNumber`, `state`. |
| `ListDocks` | `GET /docks` | The known inbound dock doors in door-code order plus the current `DOCK_DOOR_MODE`. |

Lists use cursor paging: `limit` 1..500 (default 100) and the opaque `cursor`
that is the previous page's `nextCursor`; a bad value is `400 invalid-query`
(`usecases.ErrInvalidListQuery`).

## Event-driven use cases (local copies)

| Use case | Triggered by | What it does |
| --- | --- | --- |
| `ApplyProductRegistered` | `ProductRegistered` on `warehouse.product-master.events` | Claims the CloudEvents id (consumer `product-registry`) and upserts the SKU into `known_skus`, in one unit of work. Returns `applied` or `duplicate`; an invalid SKU is `ErrInvalidEvent`, claimed nothing. |
| `ApplyLocationSlotRegistered` | `LocationSlotRegistered` on `warehouse.facility.events` | Ignores anything but `role=Dock` with `dockFlow` `Inbound` or `Both` (`ignored`); otherwise claims the id (consumer `dock-door-registry`) and upserts the door. |
| `ApplyLocationSlotDecommissioned` | `LocationSlotDecommissioned` | Claims the id and removes the door; a code that is not a known door is a no-op success. |

## Cross-cutting behaviour

- **Idempotency.** `Idempotency-Key` on the creating `POST`s: a replay returns
  the original response, the same key with another body is `422
  idempotency-key-reused`. The action `POST`s honour the header when sent.
- **Optimistic concurrency.** Every save is guarded by the loaded `version`; a
  race is `409 concurrent-modification`, a stale `If-Match` is `412
  version-mismatch`.
- **Errors** are RFC 7807 problems from one table, `problemCatalogue` in
  `internal/adapters/inbound/http/errors.go`.

Acceptance scenarios are in `features/*.feature` (`register_asn`,
`dock_appointments`, `receiving`, `idempotent_replays`; godog,
`features_test.go`).
