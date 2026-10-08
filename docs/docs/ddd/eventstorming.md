---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
sidebar_position: 16
---

# EventStorming

Design-level EventStorming following the ddd-crew
[EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet).
This is a reconstruction from the code on `develop`, not a workshop output.

Sticky colours: **Command** blue, **Aggregate** yellow, **Domain event** orange,
**Policy** lilac, **Read model** green, **External system** pink, **Hotspot**
red.

```mermaid
flowchart LR
  classDef cmd fill:#bfdbfe,stroke:#2563eb,color:#1e3a8a
  classDef agg fill:#fef08a,stroke:#ca8a04,color:#713f12
  classDef evt fill:#fed7aa,stroke:#ea580c,color:#7c2d12
  classDef pol fill:#e9d5ff,stroke:#9333ea,color:#581c87
  classDef rm fill:#bbf7d0,stroke:#16a34a,color:#14532d
  classDef ext fill:#fbcfe8,stroke:#db2777,color:#831843
  classDef hot fill:#fecaca,stroke:#dc2626,color:#7f1d1d

  C1[RegisterAsn]:::cmd --> A1((Asn)):::agg --> E1[ASNRegistered]:::evt
  C2[CancelAsn]:::cmd --> A1 --> E2[ASNCancelled]:::evt
  C3[BookAppointment]:::cmd --> A2((DockAppointment)):::agg --> E3[DockAppointmentBooked]:::evt
  C4[CheckInAppointment]:::cmd --> A2 --> E4[DockAppointmentCheckedIn]:::evt
  C5[CancelAppointment]:::cmd --> A2 --> E5[DockAppointmentCancelled]:::evt
  C6[OpenReceipt]:::cmd --> A3((Receipt)):::agg --> E6[ReceiptOpened]:::evt
  C7[ReceiveLine]:::cmd --> A3 --> E7[ReceiptLineReceived]:::evt
  C8[CloseReceipt]:::cmd --> A3 --> E8[ReceiptClosed]:::evt
  E8 --> P1{{"on close: complete the ASN and the appointment"}}:::pol
  P1 --> E9[DockAppointmentCompleted]:::evt

  X1[product-master]:::ext --> E10[ProductRegistered]:::evt --> P2{{"upsert known_skus"}}:::pol --> R1[(known_skus)]:::rm
  X2[facility-layout]:::ext --> E11[LocationSlotRegistered or Decommissioned]:::evt --> P3{{"keep only inbound dock doors"}}:::pol --> R2[(dock_doors)]:::rm
  R1 -.-> C1
  R2 -.-> C3

  E7 --> X3[inventory-storage]:::ext
  X3 --> P4{{"book Good lines as staged stock"}}:::pol

  H1[/"Hotspot: no tolerance policy for over-receipt"/]:::hot -.-> E8
  H2[/"Hotspot: Damaged units have no quarantine flow"/]:::hot -.-> E7
```

Source: `internal/application/usecases/*.go`, `internal/domain/*/events.go`,
`internal/adapters/inbound/kafka/*.go`; inventory-storage
`internal/adapters/inbound/kafka/inbound_receipt_consumer.go`.
Omits: the internal `BeginReceiving` and `Complete` transitions of the ASN
(they raise no event), error branches and the planned consumers.

## Legend

| Sticky | Meaning here |
| --- | --- |
| Command | A use case in `internal/application/usecases`, reached by a REST `POST`. |
| Aggregate | `Asn`, `DockAppointment`, `Receipt` in `internal/domain`. |
| Domain event | A type in `apis/asyncapi.yaml`, raised by the aggregate and published through the outbox. |
| Policy | Reactive logic: `CloseReceipt` completing the ASN and appointment (in the same unit of work), the two local-copy consumers, and inventory-storage's consumer. |
| Read model | The two local-copy tables `known_skus` and `dock_doors`; they are read by `RegisterAsn` / `BookAppointment` only when the mode is `kafka`. |
| External system | `product-master`, `facility-layout`, `inventory-storage`. |

## Hotspots

Taken from real gaps in the ADRs, not invented:

1. **No tolerance policy.** v1 reports every difference; accepting up to x %
   over-receipt silently is "a later, separately decided change" (ADR 0002).
2. **Damaged units have no quarantine flow.** They are recorded and counted but
   not booked as stock; "a quarantine flow is a later ADR" (ADR 0001, ADR 0003).
3. **No compensation on the far side.** If `inventory-storage` rejects a line it
   skips and logs; the receipt's counts stay the source of truth (ADR 0003).
4. **Fail-open local copies.** In `permissive` mode a typo SKU or door is
   accepted (ADR 0001, Consequences).
5. **`DockAppointmentCompleted` ordering.** Keyed by `appointment_id`, so its
   order relative to `ReceiptClosed` is not guaranteed (ADR 0004).
