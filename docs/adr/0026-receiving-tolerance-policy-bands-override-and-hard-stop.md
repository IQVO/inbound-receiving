# ADR 0026: Receiving tolerance policy — auto-accept within band, override with reason, hard stop (IF-8)

## Status

Accepted (2026-11-13). Extends [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(one new `policy` entity segment, one new published type, one payload-field
row in `ReceiptClosed`), [ADR 0002](./0002-aggregates-and-invariants.md)
(one new aggregate), [ADR 0023](./0023-receiving-advice-at-close-with-standard-discrepancy-vocabulary.md)
(two vocabulary rows; the kinds table is restated here) and
[ADR 0025](./0025-asn-timeliness-from-the-visit-arrival-copy-and-flat-soi-asn-receiving.md)
(the report block shape the `tolerance` block copies). Does not edit their
bodies beyond the catalogue rows and the entity-segment list in ADR 0004.

## Context

Normal commercial variance at the dock — a supplier that ships five cases
instead of four, or 103 units against a 100-unit line — is not a dispute, but
our close treats every unit of it as a discrepancy finding: `Short`/`Over`,
an advice line with a W13 code, a supplier conversation. The competitive study
(item IF-8, V[8][29][47][43]) shows what tier-1 WMS do instead:

- Oracle WMS Cloud 25D enforces PO **over-receipt tolerance limits in real
  time** (V[8]).
- Infor's receipt validation templates per owner make an **"override %"** band
  that **needs a reason code** and a **"hard error %"** that **blocks the
  receipt** (V[29]).
- The same three-band shape (auto-accept / override with reason / hard stop)
  recurs across the study's matrix (V[43][47]); the study's summary names
  "configurable over/under tolerance bands with reason codes and hard stops"
  as table stakes we lack.

The study names NO reason codes: every source says "a reason code" without
listing one. The code list below is therefore **ours**, declared here.

What the ground already holds, and this ADR reuses: the receipt keeps
`ownerRef` and its lines keep `expected_qty` (ADR 0002), so the variance is
computable at both seams this ADR touches; the discrepancy vocabulary table
(ADR 0023) is the single place a kind gets its W13/CBV coding; the report
already has per-day blocks with bounded per-dimension lists (`asn_timeliness`,
ADR 0025) to copy.

## Decision

### 1. The `ReceivingPolicy` aggregate, keyed (ownerRef, supplierRef)

New domain package `internal/domain/policy`. One aggregate,
`policy.ReceivingPolicy`, keyed by the pair **(ownerRef, supplierRef)** —
ownerRef-aware for the 3PL multi-client case: an empty `ownerRef` is the
**house default** a client-owned receipt does not fall back to (the lookup is
exact-match, see 3). Fields:

- `over_tolerance`, `under_tolerance`: ratios (`0.05` = 5 %);
- `over_hard_stop`, `under_hard_stop`: the ceilings **above** the tolerances;
- audit: `declared_at`, `version` (1 on create, +1 per replace).

Validation, refusing anything else: per side
**`0 <= tolerance < hard_stop <= 1`**. A tolerance of `0` is legal (every unit
of variance needs a reason); a hard stop of `1` is legal (a 100 % variance is
still overridable, everything above it is not — the ratio cannot exceed 1 by
construction, so `hard_stop = 1` means "no hard stop in practice").

**Grain: supplier only, no SKU-class scope.** The brief allows a SKU-class
scope only from packaging/sku-class data already copied locally, with no live
lookup. The local `pack_levels` copy (ADR 0013) holds GTIN → SKU, level,
units-per-pack and validity — there is **no SKU-class column anywhere in this
service**. Declaring one now would need either a product-master lookup (banned,
ADR 0003) or an invented classification. SKU-class grain is a **documented
follow-up**: when product-master publishes a class fact and a local copy is
fed, the policy gains an optional class scope and the evaluation picks the
most specific match.

`Declare(ownerRef, supplierRef, bands, loadedVersion, now)` is
create-or-replace: `loadedVersion 0` creates at version 1, anything else
replaces at `loadedVersion + 1`. The supplier reference is validated exactly
like the ASN's (`asn.ErrInvalidSupplierRef`), the owner reference like every
owner-aware field (`shared.OwnerRef`).

### 2. The band semantics (the table this ADR is)

The variance ratio of a line is `|received − expected| / expected`
(`expected_qty >= 1` by the receipt's invariants, so the ratio is always
defined; `received` is good + damaged). Over and under are judged
independently, each against its own tolerance and hard stop:

| variance ratio r | name | what happens |
| --- | --- | --- |
| `r <= tolerance` | **inside the band** | the receipt auto-accepts the variance: **no** `Short`/`Over` finding for that line (Damaged is unaffected — condition is a different dimension) |
| `tolerance < r < hard_stop` | **override zone** | the close/scan proceeds **only** with an override reason code; with one, the line's finding is `OverTolerance` / `UnderTolerance` carrying the code |
| `r >= hard_stop` | **hard stop** | refused (409 `tolerance-blocked`), no override possible; the receipt stays open and the operator must fix the ASN, return units, or amend the policy |

Boundaries, stated once: equality with the tolerance is INSIDE (accepted);
equality with the hard stop is BLOCKED (a hard stop must stop). The ratio is
computed in `float64`; `1/20` and the literal `0.05` are the same double, so
"5 % over against a 5 % band" is inside.

**No policy declared for the receipt's (ownerRef, supplierRef) = exactly
today's behaviour.** Additive only: no default policy is invented, no band is
assumed, and every existing close/scan answer is unchanged. A nil or
not-found policy repository in a test behaves the same.

### 3. The enforcement seams: CloseReceipt and carton accept (ReceiveScan)

Both seams resolve the policy by **exact match** on the receipt's
`(ownerRef, supplierRef)` — the ownerRef from the receipt, the supplier from
its ASN — read in the same unit of work as the booking/close. No house
default fallback: a client-owned receipt whose owner has no policy is a
no-policy receipt (the 3PL client, not the house, owns that commercial
relationship).

- **CloseReceipt** judges the FINAL state, over and under. The command gains
  an optional `overrideReason`. With a policy: inside-band lines raise no
  over/under finding; override-zone lines raise `OverTolerance` /
  `UnderTolerance` carrying the reason (W13 `03` / `02` below); any hard-stop
  line refuses the close before anything changes (the aggregate's state
  transition happens only after the band check passes).
- **ReceiveScan** judges, per line the scan booked (the `ReceiptLineReceived`
  events it raised), the line's state AFTER the booking, **over only**: an
  open receipt is legitimately short mid-flight, so the under band is a
  close-time judgement. The scan command gains the same optional
  `overrideReason`; a hard-stop booking is refused and the unit of work rolls
  back — nothing is booked, exactly like every refused scan.
- The reason code is validated wherever it is given (close or scan): it must
  be one of the codes of point 4, else 400 `invalid-override-reason`. The
  scan-time code is the dock-side acknowledgement that lets the physical
  receiving continue; the **finding carries the close's code** — the close is
  the commercial moment the advice reports, and it re-confirms the override
  on the frozen quantities.

### 4. The reason codes (ours — the research names none)

A small closed list, declared in the domain
(`receipt.OverrideReason`), one code per close:

| code | means |
| --- | --- |
| `supplier-agreed` | the supplier confirmed the quantity in advance (a substitution, an agreed over-ship) |
| `damaged-in-transit-recounted` | the variance was traced to transit damage and the count re-verified with the carrier note |
| `mislabelled-recounted` | the cartons were mislabelled; a physical recount confirmed the booked quantity |
| `recount-confirmed` | a full recount, for an audit or a claim, confirmed the booked quantity against the paperwork |

W13 has no code for "why the operator overrode", and we do not invent one:
the reason codes are **our vocabulary**, carried in `reason_code` on the
finding, not in any W13 segment.

### 5. The findings and the advice (extends the table of ADR 0023)

Two new **line** kinds, like `Short`/`Over`:

| kind | level | W13 | CBV disposition | why |
| --- | --- | --- | --- | --- |
| `OverTolerance` | line | `03` over | `mismatch_quantity` | received above the tolerance band; closed only with an override reason code |
| `UnderTolerance` | line | `02` short | `mismatch_quantity` | received below the tolerance band; closed only with an override reason code |

`ReceiptClosed.discrepancies` gains the two kinds and, additively, a
`reason_code` on each entry (present exactly on the tolerance kinds). The
receiving advice computes the same tolerance-aware entries with the same band
and the same code, so the advice and `ReceiptClosed` can never disagree.
`receipt_v1`'s payload rules stand: a breaking change would be a v2 type, and
adding an optional field and two enum members to an open list is additive.

**Against ADR 0023's "our discrepancy kinds map 1:1" claim:** the mapping
still holds — every kind, new ones included, has exactly one W13 code and one
CBV disposition (the two rows above). What changes is WHICH lines carry a
finding: a within-band variance is ACCEPTED, not a discrepancy, so it raises
no kind at all. A W13 renderer building from the advice sees `03`/`02` only
for variances the operator overrode, which is exactly what the supplier
dispute process wants: the accepted commercial variance never becomes an
exception document. The line's expected and received quantities still travel
on every line of `ReceiptClosed`, the advice and the REST receipt, so the
raw variance stays visible as data.

`ReceiptClosed` also gains an optional `tolerance` object
(`over_tolerance`, `under_tolerance`, `over_hard_stop`, `under_hard_stop`),
present exactly when a policy was applied — the audit fact of WHICH band
accepted the receipt, and the projector's signal for the KPI's auto-accept
count. The REST receipt read and the A2A/MCP views keep computing their raw
live discrepancies: the applied band is not persisted on the receipt (point
8), so the close event is the authoritative tolerance record.

### 6. The event `ReceivingPolicyDeclared`

`com.warehouse.wms.inbound-receiving.policy.ReceivingPolicyDeclared` — the
first type of a new `policy` entity segment (ADR 0004's list gains `policy`).
CloudEvents `subject` = the supplier reference, Kafka key = the supplier
reference (the natural correlation for a supplier-scoring consumer; owner
variants of one supplier are independent declarations and both fit the
payload), dataschema
`urn:warehouse:inbound-receiving:<events|analytics>:ReceivingPolicyDeclared:v1`,
the same id on both topics (the `FanoutEncoder`).

```
supplier_ref, owner_ref?, over_tolerance, under_tolerance,
over_hard_stop, under_hard_stop, version, declared_at
```

Raised by the declaration use case in the same unit of work as the upsert.
Orphan policy: `x-no-consumer-reason` natively in `apis/asyncapi.yaml` — this
is a declaration others will copy later (supplier-compliance's scorecards,
quality-inspection's sampling driver, IF-13's trusted-ASN); no consumer
exists yet, and none is invented here.

### 7. The REST admin surface

- `POST /receiving-policies` — create-or-replace; `Idempotency-Key` required
  (the fleet rule for every POST); body `{ownerRef?, supplierRef,
  overTolerance, underTolerance, overHardStop, underHardStop}`; 201 on
  create, 200 on replace, both answering the stored policy with its `version`.
- `GET /receiving-policies/{supplierRef}?ownerRef=` — the house default when
  `ownerRef` is absent, that owner's policy when present; 404
  `receiving-policy-not-found` when the exact key has no row.

New problems, registered in the catalogue and the openapi slug list:
**409 `tolerance-override-required`** (a line sits in the override zone and
no valid reason code was given; the detail carries the line, the band and the
actual variance), **409 `tolerance-blocked`** (a line is at or beyond the hard
stop; no override can pass; the receipt stays open), 400
`invalid-override-reason`, 404 `receiving-policy-not-found`.

### 8. The migrations and the rollout

ONE OLTP migration, `0010_receiving_policies`, strictly additive, no
backfill: the `receiving_policies` table keyed `(owner_ref, supplier_ref)`
with the four ratio columns, `version`, `declared_at` and a CHECK that pins
`0 <= tolerance < hard_stop <= 1` per side at the storage layer too.
**Nothing else changes in the OLTP schema** — the applied band is not copied
onto the receipt (the close event carries it; a stored copy would need a
backfill to answer old closes and would drift from the policy the moment it
is replaced).

One analytical migration, `0005_receiving_tolerance` (applied by the
projector): `receipt_facts.tolerance_applied boolean NOT NULL DEFAULT false`,
`receipt_discrepancies.reason_code TEXT NULL`, and the `kind` CHECK widened
to five values.

Rollout needs no order: a receipt closed before the policy exists keeps
today's answers; the projector tolerates the new payload fields (absent on
old events); the reports service deployed before the projector serves a zero
`tolerance` block. No new env, no new consumer group, no new topic, no MCP
tool, no A2A skill; `web/` untouched.

### 9. The KPI: the `tolerance` block of the receiving-performance report

The projector folds what it already folds — `ReceiptClosed` now carries the
tolerance kinds (folded into `receipt_discrepancies` with their
`reason_code`) and the `tolerance` fact (folded into
`receipt_facts.tolerance_applied`). `GET /reports/receiving-performance`
gains, additively, a `tolerance` object per day, counted in the UTC day of
the close like every receipt metric:

```
tolerance: {
  closed,                      receipts closed in the day (all of them)
  overrides,                   receipts closed with >= 1 OverTolerance/UnderTolerance finding
  share_without_override,      1 - overrides / closed, null when closed is 0
  with_policy,                 receipts closed under a declared policy
  auto_accepted,               with_policy receipts whose every variance was inside the band
  reasons: [ { reason, count } ]   bounded, at most MaxOverrideReasons = 5,
                                  biggest count first, ties by reason ascending
}
```

The reason list is bounded exactly like `asn_timeliness`' supplier list
(ADR 0025): a bounded block is a score, an unbounded one is a table dump; the
full counts stay in `receipt_discrepancies`. `report.Kinds()` does not grow:
the policy declaration is not folded (the block reads closes, not
declarations), so the projector still folds **twelve** kinds.

## Consequences

- A supplier's normal variance stops generating findings the moment a policy
  is declared, and the escalation is honest: acceptance is silent, an
  override is recorded with a code, a hard stop is refused.
- The type catalogue goes from ELEVEN published types to TWELVE with
  `ReceivingPolicyDeclared`; the analytics topic carries the same twelve.
  Every count that says eleven is updated in this PR.
- The discrepancy vocabulary goes from seventeen kinds in `AllKinds()` to
  NINETEEN (five line kinds of `ReceiptClosed`/the advice's line entries,
  the two advice-only line kinds, twelve carton kinds — the carton kinds are
  unchanged). The projector's folded event kinds stay twelve.
- Consumers of `ReceiptClosed` see two new optional payload pieces (the
  `tolerance` object, `reason_code` on a discrepancy) and two new enum
  members of an open list; the analytics store's kind CHECK is widened by
  the same two values. Every existing field keeps its meaning.
- The scan seam judges over only; an under-variance hard stop surfaces at
  close. An operator cannot be blocked mid-receipt for a shortfall the truck
  may still fix.
- The reason codes are ours and closed: adding one is a one-line domain
  change plus an ADR edit, and unknown codes are refused with 400, never
  silently stored.
- SKU-class grain is a follow-up (point 1); the (ownerRef, supplierRef) key
  is the narrowest grain the local data supports today.

## Alternatives considered and rejected

- **A default policy (a global 5 % band) when none is declared.** The brief
  forbids it and it would change every existing close answer; no-policy must
  mean exactly today's behaviour.
- **House-default fallback for owner-keyed lookups.** Silent cross-client
  commercial terms in a 3PL: the client's supplier terms are the client's.
- **`OverTolerance` as a flag on `Over`, not a kind.** The advice's W13
  rendering, the report's Pareto and the analytics fold all switch on kind;
  a flag would leak into every consumer's switch for no gain.
- **Keeping `Short`/`Over` alongside the tolerance kinds for the same
  variance.** Double counting, exactly what ADR 0023's rule 1 forbids: the
  tolerance kinds REPLACE the plain kinds on a policy-ruled line.
- **Persisting the applied band on the receipt.** Needs a second OLTP change
  (the brief allows one table) and a backfill for old closes, and it would
  answer "which band was applied" wrong the day the policy is replaced; the
  close event is the immutable audit.
- **Judging under at the scan.** A receipt is short until it is closed; the
  scan-time under check would refuse the second carton of a two-carton ASN.
- **A free-form reason string.** A Pareto over free text is worthless; the
  closed list keeps the report honest. The codes are ours because no source
  in the research names one — stated, not hidden.
- **SKU-class grain now.** No local class data exists (point 1); inventing a
  classification or calling product-master live are both worse than shipping
  the supplier grain and documenting the follow-up.
