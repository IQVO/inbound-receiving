---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 2
---

# Use cases

:::caution[DockAppointment moved (ADR 0019)]
The dock appointment is owned by dock-scheduling since [ADR 0019](/docs/adr/0019-dockappointment-moves-to-dock-scheduling). This context keeps a read-only local copy fed by dock-scheduling's `DockAppointmentBooked` / `DockAppointmentCancelled`, no longer books, checks in, cancels or completes appointments, and no longer raises the four `DockAppointment*` events (the one-shot `api republish-appointments` backfill that re-announced them was removed, [ADR 0022](/docs/adr/0022-stage-e-removes-the-backfill-the-legacy-encoders-and-replay)). Where this page describes those commands, rules or events, read it as the pre-transfer design; the page is not yet rewritten.
:::

Application services in `internal/application/usecases`. The six REST write use
cases (`RegisterAsn`, `CancelAsn`, `OpenReceipt`, `ReceiveLine`, `ReceiveScan`,
`CloseReceipt`) embed `Writer` (`writer.go`), which runs load, command,
version-guarded save and outbox insert in one `ports.UnitOfWork`; so do the two
stow consumer use cases. The eleven consumer use cases (`consumers*.go`) claim the
CloudEvents id and apply the effect in one unit of work. The three appointment write
use cases (`BookAppointment`, `CheckInAppointment`, `CancelAppointment`) no longer
exist: their routes answer `410 appointment-moved` (ADR 0019).

## Commands

| Use case | Inbound | What it does |
| --- | --- | --- |
| `RegisterAsn` | `POST /asns` (`Idempotency-Key` required) | Builds the ASN (`asn.Register`), with `PRODUCT_MODE=kafka` checks every line's SKU against `known_skus` (`422 unknown-sku`), then saves it unless the number exists (`409 asn-already-exists`). `201`, `ASNRegistered`. |
| `CancelAsn` | `POST /asns/{asnNumber}/cancellations` | Loads the ASN, checks `If-Match`, cancels a `Registered` one. `200`, `ASNCancelled`. `409 asn-in-progress` while `Receiving`, `409 asn-terminal` when `Closed` or `Cancelled`. |
| `OpenReceipt` | `POST /receipts` (`Idempotency-Key` required) | In one unit of work: loads the ASN (`422 unknown-asn`), validates the optional appointment against the local copy (`422 unknown-appointment`, `422 asn-not-on-appointment`, `409 appointment-cancelled`) and takes its door, opens the receipt against the ASN snapshot, requires no open receipt for the ASN (`409 receipt-already-open`), moves the ASN to `Receiving` and saves both. `201`, `ReceiptOpened`. |
| `ReceiveLine` | `POST /receipts/{receiptId}/lines` (`Idempotency-Key` required, not naturally idempotent) | Loads the receipt, checks `If-Match`, records the quantity as `Good` or `Damaged`. `201`, `ReceiptLineReceived`. `409 receipt-closed`, `422 line-not-on-asn`, `400 invalid-condition`. |
| `ReceiveScan` | `POST /receipts/{receiptId}/scans` (`Idempotency-Key` required) | Receives by scan: an SSCC accepts the carton's manifest, a case GTIN books cases x units per pack, an each GTIN books eaches. A refused scan is not an error: it commits the finding and books nothing. `ReceiptLineReceived` and `ReceiptCartonReceived` for an accepted scan, `CartonDiscrepancyRaised` per finding. |
| `CloseReceipt` | `POST /receipts/{receiptId}/closures` | In ONE unit of work: closes the receipt (computing the discrepancies), completes the ASN and evaluates the dock-to-stock fact. `200`, `CartonDiscrepancyRaised` (a `MissingCarton` per manifest carton never accepted), `ReceiptClosed` and, when the evidence is complete, `ReceiptStowCompleted`. `409 receipt-closed`. It no longer completes the appointment (ADR 0019). |

## Queries

| Use case | Inbound | What it does |
| --- | --- | --- |
| `GetAsn` | `GET /asns/{asnNumber}` | One ASN with lines, state and version (also the `ETag`); `404 asn-not-found`. |
| `ListAsns` | `GET /asns` | Pages in ascending ASN-number order, optional `state` filter. |
| `GetAsnCartons` | `GET /asns/{asnNumber}/cartons` | The announced cartons with their receive status. |
| `GetAppointment` | `GET /appointments/{appointmentId}` | One appointment from the local copy (**deprecated**, kept pending a decision, ADR 0022); `404 appointment-not-found`. |
| `ListAppointments` | `GET /appointments` | (**deprecated**, same) Pages by `windowStart` then id; filters `door`, `state`, and `from` / `to` (windows overlapping `[from, to)`). |
| `GetCartonLabel` | `GET /receipts/{receiptId}/cartons/{lpn}/label` | The label of one received carton. |
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
| `ApplyPackLevelDeclared`, `ApplyPackLevelRetired` | `ProductPackLevelDeclared` / `ProductPackLevelRetired` on `warehouse.product-master.events` | Keep the packaging view used by case-level scans. |
| `ApplyDockAppointmentBooked`, `ApplyDockAppointmentCancelled`, `ApplyDockAppointmentRescheduled`, `ApplyDockAppointmentDoorReassigned` | dock-scheduling's `DockAppointment*` types on `warehouse.dock-scheduling.events` | Keep the `appointments` local copy, guarded by the event `version`; raise no event (ADR 0019, ADR 0020). |
| `ApplyVisitCheckedIn`, `ApplyContainerStowed` | `VisitCheckedIn` on `warehouse.yard-management.events`, `ContainerStowed` on `warehouse.inventory.events` | Store the arrival or the stow, then evaluate the dock-to-stock fact and may raise `ReceiptStowCompleted` (ADR 0021). |

## Analytics read side (outside `internal/application`)

The analytics binaries do not use the application layer; their "use cases"
are the ports in `internal/analytics/report`
([ADR 0006](/docs/adr/0006-analytics-read-side)):

| Port | Trigger | What it does |
| --- | --- | --- |
| `report.Projection.Apply` | the events on `warehouse.inbound-receiving.analytics` it knows (eleven kinds: seven of the ten current types, including `ReceivingAdviceIssued`, plus the four historical `dockappointment` types, ADR 0022; the two carton types and `ReceivingAdviceOverdue` are ignored), consumed by `cmd/inbound-projector` | Claims the CloudEvents id in `analytics_processed_events` and folds the event into `asn_facts`, `appointment_facts`, `receipt_facts`, `receipt_line_events` or `receipt_discrepancies` in one analytical transaction. A deterministic rejection (`report.ErrRejected`) is dead-lettered to `warehouse.inbound-receiving.analytics.dlq`. |
| `report.Reader.PerformanceDays` + `Current` | `GET /reports/receiving-performance` on `cmd/inbound-reports` | Per-UTC-day counts and p50/p95 timings over `[from, to)` (default the last 30 days, at most 366) plus today's open receipts and expected arrivals. |
| `report.Reader.LastEventAt` | `GET /reports/freshness` | `as_of` and `lag_seconds` of the projection. |

Response shapes: [Runbook — Reports API](../operations/runbook.md#reports-api-cmdinbound-reports).

## Cross-cutting behaviour

- **Idempotency.** `Idempotency-Key` on every `POST`: a replay returns
  the original response (`Idempotency-Replayed: true`), the same key with another request is `409 idempotency-key-reused`.
- **Optimistic concurrency.** Every save is guarded by the loaded `version`; a
  race is `409 concurrent-modification`, a stale `If-Match` is `412
  version-mismatch`.
- **Errors** are RFC 7807 problems from one table, `problemCatalogue` in
  `internal/adapters/inbound/http/errors.go`.

Acceptance scenarios are in `features/*.feature` (7 files, 71 scenarios:
`register_asn`, `dock_appointments`, `receiving`, `idempotent_replays`,
`list_receipts_and_docks`, `dock_door_registry`, `probes`; godog,
`features_test.go` and `features_wave2_test.go`). See
[Testing](../development/testing.md).
