# ADR 0022: Stage E removes the backfill command, the legacy encoders and Replay

## Status

Accepted (2026-11-10). Amends [ADR 0019](./0019-dockappointment-moves-to-dock-scheduling.md)
(point 5 and Rollout step 4, the first half of stage E) and the
`dockappointment` rows of [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md),
which are now marked retired. Does not edit their bodies beyond a one-line
pointer each. Companion of dock-scheduling's ADR 0011, which removes the
importer this command fed.

## Context

ADR 0019 moved the DockAppointment to dock-scheduling by a strangler. Stage A
was dock-scheduling's legacy importer, stage B this context's one-shot
`api republish-appointments` backfill, which re-announced every stored
appointment as the four legacy `dockappointment` events
(`DockAppointmentBooked`, `CheckedIn`, `Cancelled`, `Completed`) so the
importer could rebuild them. ADR 0019 kept, "for this command only", the legacy
`Encoder` / `AnalyticsEncoder` mappings of those types and the domain `Replay`
that rebuilds a row's event sequence, and said they go at stage E.

Verified on the live cluster on 2026-11-10:

- this context's appointment copy is empty (`GET /appointments` returns
  `{"items":[]}`): there is no legacy row to re-announce;
- dock-scheduling's `DOCK_LEGACY_IMPORT_CONSUMER_GROUP` is unset, so its
  importer never ran;
- the topic `warehouse.inbound-receiving.events` holds zero messages.

Nothing legacy exists to preserve, and the command's only purpose, feeding the
importer, is gone with it (dock-scheduling ADR 0011).

Consumers of the four types were checked on each repository's `origin/develop`
(2026-11-10): dock-scheduling (its importer, removed by its ADR 0011),
warehouse-planning, yard-management, exception-management, e2e-tests,
network-fulfillment and warehouse-ops-agent. warehouse-planning and
yard-management mention the types only in comments and in tests that assert the
type is ignored; the others have no reference. No non-test code reads them.

## Decision

1. **Remove the backfill.** The `republish-appointments` subcommand and its
   dispatch (`cmd/api/republish.go`, the `runCommand` hook in `main`), the
   `RepublishAppointments` use case, the `ports.AppointmentHistory` port and
   `AppointmentRepo.ListRecords` with `repository.AppointmentRecord`, which only
   it used. `./api <anything>` now serves like `./api`: the binary has no
   subcommand.
2. **Remove the legacy encoders.** `Encoder.EncodeAppointment`,
   `AnalyticsEncoder.EncodeAppointment`, `appointmentRow`, the four payload
   structs and the `dockappointment` entity constant of the outbound Kafka
   adapter, and `ports.LegacyAppointmentEncoder`. The running service's
   `FanoutEncoder` never had them.
3. **Remove `Replay`.** The domain's `Timeline`, `Replay` and the four legacy
   domain events with their `Event` interface and `Header`
   (`internal/domain/appointment/events.go`). The aggregate keeps the state the
   copy needs (door, window, ASN coverage, version, state).
4. **Retire the four types from the contract.** `apis/asyncapi.yaml` no longer
   declares `DockAppointmentBooked`, `DockAppointmentCheckedIn`,
   `DockAppointmentCancelled` and `DockAppointmentCompleted` with the prefix
   `com.warehouse.wms.inbound-receiving.dockappointment` on either channel
   (integration and analytics: messages, payload schemas, channel references).
   dock-scheduling's `Booked`, `Cancelled`, `Rescheduled` and `DoorReassigned`
   declared as **consumed** are a different prefix and stay. ADR 0004's
   catalogue rows for the four types are marked retired, not deleted, so the
   history of what the topics once carried stays readable.
5. **No legacy event can be produced any more.** Nothing in this service builds
   one; the integration topic carries `asn.*` and `receipt.*` types only.

Kept on purpose, each by its own reason:

- **The analytics projector still folds the historical `dockappointment` types.**
  They stay on `warehouse.inbound-receiving.analytics` (a stream is
  append-only and a rebuild from the topic reads them again) and the
  `appointments` block of the receiving-performance report stays frozen, as
  ADR 0019 said. The fold, the report `Kind` constants and the consumer's
  handling are untouched; their tests build the historical messages from
  literals instead of through the removed encoders.
- **The deprecated `GET /appointments` and `GET /appointments/{appointmentId}`**
  (and the MCP tools `get_appointment` and `list_appointments`), **the three
  410 `appointment-moved` write routes**, the appointment copy tables and the
  `DockAppointmentConsumer`, and the `web/` console. This context's own console
  remote still calls `/appointments`, and dock-scheduling has no UI; removing
  the GETs is a business and UX decision not taken yet. ADR 0019 point 4's
  "go at stage E" is therefore **pending**, not done.

## Consequences

- ADR 0019's Rollout step 4 is half done: the backfill command and the legacy
  encoders and `Replay` are gone; the deprecated GETs and the 410 routes remain
  until a decision names a date.
- The published contract shrinks by four types per channel. A consumer that
  still declared one of them would now be reading a type nobody produces; none
  was found.
- The `appointments` copy tables, the `appointment` aggregate and the
  `DockAppointmentConsumer` are unchanged; no migration.
- Retiring the types from `apis/asyncapi.yaml` is a contract change in the
  breaking direction for a reader that ignored ADR 0019's "legacy" label; the
  cluster shows no message of these types on the topic and no consumer reads
  them, which is why it is safe now.
