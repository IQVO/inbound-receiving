# ADR 0020: The appointment copy follows reschedules and door reassignments

## Status

Accepted (2026-11-09). Amends [ADR 0019](./0019-dockappointment-moves-to-dock-scheduling.md)
point 2 (the types the `DockAppointmentConsumer` acts on) and the Consequences
bullet on rescheduled appointments. Does not edit ADR 0019's body.

## Context

ADR 0019 made `appointments` a read-only copy of dock-scheduling's
appointments and fed it from two types only, `DockAppointmentBooked` and
`DockAppointmentCancelled`; it reasoned that "reschedules do not change ASN
coverage and are not needed".

They are needed for the door. dock-scheduling reschedules an appointment (a new
window and possibly a new door) and the yard reassigns its door when the booked
one is busy or closed. The copy kept the door of the booking, and `OpenReceipt`
stamps the receipt with the door on the copy, so a receipt could name a door the
truck is not at. Receipts, yard-management's door occupation and the door
figures of the receiving-performance report then disagree.

## Decision

1. **Two more types.** The `DockAppointmentConsumer` (consumer name
   `dock-appointment-copy`, topic `warehouse.dock-scheduling.events`) also acts
   on:
   - `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentRescheduled`.
     Payload (snake_case): `appointment_id`, `site_id`, `door_code`,
     `previous_door_code`, `window_start`, `window_end`,
     `previous_window_start`, `previous_window_end`,
     `arrival_tolerance_minutes`, `version`. The copy takes `door_code`,
     `window_start` and `window_end` at `version`.
   - `com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentDoorReassigned`.
     Payload: `appointment_id`, `site_id`, `from_door_code`, `to_door_code`,
     `version`. The copy takes `to_door_code` at `version`; `from_door_code`
     is informational (logged, not checked). dock-scheduling is moving this
     type onto its integration topic in a separate change; until that is
     deployed it simply never arrives, and nothing here depends on it.

   The payloads are restated in `apis/asyncapi.yaml`, never imported. Every
   other type on the topic (`NoShow`, check-in, door closures, ...) is still
   committed past.

2. **Same rules as Booked and Cancelled** (use cases
   `ApplyDockAppointmentRescheduled` and `ApplyDockAppointmentDoorReassigned`,
   store methods `ApplyRescheduled` and `ApplyDoorReassigned`):
   - **Version guard**: applied only when `version` is greater than the stored
     one. Reschedules, reassignments, bookings and cancellations share one
     version line, dock-scheduling's aggregate version.
   - The CloudEvents `id` claim in `processed_events` and the update commit in
     one unit of work; the offset is committed afterwards.
   - **No loop**: nothing is written to the outbox; unit, BDD and real-Postgres
     tests pin it, as for the other two types.

3. **Unknown appointment.** A reschedule or reassignment of an appointment the
   copy does not hold (an `OUTBOUND` appointment is never copied, or one not
   seen yet) is `Ignored`: the claim commits, no row is created, and the
   consumer logs it at DEBUG, not WARN, because it is the normal case. A
   cancelled appointment is never revived: a newer reschedule or reassignment
   refreshes its door (and window) and the state stays `Cancelled`, as ADR
   0019 already does for a later Booked.

4. **Open receipts keep their door.** The door on a receipt that is already
   `Open` is a physical fact (where the truck was unloaded) and does not
   follow the appointment. Only receipts opened after the change take the new
   door from the copy.

5. **Invalid payloads.** An empty or malformed `appointment_id`, `version < 1`,
   an empty or invalid door, or `window_end <= window_start` is
   `ErrInvalidEvent`: WARN and commit past, nothing claimed, as for the other
   types. A transient failure rolls back and is retried.

## Consequences

- The copy's door and window are dock-scheduling's current ones, so a receipt
  opened after a reschedule or a door reassignment names the door the truck is
  at, and receipts agree with yard door occupation for new receipts. This
  replaces ADR 0019's Consequences statement that "the door on the receipt is
  the one on the copy, which a Rescheduled event does not refresh".
- A receipt opened before the change keeps the old door. Correcting the door
  of an open receipt is out of scope: it would need a receipt event and an
  operator decision, not a copy update.
- No-show and completion are still not consumed; a receipt can still be opened
  on a no-showed or completed appointment, and only a cancellation blocks one.
- ASN coverage is unchanged by both types; the ASN list still comes from
  `DockAppointmentBooked` only.
- Until dock-scheduling publishes `DockAppointmentDoorReassigned` on its
  integration topic, door reassignments reach the copy only through a
  reschedule that carries the new door.
- No migration: the update touches existing columns of `appointments`.
