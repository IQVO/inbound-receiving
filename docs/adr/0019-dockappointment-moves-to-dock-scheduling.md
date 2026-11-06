# ADR 0019: The DockAppointment moves to dock-scheduling (strangler stages B and C)

## Status

Accepted (2026-11-05). Companion of dock-scheduling's ADR 0002 (aggregates,
invariants) and ADR 0003 (consumed contracts and local copies), and part of
plan YD-0002. Amends [ADR 0002](./0002-aggregates-and-invariants.md) (the
`DockAppointment` aggregate and its invariants leave this context),
[ADR 0003](./0003-local-copies-and-handover.md) (a third local copy) and
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (the four
`dockappointment` types are legacy). Does not edit those ADRs: it supersedes the
statements named below.

## Context

A dock appointment is a promise about a door and a time window. Deciding it
needs a calendar (opening hours, closures, tolerance, overlap rules) and the
yard's view of who is at the gate; inbound-receiving has neither. It owned the
aggregate only because it was the first context to need one. The new
dock-scheduling context owns the calendar, and yard-management owns arrivals
(`POST /visits`), door occupation and release.

The move follows the fleet's strangler pattern for moving an aggregate between
contexts (see the inventory-storage to product-master move of the SKU
classification, ADR 0034 there):

- **A.** dock-scheduling runs a legacy importer on this context's topic (done
  there, not here).
- **B.** this context re-announces its history so the importer can rebuild
  every appointment.
- **C.** this context stops deciding appointments and keeps a read-only copy.
- **D.** readers (warehouse-planning) switch to dock-scheduling's events.
- **E.** decommission after cluster verification.

This ADR covers **B and C**. What this context still needs from an appointment
is small: when a receipt is opened against one, it must exist, cover the ASN
and not be cancelled, and the receipt takes its door code from it.

## Decision

1. **dock-scheduling owns the DockAppointment.** `POST /appointments`,
   `POST /appointments/{appointmentId}/check-ins` and
   `POST /appointments/{appointmentId}/cancellations` answer **410** with the
   problem type `appointment-moved`; the `detail` names the operation to call
   in dock-scheduling (booking, cancelling) or yard-management (the gate
   check-in, from which dock-scheduling derives the appointment's check-in).
   The routes sit outside the `Idempotency-Key` middleware, so a client is told
   where to go whatever it sent; the OpenAPI marks them `x-retired: true` and
   the REST guideline lint treats them accordingly. The use cases
   (`BookAppointment`, `CheckInAppointment`, `CancelAppointment`), the id
   generator for appointments and the per-door overlap rule
   (`Schedule.CheckNoOverlap` and its advisory lock) are deleted: nothing calls
   them. This context raises no `DockAppointment*` event any more.
   `CloseReceipt` no longer completes the appointment nor raises
   `DockAppointmentCompleted`: the door release recorded in yard-management does
   that, through dock-scheduling. `ReceiptClosed` and every other receipt and
   ASN event are unchanged. Walk-in receipts (no appointment) are unchanged.
   The MCP and A2A surfaces were read-only (ADR 0005, 0016, 0018): they keep
   `get_appointment` / `list_appointments`, now over the copy.

2. **`appointments` becomes a local copy.** The table and its ASN-coverage
   table stay and are fed by a new consumer, `DockAppointmentConsumer`
   (consumer name `dock-appointment-copy`), of dock-scheduling's
   `warehouse.dock-scheduling.events`. It acts on two types only,
   `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked`
   and `...DockAppointmentCancelled` (the payloads are restated in
   `apis/asyncapi.yaml`, never imported); every other type on the topic
   (`Rescheduled`, `NoShow`, closures) is committed past, and a payload that
   breaks a rule of this context is skipped with a WARN. Reschedules do not
   change ASN coverage and are not needed.
   - Only `direction=INBOUND` appointments are copied; an `OUTBOUND` one cannot
     carry a receipt.
   - **Version guard.** Both events carry dock-scheduling's aggregate
     `version`; an event is applied only if it is greater than the stored one.
     Redelivery, replay and reordering are harmless. Migration 0005 resets the
     existing rows to version 0 and drops the aggregate guard
     (`version >= 1`), so *any* dock-scheduling version overwrites a legacy
     row. A later Booked refreshes the data of a cancelled appointment but never
     revives it.
   - The CloudEvents `id` claim (`processed_events`) and the upsert commit in
     one unit of work; the offset is committed afterwards.
   - **No loop.** Applying an event writes nothing to the outbox: the use cases
     (`ApplyDockAppointmentBooked`, `ApplyDockAppointmentCancelled`) have no
     outbox, and a unit test, a BDD scenario and a real-Postgres test pin that
     the outbox stays empty.
   - The consumer **always runs**, like the three other local-copy consumers; an
     unset `DOCK_APPOINTMENT_CONSUMER_GROUP` is a boot error (chart value
     `config.dockAppointmentConsumerGroup`, required). There is no off switch.

3. **Receipts read the copy.** `OpenReceipt` with an `appointmentId` requires
   the appointment to exist (422 `unknown-appointment`), to cover the ASN (422
   `asn-not-on-appointment`) and not to be cancelled (409
   `appointment-cancelled`). It no longer requires `CheckedIn`: the arrival is a
   yard-management fact this context does not see. The receipt keeps the
   appointment's door code.

4. **GET stays, deprecated.** `GET /appointments` and
   `GET /appointments/{appointmentId}` read the copy, are marked `deprecated`,
   and go at stage E. The copy only holds `Booked` and `Cancelled`; `CheckedIn`
   and `Completed` survive only on legacy rows. `version` and `ETag` are
   dock-scheduling's.

5. **Stage B: the backfill command.** The main binary has a one-shot
   subcommand, `api republish-appointments` (e.g.
   `kubectl exec deploy/inbound-receiving -- ./api republish-appointments`):
   it needs only `DATABASE_URL` (no broker, no HTTP server, no consumer, no
   migrations: the running pod owns the schema), pages through every stored
   appointment in id order, and for each enqueues, through the **transactional
   outbox** and on the integration topic only, the legacy events its state
   implies: `DockAppointmentBooked`, then `DockAppointmentCheckedIn` for a
   `CheckedIn` or `Completed` row, then `DockAppointmentCancelled` or
   `DockAppointmentCompleted`. The running pod's relay publishes them. It prints
   the count. Timestamps are the row's own (booked, last change, and for a
   completed row the opening of its first receipt as the check-in moment). It
   does not write the analytics topic: re-announcing history to dock-scheduling
   is no new occurrence for this context's own analytics. It is idempotent in
   effect: dock-scheduling's importer is idempotent by `appointment_id`, and a
   second run only repeats messages. The legacy encoder mapping (`Encoder` for
   the integration topic, `AnalyticsEncoder` for the events already on the
   analytics stream) is kept for this command only, and the domain keeps a
   `Replay` that rebuilds a legacy row's event sequence; they go at stage E.
   Order: run it after dock-scheduling's importer is deployed and before
   routing traffic to dock-scheduling for booking.

## Consequences

- One owner decides appointments; the 410 makes a stale client fail loudly
  instead of writing to a store nobody reads.
- Receipts keep working without calling anyone: the copy is local and a
  receipt only needs door, coverage and cancelled-or-not. Between deploy and the
  first booking event, appointments created before the cutover are served from
  their legacy rows (version 0), which dock-scheduling's importer then
  confirms.
- A receipt can be opened on an appointment that dock-scheduling has
  rescheduled, no-showed or completed: those transitions are not consumed. Only
  a cancellation blocks a receipt. The door on the receipt is the one on the
  copy, which a Rescheduled event does not refresh.
- The analytics stream keeps the legacy `DockAppointment*` events already on it
  and the projector keeps counting them; no new ones are produced, so the
  appointments block of the receiving-performance report stops growing. Moving
  that report to dock-scheduling's events is a follow-up, not part of this ADR.
- Removing the per-door overlap rule and the advisory lock is safe only because
  nothing books here; dock-scheduling enforces overlap with its own constraint.
- The REST guideline lint gains one narrow exemption (`x-retired: true`: a
  retired operation documents a 410 and no success response, and needs no
  idempotency key); the success-response rule moves into `restGuideline.js` so
  live operations are still checked.

## Rollout and decommissioning

1. Deploy dock-scheduling with the legacy importer (stage A).
2. Merge and deploy this change: the migration runs at boot, the consumer joins
   its group, the writes answer 410. Set `DOCK_APPOINTMENT_CONSUMER_GROUP`
   (infra change in the same apply).
3. Run `api republish-appointments` and compare counts: appointments here ==
   dock-scheduling's `legacy-import` rows.
4. Stage D readers switch; stage E removes the deprecated GETs, the backfill
   command, the legacy encoders and `Replay`, and marks the old types retired.
