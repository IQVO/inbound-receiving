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
| `inventory-storage` (Core) | `com.warehouse.wms.inbound-receiving.receipt.ReceiptLineReceived` | `INBOUND_RECEIPT_CONSUMER_GROUP` (unset = not started) | none | for `condition=Good` runs its existing ReceiveStock use case with `(sku, quantity)`, so the units are staged and Unlocated until stowed; `Damaged` lines are logged and counted (`inventory.inbound_receipt_units{outcome=damaged_not_booked}`), not booked; redelivery is deduped on the CloudEvents id | 0037 |

Evidence (on the consumer's `develop`): inventory-storage
`internal/adapters/inbound/kafka/inbound_receipt_consumer.go`,
`internal/application/usecases/book_inbound_receipt_line.go` and
`cmd/inventory/consumers.go`. The consumer reads `receipt_id`, `asn_number`,
`line_no`, `sku`, `quantity` and `condition` and ignores `received_at`
(inventory-storage stamps with its own clock). A brand-new group starts at the
earliest offset. Every other type on the topic is committed past untouched.

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

## Planned, not built

| Candidate consumer | Event | State |
| --- | --- | --- |
| `warehouse-planning` | `DockAppointmentBooked` as inbound-labor demand | **Planned, not built.** Its CapacityPlan has no inbound process path, so this needs its own ADR there first (ADR 0001, ADR 0003). |

## Events with no consumer yet

`ASNRegistered`, `ASNCancelled`, `DockAppointmentBooked`,
`DockAppointmentCheckedIn`, `DockAppointmentCancelled`,
`DockAppointmentCompleted`, `ReceiptOpened` and `ReceiptClosed` are published but
no sibling consumes them on `develop` today. All nine events are also copied to
the internal analytics topic, whose only consumer is this context's own
`inbound-projector` ([ADR 0006](/docs/adr/0006-analytics-read-side)); it is not
an integration contract.
