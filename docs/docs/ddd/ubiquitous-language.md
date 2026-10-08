---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
sidebar_position: 1
---

# Ubiquitous language

Every term the inbound dock uses, mapped to the identifier that implements it.
Code identifiers are from `internal/domain/*`, wire names from
`apis/openapi.yaml` (camelCase) and `apis/asyncapi.yaml` (snake_case).

## Documents

| Term | Meaning | Code / wire identifier |
| --- | --- | --- |
| **ASN** (advance ship notice) | The supplier's announcement of a delivery and its lines. | `asn.Asn`, `POST /asns`, event `ASNRegistered` |
| **ASN number** | The ASN's identity: 1..64 characters of `[A-Za-z0-9._-]`. | `asn.Number`, `asnNumber` / `asn_number` |
| **Supplier reference** | The supplier's own reference for the delivery. | `supplierRef` / `supplier_ref` |
| **Expected arrival** | Optional announced arrival instant. | `expectedArrival` / `expected_arrival` |
| **Line** | One SKU and an expected quantity on an ASN; numbered `1..n`. | `asn.Line`, `lineNo` / `line_no` |
| **Expected quantity** | What the supplier announced for a line. | `expectedQty` / `expected_qty` |
| **Dock appointment** | A carrier's booked window at one door, covering one or more ASNs. | `appointment.DockAppointment`, `appt-<uuid>` |
| **Receipt** | The counted physical receiving of one ASN. | `receipt.Receipt`, `rcpt-<uuid>` |

## Dock vocabulary

| Term | Meaning | Code / wire identifier |
| --- | --- | --- |
| **Door** / **dock door** | A facility-layout slot with role Dock where trucks are worked. | `appointment.DoorCode`, `doorCode` / `door_code` |
| **Inbound dock door** | A door whose `dockFlow` is `Inbound` or `Both`. | `dock_doors` table, `GET /docks` |
| **Window** | The half-open interval `[start, end)` a door is booked for; at most 4 hours. | `appointment.Window`, `MaxWindow` |
| **Carrier** | The haulier the window is booked for. | `carrier` |
| **Check-in** | The carrier's arrival at the door, allowed from 30 minutes before the window until it ends. | `CheckIn`, `CheckInLeadTime` |
| **Walk-in** | A delivery received without an appointment. | `OpenReceiptCommand.AppointmentID == ""` |
| **Schedule** | The domain service that refuses overlapping active windows on one door. | `appointment.Schedule.CheckNoOverlap` |

## Receiving vocabulary

| Term | Meaning | Code / wire identifier |
| --- | --- | --- |
| **Snapshot** | The read-only copy of an ASN a receipt is opened against. | `asn.Snapshot`, `Asn.Snapshot()` |
| **Receiving** | The ASN state while a receipt is open. | `asn.Receiving` |
| **Receive a line** | Record a quantity against a line. | `Receipt.ReceiveLine`, `POST /receipts/{receiptId}/lines` |
| **Condition** | Whether received units are fit to stock: `Good` or `Damaged`. | `receipt.Condition` |
| **Good / Damaged** | The two counters of a receipt line. | `receivedGood`, `receivedDamaged` |
| **Received quantity** | Good plus damaged. | `Line.Received()`, `received_qty` |
| **Over-receipt** | More units than expected; accepted and reported. | discrepancy kind `Over` |
| **Discrepancy** | A difference between expected and received found on close. | `receipt.Discrepancy`, `discrepancies[]` |
| **Short** / **Over** / **Damaged** | The three discrepancy kinds; one line can carry several. | `receipt.KindShort`, `KindOver`, `KindDamaged` |
| **Handover** | Publishing each received line so inventory-storage books the Good units. | `ReceiptLineReceived` |

## Platform vocabulary

| Term | Meaning | Code / wire identifier |
| --- | --- | --- |
| **Version** | Starts at 1, +1 per accepted change; the strong `ETag` and the `If-Match` value. | `Version()`, `ETag` |
| **Idempotency key** | Caller-supplied key that makes a `POST` replay-safe. | `Idempotency-Key`, `idempotency_keys` |
| **Local copy** | A table built from a producer's events so no live lookup is needed. | `known_skus`, `dock_doors` |
| **Mode** | Whether a local copy is enforced (`kafka`) or ignored (`permissive`). | `usecases.Mode`, `PRODUCT_MODE`, `DOCK_DOOR_MODE` |
| **Outbox** | Table of already-encoded events written in the same transaction as the change. | `outbox_events` |
| **Processed event** | A consumer's claim of a CloudEvents `id`, for dedupe. | `processed_events` |

## Words that mean something else elsewhere

| Word | Here | Elsewhere in the fleet |
| --- | --- | --- |
| **Receive** | Count units at the dock against an ASN line. | In `inventory-storage`, `ReceiveStock` stages units in stock; this context hands it the Good lines. |
| **Door** | A facility-layout slot with role Dock. | In `facility-layout` it is a `LocationSlot`. |
| **Location** | Not used: this context never says where goods go. | `inventory-storage` / `facility-layout` own placement. |
