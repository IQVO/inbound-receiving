---
id: downstream-consumers
title: Downstream consumers
sidebar_position: 1
---

# Downstream consumers

`inbound-receiving` publishes on `warehouse.inbound-receiving.events`: eight
types (`ASNRegistered`, `ASNCancelled`, `ReceiptOpened`,
`ReceiptLineReceived`, `ReceiptCartonReceived`, `CartonDiscrepancyRaised`,
`ReceiptClosed`, `ReceiptStowCompleted`) and no longer the four legacy `DockAppointment*` types ([ADR 0019](/docs/adr/0019-dockappointment-moves-to-dock-scheduling),
retired from the contract by [ADR 0022](/docs/adr/0022-stage-e-removes-the-backfill-the-legacy-encoders-and-replay)). None of its
consumers calls this service at request time: each keeps what it needs from
the events (ADR 0001, ADR 0003). The table reflects each consumer's code on its
`develop` branch (verified 2026-11-09 by a literal scan of every fleet repo's
non-test Go for `com.warehouse.wms.inbound-receiving.*` types).

| Consumer | Types consumed | Consumer group env | What it does with them | Its ADR |
| --- | --- | --- | --- | --- |
| `inventory-storage` (Core) | `receipt.ReceiptLineReceived`, `receipt.ReceiptCartonReceived` | `INBOUND_RECEIPT_CONSUMER_GROUP` (required, boot error if unset: its ADR 0042; warehouse-infra pins `inventory-storage-inbound-receipts`) | for `condition=Good` lines runs its ReceiveStock use case, so the units are staged and Unlocated until stowed; `Damaged` lines are counted (`inventory.inbound_receipt_units{outcome=damaged_not_booked}`), not booked; an accepted carton becomes a sealed container (LPN) with its cases; redelivery is deduped on the CloudEvents id | 0037, 0045, 0046 |
| `network-fulfillment` (fleet side of the retail network) | `receipt.ReceiptCartonReceived`, `receipt.CartonDiscrepancyRaised`, `receipt.ReceiptClosed` | `INBOUND_RECEIPT_CONSUMER_GROUP` (warehouse-infra pins `network-fulfillment-inbound-receiving`) | closes the seller inbound loop: the receipt of a seller shipment (cartons accepted, their findings, the closing line kinds) goes back to the vendor as the shipment's receipt with its discrepancies | 0028 |
| `exception-management` | `receipt.CartonDiscrepancyRaised` | `CARTON_DISCREPANCY_CONSUMER_GROUP` | opens and routes one exception case per carton finding | 0006 |
| `yard-management` | `asn.ASNRegistered`, `asn.ASNCancelled`, `receipt.ReceiptOpened`, `receipt.ReceiptClosed` | `INBOUND_RECEIVING_CONSUMER_GROUP` | keeps a local copy of expected inbound ASNs and of receipts per door; a closed receipt auto-completes the unload at the door (AutoCompleteUnload) | 0003, 0009 |
| `dock-scheduling` | `asn.ASNRegistered`, `asn.ASNCancelled` | `ASN_CONSUMER_GROUP` | ASN copy (an appointment can only cover a known, not-cancelled ASN; CANCELLED is sticky); its legacy importer of the `dockappointment` types was removed (dock-scheduling ADR 0011) | 0003, 0009 |

`receipt.ReceiptStowCompleted` (the dock-to-stock fact, [ADR 0021](/docs/adr/0021-dock-to-stock-kpi-from-yard-arrival-and-inventory-stow-facts))
has **no consumer yet** outside this service's own projector, which folds its
analytics copy into `GET /reports/receiving-performance`.

`warehouse-planning` no longer reads this topic: its inbound labour demand
comes from dock-scheduling's appointments (its ADR 0020, strangler stage D),
and warehouse-infra retired its old `INBOUND_DEMAND_CONSUMER_GROUP`.

Evidence (each consumer's `develop`): inventory-storage
`internal/adapters/inbound/kafka/inbound_receipt_consumer.go`; network-fulfillment
`internal/adapters/inbound/kafka/receipt_consumer.go`; exception-management
`internal/adapters/inbound/kafka/carton_discrepancy_consumer.go` and
`internal/application/usecases/carton_discrepancy.go`; yard-management
`internal/adapters/inbound/kafka/inbound_receiving_consumer.go`; dock-scheduling
`internal/adapters/inbound/kafka/asn_consumer.go`; warehouse-planning
`internal/adapters/inbound/kafka/dock_appointment_consumer.go`. A brand-new
group starts at the earliest offset. Every consumer commits past the types it
does not act on.

Every message also carries the optional W3C `traceparent` (and `tracestate` /
`baggage` when present) of the request that wrote the outbox row
([ADR 0009](/docs/adr/0009-trace-context-crosses-the-outbox)); a consumer that
opens a span can `Extract` it to continue that trace. Messages relayed from rows
written without an active span carry only `content-type`.

Classifications of neighbours come from the warehouse-docs contexts table.

## Handover semantics

- **Event-driven, never synchronous.** No REST or MCP call goes between
  `inbound-receiving` and `inventory-storage` in either direction and there is
  no acknowledgement event back.
- **Good only.** `Damaged` units are recorded on the receipt and counted in its
  discrepancies, but not booked as receivable stock. Quarantine is a later ADR.
- **Stow stays an RF action** (item scan plus location scan in
  `inventory-storage`); this context prescribes no location.
- **Failure on the far side** (for example inventory-storage rejects the SKU) is
  that consumer's skip-and-log. This context has no compensation flow in v1; the
  receipt's counts remain the source of truth for the dock.

## Read-side consumers (not event-driven)

| Consumer | Surface | State | Evidence (on its `develop`) |
| --- | --- | --- | --- |
| `warehouse-ops-agent` | The seven read-only MCP tools of `cmd/mcp` ([ADR 0005](/docs/adr/0005-mcp-server-adoption), authority L1 per [ADR 0018](/docs/adr/0018-agent-authority-levels-and-complete-mcp-surface)) | **Live**, opt-in by `INBOUND_RECEIVING_MCP_ENDPOINT` on the agent side (unset = disabled, fail-open) | its ADR 0021, `cmd/agent/main.go`; its pinned copy of this context's tool registry golden |
| `warehouse-console` | The `inbound_mfe` remote from `web/`, mounted at `/inbound-receiving/*`, calling REST through Kong | **Live** | its `vite.config.ts` (remote `inbound_mfe`, dev port 5193) and `src/App.tsx` |
| `warehouse-floor-client` | REST: `GET /receipts`, `GET /receipts/{receiptId}`, `POST /receipts/{receiptId}/scans`, `POST /receipts/{receiptId}/closures`, `GET /receipts/{receiptId}/cartons/{lpn}/label` (the RF receiving screens) | **Live** | its `src/api/inboundReceiving.ts` and `src/features/receiving/` |

These read this context (the floor client also drives receipts); they do not
consume its events.

## Nothing is planned and unbuilt

There is no candidate consumer left that has an ADR. Business follow-ups
(trailer holds and seal exceptions gating a receipt, inbound KPIs on the
dock-scheduling and yard facts) are planned in the fleet's inbound evolution
plan, not here.

## Events with no consumer yet

Seven of the eight current types have at least one consumer (the table above);
`ReceiptStowCompleted` has none yet (see above). The four retired
`DockAppointment*` types have none: dock-scheduling's importer is removed
(its ADR 0011) and this context no longer declares them (ADR 0022).
All types are also copied to the internal analytics topic, whose only consumer
is this context's own `inbound-projector`
([ADR 0006](/docs/adr/0006-analytics-read-side)); it is not an integration
contract.
