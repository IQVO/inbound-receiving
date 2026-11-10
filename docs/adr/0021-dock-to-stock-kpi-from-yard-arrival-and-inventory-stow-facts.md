# ADR 0021: Dock-to-stock KPI from yard arrival and inventory stow facts

## Status

Accepted (2026-11-09). Extends [ADR 0006](./0006-analytics-read-side.md) (the
analytics read side), [ADR 0003](./0003-local-copies-and-handover.md) (local
copies) and [ADR 0019](./0019-dockappointment-moves-to-dock-scheduling.md)
(the appointment copy). Does not edit their bodies.

## Context

The board-level receiving KPI is **dock-to-stock**: the time from the
carrier's physical arrival at the gate to the moment the goods are stowed in
inventory (WERC best-in-class under 3.1 h, APQC median 15 h). The
receiving-performance report cannot say it today:

- Receiving only knows receipt *open* and *close*. The report's appointment
  block (booked, checked in, cancelled, completed, dock dwell) has been frozen
  since ADR 0019 moved the `DockAppointment` to dock-scheduling: this context
  no longer raises those events.
- The arrival (yard-management `VisitCheckedIn`) and the stow (inventory-storage
  `ContainerStowed`) are facts of two other contexts.

The fleet rule is that an analytics topic is a context's INTERNAL data product
and a projector reads only its own analytics topic. So the arrival and the
stow reach this context the way every other sibling fact does: through
event-fed local copies in the OLTP database, and the result leaves as this
context's own event, which the existing analytics stream and projector carry.

## Decision

### 1. KPI definition

```
dock_to_stock = stowed_at - arrived_at
arrived_at    = yard-management VisitCheckedIn.arrived_at of the appointment the receipt was opened from
stowed_at     = the latest, over the Good cartons of the receipt, of the time each carton was first stowed
```

Start is the yard's physical arrival, not the receipt open and not the
appointment window. Stop is the LAST Good carton stowed, so a receipt is
"in stock" only when all of it is. The unit is seconds. A negative result
(clock skew between two contexts) is reported as 0.

The report also gives the share of receipts at or under the WERC target of
3.1 h = 11160 seconds.

Excluded, on purpose and visibly:

- **Walk-in receipts** (opened without an appointment) have no arrival fact.
  Their dock-to-stock is "no arrival": they are excluded, no event is raised.
- **Receipts without cartons** (line-only receipts, no `ReceiptCartonReceived`)
  have no LPN to follow to a stow. Out of scope; a later ADR can follow them by
  SKU and quantity if inventory-storage publishes a joinable stow fact.
- Receipts that never get every Good carton observed as stowed (see point 2)
  stay "stow pending": no event, so they are absent from the KPI, never
  counted as zero.

### 2. The stow fact: `ContainerStowed`, joined by LPN

inventory-storage's AsyncAPI (`apis/asyncapi.yaml` on `origin/develop`, channel
`warehouse.inventory.events`) publishes two stow facts:

| type | payload keys | joinable to a receipt? |
| --- | --- | --- |
| `com.warehouse.wms.inventory-storage.container.ContainerStowed` | `lpn`, `sku`, `qty_eaches`, `owner_ref?`, `site_id`, `bin`, `at` (key and subject = the LPN) | **Yes, by `lpn`**: the LPN is the SSCC (or the internal `LPN-<ulid>`) that `ReceiptCartonReceived` and the `receipt_cartons` ledger carry |
| `com.warehouse.wms.inventory-storage.stock.ItemStowed` | `sku`, `bin_id`, `quantity` (key and subject = the SKU) | No: it names no LPN, receipt or line, only a SKU and a bin |

So the carton-level join is by LPN through `ContainerStowed`; `ItemStowed` is
not consumed (it could only be matched by SKU and quantity, which is
ambiguous across receipts). The consumed payload is restated in
`apis/asyncapi.yaml`, never imported.

Limit, stated plainly: inventory-storage creates a Container (and so raises
`ContainerStowed`) only for a sealed, single-SKU, Good carton whose receipt
line is booked and whose GTIN is in its PackagingView (its ADR 0045/0046).
A Good carton that is not a Container never produces a stow observation, so its
receipt never completes and is absent from the KPI (survivorship bias towards
well-labelled receipts). The report counts only completed receipts; the
operational answer is for inventory-storage to register more cartons, not for
this context to guess a stow.

`ContainerStowed` carries no version and is re-raised when a container moves
to another bin of the same site. The local copy `carton_stows(lpn, stowed_at)`
therefore keeps the EARLIEST `at` per LPN (a move is not a new stow), which
makes the upsert idempotent and order-independent. This is the ordering guard
for a payload that has no version.

### 3. The arrival fact: `VisitCheckedIn`, joined by appointment id

yard-management publishes
`com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn` on
`warehouse.yard-management.events` (key and subject = `visit_id`). Consumed
keys: `visit_id`, `appointment_id` (nullable), `arrived_at`, `version`; the
rest (`site_id`, `carrier`, `carrier_scac`, `purpose`, `trailers`) is ignored.

Local copy `visit_arrivals(appointment_id, arrived_at, visit_id)`; only visits
with a non-null `appointment_id` are kept (a visit without one is `Ignored`,
nothing is claimed past the id). The receipt joins by the appointment id it
was opened from. `VisitCheckedIn` is raised once per visit at version 1 (later
visit events carry higher versions on other types), so a version guard would
be vacuous; the copy keeps the EARLIEST `arrived_at` per appointment instead,
again idempotent and order-independent. `version < 1` is an invalid payload.
An appointment that covers several ASNs gives several receipts that share one
arrival.

### 4. Two local-copy consumers

Both have the shape and rules of `dock-appointment-copy` (ADR 0019):

| consumer (claim namespace) | topic | env (mandatory) | types acted on |
| --- | --- | --- | --- |
| `visit-arrival-copy` | `warehouse.yard-management.events` | `VISIT_ARRIVAL_CONSUMER_GROUP` | `VisitCheckedIn` |
| `carton-stow-copy` | `warehouse.inventory.events` | `CARTON_STOW_CONSUMER_GROUP` | `ContainerStowed` |

- The CloudEvents `id` is claimed in `processed_events` in the SAME unit of
  work as the upsert and the evaluation of point 5; a redelivery is a
  `Duplicate`.
- Every other type on either topic is committed past untouched.
- A message that is not a CloudEvent, a payload that cannot be decoded and
  an invalid payload (empty `lpn` or `visit_id`, missing time, `version < 1`)
  are WARNed and committed past, nothing claimed.
- A transient failure rolls back (claim included) and retries with the
  shared capped backoff.
- Always on, no off switch; the group env is a boot error when empty. Readiness
  does not depend on either topic: the yard topic may not exist on the cluster
  yet, in which case the reader retries and logs exactly like the other
  consumers, and the service stays ready.
- Reader `MaxWait` is the shared 500 ms.

### 5. `ReceiptStowCompleted`: the receipt's own event

`com.warehouse.wms.inbound-receiving.receipt.ReceiptStowCompleted`, an
integration and analytics event like the other receipt events: subject = the
receipt id, Kafka key = the ASN number, the same id on both topics (the
`FanoutEncoder`), dataschema
`urn:warehouse:inbound-receiving:<events|analytics>:ReceiptStowCompleted:v1`.

Payload (snake_case, times RFC 3339 UTC):

```
receipt_id, asn_number, appointment_id, arrived_at, closed_at, stowed_at,
dock_to_stock_seconds, carton_count
```

It is raised through the transactional outbox when, for a CLOSED receipt with
an appointment whose arrival is known, **every Good carton** of the receipt
has a stow observation. A Good carton is an Accepted carton whose contents are
all Good; a Damaged carton, an Expected-but-never-accepted carton (a
`MissingCarton`) and an empty carton are not. `carton_count` is the number of
Good cartons; a receipt with none is out of scope.

The computation is pure and lives in the domain
(`receipt.Receipt.StowCompletion(arrivedAt, stows)`): no wall-clock read; the
event's `occurred_at` comes from the injected `ports.Clock` in the use case.

**Exactly once, in any order.** A marker row `receipt_stow_completions(receipt_id
primary key)` is inserted with `ON CONFLICT DO NOTHING` in the same unit of
work as the outbox rows; only the inserter raises the event. The completion is
evaluated on every path that can complete the set:

1. `CloseReceipt` (stows and arrival arrived while the receipt was open),
2. the arrival consumer (late arrival fact), for the closed receipts of that
   appointment,
3. the stow consumer (late stow), for the receipts that hold that LPN.

Each evaluation first takes `SELECT ... FOR UPDATE` on the receipt row, after
writing its own fact. Two evaluations of one receipt (the close racing a
consumer, or two consumers) therefore serialise, and the later one sees the
earlier one's committed fact: neither can miss the other, and the marker makes
a repeat a no-op.

### 6. The report

`apis/openapi.yaml` `GET /reports/receiving-performance` gains, additively, a
`dock_to_stock` object per day and a top-level `dock_to_stock_target_seconds`
(11160):

```
dock_to_stock: { count, p50_seconds, p90_seconds, under_target, share_under_target }
```

The projector folds `ReceiptStowCompleted` into a new analytical table
`dock_to_stock_facts` (one row per receipt, migration `0002`). A fact counts in
the UTC day of its `stowed_at`. `p50_seconds` (the median) and `p90_seconds`
are linear-interpolated percentiles (`percentile_cont`, the definition
`report.Percentile` mirrors), `null` when `count` is 0; `under_target` counts
the facts at or under 11160 s and `share_under_target` is `under_target /
count`, `null` when `count` is 0. The MCP report tool returns the same rows.

The frozen appointment block (`appointments`, `dock_dwell`) stays in the
report and in the projector, unchanged, and is documented as frozen since ADR
0019. dock-to-stock does not replace it.

## Consequences

- The KPI exists without any synchronous call to a sibling and without the
  projector reading a foreign topic. The price is two more local copies in the
  OLTP database and two more consumer groups.
- The KPI is only as complete as inventory-storage's container registration
  (point 2); the report's `count` against `receipts_closed` shows the gap.
- Everything is additive: a new event type (consumers of
  `warehouse.inbound-receiving.events` ignore unknown types), additive report
  fields, an additive migration (the two copies and the marker) and an
  additive analytical migration.
- `ReceiptStowCompleted` has no consumer in the fleet yet
  (`x-no-consumer-reason` in `apis/asyncapi.yaml`); it feeds this context's
  own projector through the analytics topic.
- warehouse-infra must set `VISIT_ARRIVAL_CONSUMER_GROUP` and
  `CARTON_STOW_CONSUMER_GROUP` (chart values `config.visitArrivalConsumerGroup`
  and `config.cartonStowConsumerGroup`); the API refuses to boot without them.

## Alternatives considered and rejected

- **The projector reads the yard and inventory topics directly.** Breaks the
  rule that a projector reads only its own analytics topic and would couple
  the analytical model to two foreign contracts.
- **A synchronous call to inventory-storage or yard-management.** Forbidden
  (ADR 0003): receiving never calls a sibling live.
- **`ItemStowed` as the stop.** It names a SKU and a bin, not an LPN or a
  receipt, so it cannot be attributed to a receipt.
- **Receipt open or close as the start.** That is the dwell of the dock, not
  the gate-to-stock KPI the standard defines.
- **A version guard on the two copies.** Neither payload offers a version that
  orders the fact (`VisitCheckedIn` is always version 1; `ContainerStowed` has
  none). The earliest-wins `LEAST` upsert is idempotent and order-independent.
