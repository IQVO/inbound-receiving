# ADR 0001: inbound-receiving as the owner of the inbound dock workflow

## Status

Accepted (2026-10-08).

## Context

The fleet has nothing between "a truck is announced" and "units are staged in
inventory-storage". `inventory-storage`'s `POST /stock/receive` is a bare
quantity acknowledgement: it does not know which delivery the units belong
to, whether the delivery was announced, who booked which door, or what was
short, over or damaged. The 2026-10-08 competitor review (Manhattan Active
SCE, Blue Yonder WMS, SAP EWM, Infor WMS; Gartner's 2026 WMS Magic Quadrant
treating inbound execution as expected standard capability) puts the
announce -> appoint -> receive -> reconcile chain squarely inside a WMS.

Receiving is a transactional document workflow: an announcement, a booked
window, a counted receipt. It changes for different reasons and at a
different cadence than the stock ledger (which is about "how many, where")
and than layout (which is about "what exists physically"). Folding it into
`inventory-storage` would couple supplier-facing document rules to the
ledger's availability and release cadence.

## Decision

Introduce `inbound-receiving` as a new bounded context that owns the inbound
dock workflow up to, and not including, stock placement.

### Classification

- Strategic: **Supporting subdomain**. Every warehouse needs it and the rules
  (what counts as a discrepancy, how long a door window may be) differ per
  retailer, but it is not where this platform differentiates. Policy objects
  (appointment window, discrepancy kinds) are replaceable.
- Tier: **WMS** ("what and where"). CloudEvents subdomain `wms`, the fourth
  `wms` context after `facility-layout`, `inventory-storage` and
  `product-master` (user decision 2026-10-08, together with
  `slotting-optimization` as the fifth). Receiving books units into the WMS
  stock ledger; it is not task execution.
- Type prefix: `com.warehouse.wms.inbound-receiving.<entity>.<EventName>`,
  source `/warehouse/inbound-receiving`.

### The model (v1)

Three aggregates, each its own consistency boundary (ADR 0002): `Asn` (the
supplier's announcement), `DockAppointment` (a carrier's booked door window)
and `Receipt` (the counted physical receiving of one ASN). They reference each
other by id, never by object. A snapshot of the ASN is copied into the
Receipt when it is opened.

### Context map

| Relationship | Pattern |
|---|---|
| product-master -> inbound-receiving | Conformist on `ProductRegistered` (`warehouse.product-master.events`); local copy `known_skus` answers "does this SKU exist" (ADR 0003) |
| facility-layout -> inbound-receiving | Conformist on `LocationSlotRegistered` / `LocationSlotDecommissioned` (`warehouse.facility.events`); local copy `dock_doors` answers "is this an inbound dock door" (ADR 0003) |
| inbound-receiving -> inventory-storage | Published Language (`ReceiptLineReceived`, `warehouse.inbound-receiving.events`); inventory-storage consumes Good lines into its existing ReceiveStock use case. Event-driven, no synchronous call in either direction (ADR 0003) |
| inbound-receiving -> warehouse-planning | **Planned, not built.** warehouse-planning would consume `DockAppointmentBooked` as inbound-labor demand. It needs an inbound process path in its CapacityPlan first, which is its own ADR there |
| inbound-receiving -> warehouse-ops-agent, warehouse-console | Open Host Service (REST, MCP read tools); later phases |

### Explicitly out of scope

- Yard and trailer management (gate, yard slots, trailer tracking): an
  appointment ends at the door.
- Returns (RMA receiving): a different document with a different source.
- Putaway directives: stow stays an RF action (item scan plus location scan)
  in `inventory-storage`; this context says nothing about where goods go.
- Quality inspection and quarantine: Damaged units are recorded on the receipt
  and not booked as receivable stock; a quarantine flow is a later ADR.
- Supplier master data, purchase orders, EDI parsing: an ASN arrives through
  the REST API already structured.

### No live cross-context lookup, in either direction

- `inbound-receiving` never calls a sibling context at request time. SKU and
  dock-door checks read local copies fed by events.
- No context calls `inbound-receiving` at request time either; the REST `GET`
  endpoints exist for operators, the console and agents.

## Consequences

- A new deployable (API, Postgres database, Kafka topic, Kong route) joins the
  fleet. `warehouse-infra` wires it by hand (`local.services`) in a later wave.
- inventory-storage gains a new consumer; its own ADR records its side.
- The fleet CloudEvents rule "wms is three contexts" changes to name five
  (this one and `slotting-optimization`). The managed fleet rule and the
  warehouse-docs subdomain table change in the same wave.
- Until the local copies are authoritative (`PRODUCT_MODE` and
  `DOCK_DOOR_MODE` are `permissive` by default), a typo SKU is accepted on an
  ASN. That is a deliberate fail-open, see ADR 0003.
