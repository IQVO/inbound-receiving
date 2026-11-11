# ADR 0025: ASN timeliness (`AsnLateForArrival` from the visit-arrival copy) and flat S-O-I ASN receiving

## Status

Accepted (2026-11-11). Extends [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(one new published type, one additive payload field), [ADR 0003](./0003-local-copies-and-handover.md)
and [ADR 0021](./0021-dock-to-stock-kpi-from-yard-arrival-and-inventory-stow-facts.md)
(the `visit_arrivals` local copy is READ again; no new consumer, no cross-context
call), [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md),
[ADR 0013](./0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view.md) and
[ADR 0014](./0014-carton-discrepancies-over-limit-mixed-count-and-label-data.md) (a flat ASN is
received by content-GTIN scan), [ADR 0024](./0024-gs1-label-receiving-validation-destination-earliest-best-before-and-receiver-assigned-sscc.md)
(AI 02/AI 37 of the shared gs1 package) and [ADR 0023](./0023-receiving-advice-at-close-with-standard-discrepancy-vocabulary.md)
(one vocabulary row; the kind table is restated here). Does not edit their bodies
beyond the one catalogue row and the one payload-field row in ADR 0004.

## Context

The inbound competitive study (item IF-2) fixes two facts from the standards
survey:

- **The ASN is required before yard entry.** Best Buy's vendor compliance is the
  primary example: ASNs or replacement ASNs are "required prior to yard entry",
  and the retailer measures ASN timeliness and accuracy per supplier (S[11]).
  We could not measure it: receiving knew when an ASN was *registered* only as
  the `time` of `ASNRegistered`, and no ASN fact was ever compared with the
  physical arrival.
- **The most common DESADV/856 form is FLAT (S-O-I).** "The hierarchy is
  optional. The most common form is a flat list of items and quantities, which
  arrives with no SSCC-to-content mapping" (S[2]). Our carton receiving is
  hierarchical (ADR 0012): an SSCC scan resolves against the announced carton
  manifest, and an SSCC that is not on it is `UnexpectedCarton`. A flat ASN
  (lines, no cartons) therefore refused EVERY carton scan, although the ASN
  itself was perfectly valid — `RegisterAsn` has always accepted cartons-free
  ASNs ("a flat ASN has none", ADR 0012).

What the ground already holds, and this ADR reuses instead of rebuilding:

- The **`visit_arrivals` local copy** (ADR 0021): one row per appointment, the
  EARLIEST `arrived_at` of yard-management's `VisitCheckedIn`, written by the
  existing `visit-arrival-copy` consumer. IF-2 needs NO new consumer and NO
  cross-context call (banned anyway, ADR 0003).
- The **only place an ASN and an appointment meet** is `OpenReceipt`
  (`asn_number` + `appointment_id`). That is the only moment the comparison can
  be made without a new trigger path.
- The shared **gs1 package** (re-synced, ADR 0024) parses AI 02 (content GTIN)
  and AI 37 (count) on a GS1-128 scan, and `checkCarton` already reads them for
  a homogeneous manifest carton.
- The **PackagingView** maps a GTIN to its SKU.

## Decision

### 1. The ASN's registration time is exposed, additively, as `registered_at`

The ASN already knows when it was registered (`Register`/`RegisterDraft` takes
`now`); the aggregate simply did not keep it. It does now, under the name
**`registered_at`** (the ADR fixes the name):

- `asn.Asn.RegisteredAt()` (UTC); set by `RegisterDraft` from the same `now`
  that stamps `ASNRegistered`, round-tripped through `Persisted` like every
  other flat field, stored in `asns.registered_at` (nullable; a row written
  before this ADR reads as the zero time).
- `ASNRegistered` gains one optional payload field `registered_at`, and the
  REST `Asn` response one optional `registeredAt` — both additive, both omitted
  for a legacy zero value. Nothing existing is renamed: `expected_arrival`
  (what the supplier ANNOUNCED) and `ReceiptOpened.opened_at` keep their
  meanings; `registered_at` is when WE accepted the ASN into the system.

### 2. `AsnLateForArrival`, raised at `OpenReceipt` and only there

`com.warehouse.wms.inbound-receiving.asn.AsnLateForArrival` — an integration
and analytics event like the other ASN events: CloudEvents `subject` = the ASN
number, Kafka key = the ASN number (so it stays ordered with everything that
happens to that ASN), dataschema
`urn:warehouse:inbound-receiving:<events|analytics>:AsnLateForArrival:v1`, the
same id on both topics (the `FanoutEncoder`).

```
asn_number, supplier_ref, appointment_id, visit_id,
asn_registered_at, arrived_at, lateness_seconds
```

- **Trigger, and only this one.** `OpenReceipt` (the only place asn meets
  appointment): for a receipt opened WITH an appointment, it reads the
  appointment's arrival from the `visit_arrivals` copy (`StowStore.Arrival`,
  the same store ADR 0021 introduced, now with a read method) and, when
  `asn.registered_at > arrived_at`, the Asn aggregate computes the fact
  (`Asn.LateForArrival`, pure, no clock read: `lateness_seconds` =
  `registered_at - arrived_at`, whole seconds, >= 1 by construction) and the
  event is enqueued in the SAME unit of work as the open. A walk-in receipt
  (no appointment), an appointment with no arrival fact seen yet, an ASN whose
  `registered_at` is unknown (a legacy row) and an on-time ASN raise nothing.
  `registered_at == arrived_at` is ON TIME (the ASN beat the truck); so is one
  second before. Neither the registration use case nor any consumer raises it:
  at registration the appointment may not even exist yet, and a consumer-side
  trigger would need a second copy of the ASN→appointment join that
  `OpenReceipt` already holds.
- **Exactly once per ASN.** Two guards, both in the one unit of work: the
  one-open-receipt-per-ASN rule that already guards `OpenReceipt` (a second
  open is `ErrReceiptAlreadyOpen`), and a marker row
  `asn_late_arrivals(asn_number primary key)` inserted `ON CONFLICT DO
  NOTHING` — only the inserter raises — the same shape as
  `receipt_advices` (ADR 0023) and `receipt_stow_completions` (ADR 0021). The
  comparison, the marker and the outbox rows commit together or not at all.
- **Orphan policy:** `x-no-consumer-reason` natively in `apis/asyncapi.yaml`,
  like every producer-only type; the analytics projector (point 5) is its
  first reader.

### 3. Flat S-O-I ASN receiving by content-GTIN scan

An ASN registered **with lines but no carton manifest is FLAT** — that is the
S-O-I form (S[2]); `RegisterAsn` already accepts it and nothing changes at
registration. What changes is the scan:

- An **SSCC scan against a FLAT ASN** resolves at scan time instead of against
  a manifest: the label's **AI 02 content GTIN** is resolved to its SKU
  through the PackagingView, the SKU to the ASN LINE that carries it, and the
  **AI 37 count** (default **1** without AI 37) is booked on that line in the
  scan's condition (`Good` by default, `Damaged` if the operator says so) —
  through the receipt's own line rules, so `ReceiptLineReceived` stays the sole
  quantity authority, followed by `ReceiptCartonReceived` (identity only:
  the SSCC becomes the carton's SSCC and LPN, `sscc_source: supplier`, sealed,
  one content entry). The carton is ACCEPTED — not `UnexpectedCarton`. A
  later scan of the same SSCC is `DuplicateSscc` exactly like a manifest
  carton's, and the site-wide `ReceivedElsewhere` check applies unchanged.
- **Flat or hierarchical is a property of the ASN, decided by the use case**
  from the ASN's own carton list (immutable after registration), never from
  the receipt's ledger — otherwise the first accepted flat carton (which does
  carry an SSCC) would turn the receipt "hierarchical" mid-flight.
- **An SSCC scan against a HIERARCHICAL ASN keeps today's strict behaviour
  exactly**: an SSCC that is not on the manifest is still
  `UnexpectedCarton`, AIs 02/37 still feed `checkCarton` (ADR 0014/0024). The
  flat path is entered only when the ASN announced no cartons at all.
- **Refusals, both recorded as findings and booking nothing** (200
  `accepted: false`, the shape of every refused scan):
  - a flat-ASN scan **with no AI 02** is refused as the new kind
    **`MissingContentGtin`** (see the vocabulary row below): the label itself
    may be GS1-compliant — an SSCC-only label is legal — but a flat ASN has
    no manifest, so without AI 02 there is nothing to resolve the carton
    against. `LabelNonCompliant` (ADR 0024) does NOT fit: its closed reason-code
    set is derived from the gs1 package's PARSE errors, and this scan parses
    fine. `sscc` is carried (the SSCC parsed), `actual` the scan's count when
    AI 37 was present.
  - a flat-ASN scan whose **AI 02 resolves to no line of the ASN** (the GTIN
    is unknown to the PackagingView, or maps to a SKU the ASN does not carry)
    is refused as **`UnknownGtin`** with the content GTIN in `actual` — the
    same kind and effect as the manifest path (ADR 0014), restated for the
    flat form as "no line of THIS receipt resolves it" rather than only "the
    PackagingView cannot resolve it". No new kind: the product-class mismatch
    already has one.
- **Close is unchanged.** A flat ASN's lines carry `expected_qty` like any
  other; `Short`/`Over`/`Damaged` are computed from booked quantities against
  them exactly as always. The manifest is simply ABSENT: no carton is ever
  `Expected`, so close raises no `MissingCarton` and no per-carton expectation
  exists to disagree with. Expected quantities are NOT adjusted by a flat
  scan (a `KindOver` at close is still an over).

### 4. The vocabulary row (extends the table of ADR 0023)

| kind | level | W13 | CBV disposition | why |
| --- | --- | --- | --- | --- |
| `MissingContentGtin` | carton | **unmapped** | `non_conformant` | a carton scan against a flat ASN carried no AI 02, so no line could be resolved; the label does not carry what receiving requires |

There are now **twelve** carton kinds (the eleven of ADR 0012/0014/0024 plus
this one) and seventeen kinds in `AllKinds`. W13 stays `unmapped` for the same
reason as `UnlabelledCarton`: it describes the condition of goods and nothing
in the research names a code for a label that lacks an AI. `MissingContentGtin`
is not added to `ReceiptClosed`.

### 5. The KPI: "% ASNs received before arrival", per supplier, in the report

The projector folds the new kind (`report.Kinds()` goes from eleven to twelve
kinds): `ASNRegistered` now also folds its `supplier_ref` into `asn_facts`
(additive column), and `AsnLateForArrival` folds into the new analytical table
`asn_late_facts` (one row per ASN, `ON CONFLICT DO NOTHING`). `GET
/reports/receiving-performance` gains, additively, an `asn_timeliness` array
per day:

```
asn_timeliness: [ { supplier_ref, registered, late, share_on_time } ]
share_on_time = 1 - late / registered, null when registered is 0
```

- **Both sides of the ratio are counted in the UTC day of the ASN's
  `registered_at`** — the supplier's own action day — so a late flag that the
  projector sees after midnight still lands in the day the ASN belongs to.
  `registered` counts the day's `asn_facts` rows per supplier; `late` counts
  the day's `asn_late_facts` rows (a subset by `asn_number`).
- **The supplier list is bounded**: at most the **top 10** suppliers of the day
  by `registered` (ties broken by `supplier_ref` ascending, deterministic).
  A supplier outside the top 10 is simply absent from that day's block — its
  facts stay in the tables and reappear the day it makes the cut; nothing is
  aggregated into an "others" bucket, because a share over a mixed bucket is
  not a supplier score.
- No dashboard changes; the MCP server exposes OLTP reads only and gains
  nothing (ADR 0021's rule).

### 6. The migrations and the rollout

One OLTP migration, `0009_asn_timeliness_and_flat_asns`, strictly additive,
no backfill:

- `asns.registered_at TIMESTAMPTZ NULL` (a legacy row reads as unknown; an
  unknown `registered_at` can never be late, so no old ASN raises the event);
- `asn_late_arrivals` (the exactly-once marker of point 2);
- the `receipt_carton_discrepancies.kind` check widened by
  `MissingContentGtin` (constraint dropped and recreated with twelve values).

One analytical migration, `0004_asn_timeliness` (applied by the projector):
`asn_facts.supplier_ref TEXT NULL` and the `asn_late_facts` table.

Rollout needs no order: a consumer that does not know the type ignores it, the
new columns are nullable, and the reports service deployed before the
projector simply serves an empty `asn_timeliness` block. No new env, no new
consumer group, no new topic, no MCP tool; `web/` untouched.

## Consequences

- ASN timeliness is measurable per supplier without a synchronous call to a
  sibling and without a second local copy: the yard fact was already ours
  (ADR 0021); only the comparison and the event are new.
- A flat S-O-I ASN is now receivable end to end. The price is honest: the
  carton's contents come from the LABEL (AI 02/37), not from an announced
  manifest, so a mislabelled carton books what the label says until close —
  exactly the trust a supplier asking for S-O-I receiving accepts, and the
  discrepancy still shows at close.
- The type catalogue goes from ten published types to ELEVEN; the analytics
  topic carries the same eleven. Every count that says ten is updated in this
  PR.
- `registered_at` is exposed on reads only; nothing existing is renamed and no
  stored value changes meaning. `expected_arrival` remains the announced
  time and the two must never be conflated — one is the supplier's promise,
  the other our record of when the promise arrived.
- Flat receiving cannot give a mixed carton its breakdown: one scan carries
  one content GTIN. A mixed pallet under a flat ASN is scanned carton by
  carton; the audit count of ADR 0014 stays a manifest-carton tool.
- `web/` and the dashboards change nothing; the floor client renders the
  findings it already reads.

## Alternatives considered and rejected

- **Raise the event from `RegisterAsn` or a consumer.** At registration the
  appointment may not exist and the visit may not have happened; a consumer
  trigger needs the ASN→appointment join that only `OpenReceipt` holds, plus
  its own exactly-once machinery. `OpenReceipt` is where the two facts meet
  and where the one-open-receipt rule already serialises us.
- **Compare against the appointment WINDOW (expected arrival).** That measures
  a promise against a booking; the standard (S[11]) measures the ASN against
  the PHYSICAL truck. The visit-arrival copy is the physical fact, and it was
  already there.
- **Treat a flat-ASN SSCC scan as `UnexpectedCarton`.** That refuses every
  carton of the most common ASN form in retail (S[2]) — the exact failure this
  ADR exists to remove.
- **A `LabelNonCompliant` with a new reason code for the missing AI 02.** The
  label breaks no GS1 rule; ADR 0024's reason codes are pinned to the gs1
  package's parse errors and must not grow a non-parse member.
- **A new kind for "GTIN resolves to no line".** `UnknownGtin` already says
  it for the manifest path; restating its scope for the flat form is one
  sentence here, a second kind would be a synonym.
- **Read `visit_arrivals` in the projector (join the two facts there).** The
  projector reads only its own analytics topic (ADR 0006 rule); the yard fact
  would have to be re-published onto it, which is a second copy of ADR 0021.
- **A per-supplier timeliness table with its own consumer.** The projector
  already folds ASN kinds and already owns the report's per-day shape; a
  second projector is a second analytical database.
