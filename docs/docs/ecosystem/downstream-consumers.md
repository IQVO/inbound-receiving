---
id: downstream-consumers
title: Downstream consumers
sidebar_position: 1
---

# Downstream consumers

`inbound-receiving` publishes its nine events on
`warehouse.inbound-receiving.events`. None of its consumers calls this service
at request time: each keeps what it needs from the events (ADR 0001, ADR 0003).
The table reflects each consumer's code on its `develop` branch.

| Consumer | Type consumed | Consumer group env | Mode switch | What it does with it | Its ADR |
| --- | --- | --- | --- | --- | --- |
| `inventory-storage` (Core) | `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | `INBOUND_RECEIPT_CONSUMER_GROUP` (required, boot error if unset: its ADR 0042) | none | for `condition=Good` runs its existing ReceiveStock use case with `(sku, quantity)`, so the units are staged and Unlocated until stowed; `Damaged` lines are logged and counted (`inventory.inbound_receipt_units{outcome=damaged_not_booked}`), not booked; redelivery is deduped on the CloudEvents id | 0037 |
| `warehouse-planning` (Core, `wes`) | `...dockappointment.DockAppointmentBooked` and `...DockAppointmentCancelled` | `INBOUND_DEMAND_CONSUMER_GROUP` (required there; warehouse-infra pins `warehouse-planning-inbound-demand`) | none | upserts one `inbound_appointments` row per `appointment_id` at its configured site (`DEMAND_SITE_ID`) as inbound-labour demand; last writer wins on the CloudEvents `time`, a Cancelled wins a tie and a Cancelled that arrives before its Booked is kept as a tombstone; failures are retried 5 times, deterministic ones are dead-lettered to `warehouse.inbound-receiving.events.dlq` | 0017 (warehouse-planning) |

Evidence (on the consumer's `develop`): inventory-storage
`internal/adapters/inbound/kafka/inbound_receipt_consumer.go`,
`internal/application/usecases/book_inbound_receipt_line.go`,
`cmd/inventory/config.go` and `cmd/inventory/consumers.go`; warehouse-planning
`internal/adapters/inbound/kafka/inbound_demand_consumer.go`,
`cmd/api/runtime.go` and `docs/adr/0017-inbound-labour-demand-and-observed-capacity-feedback.md`.
The inventory-storage consumer reads `receipt_id`, `asn_number`,
`line_no`, `sku`, `quantity` and `condition` and ignores `received_at`
(inventory-storage stamps with its own clock). A brand-new group starts at the
earliest offset. Every other type on the topic is committed past untouched.

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
| `warehouse-ops-agent` | The seven read-only MCP tools of `cmd/mcp` ([ADR 0005](/docs/adr/0005-mcp-server-adoption)) | **Live**, opt-in by `INBOUND_RECEIVING_MCP_ENDPOINT` on the agent side (unset = disabled, fail-open) | its ADR 0021, `cmd/agent/main.go` |
| `warehouse-console` | The `inbound_mfe` remote from `web/`, mounted at `/inbound-receiving/*`, calling REST through Kong | **Live** | its `vite.config.ts` (remote `inbound_mfe`, dev port 5193) and `src/App.tsx` |

These read this context; they do not consume its events.

## Nothing is planned and unbuilt

There is no candidate consumer left. The `warehouse-planning` edge that earlier
versions of this page listed as planned (ADR 0001, ADR 0003) is built, merged
and running: warehouse-planning ADR 0017 added the consumer
(`internal/adapters/inbound/kafka/inbound_demand_consumer.go`) and
`helm-values/warehouse-planning.yaml` in warehouse-infra sets
`inbound.consumerGroup`. The earlier statement that its CapacityPlan had no
inbound process path is superseded by that ADR.

## Events with no consumer yet

`ASNRegistered`, `ASNCancelled`, `DockAppointmentCheckedIn`,
`DockAppointmentCompleted`, `ReceiptOpened` and `ReceiptClosed` are published but
no sibling consumes them on `develop` today (`warehouse-planning` reads only
`DockAppointmentBooked` and `DockAppointmentCancelled`; its ADR 0017 lists
`ASNRegistered`, `DockAppointmentCheckedIn` and `DockAppointmentCompleted` as not
consumed). All nine events are also copied to
the internal analytics topic, whose only consumer is this context's own
`inbound-projector` ([ADR 0006](/docs/adr/0006-analytics-read-side)); it is not
an integration contract.
