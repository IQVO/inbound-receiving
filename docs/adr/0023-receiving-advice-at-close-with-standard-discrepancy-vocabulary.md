# ADR 0023: Receiving advice at close, with the standard discrepancy vocabulary (W13 and CBV)

## Status

Accepted (2026-11-10). Extends [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(two new types), [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md)
and [ADR 0014](./0014-carton-discrepancies-over-limit-mixed-count-and-label-data.md)
(the kinds are given a standard code), [ADR 0010](./0010-housekeeping-sweeper.md)
(the sweeper also detects overdue advices) and
[ADR 0021](./0021-dock-to-stock-kpi-from-yard-arrival-and-inventory-stow-facts.md)
(a second KPI block in the receiving-performance report). Does not edit their
bodies beyond one catalogue row in ADR 0004.

## Context

When a receipt closes, the supplier (or, for a 3PL, the depositor) has to be told
what arrived. The standards call it a receiving advice: EANCOM RECADV and X12 861
for a retail supplier, X12 944 for a 3PL depositor. RECADV is sent "within a
commercially agreed time period, for example 24 hours after receipt". It reports
received and accepted quantities per line, refers to the despatch advice, and
codes every discrepancy. In the 944 the code is the W13 *receiving condition*:
`01` damaged, `02` short, `03` over, `05` incorrect product, `09` hold. GS1 CBV
adds a *disposition* to each finding (`damaged`, `mismatch_quantity`,
`mismatch_class`, `non_conformant`, ...). CBV has **no `quarantined`
disposition**: a hold is the business step `holding` with the disposition
`non_conformant` or `no_pedigree_match`.

What we had at the start of this ADR:

- `ReceiptClosed` carries `Short`, `Over`, `Damaged` per line. They map to W13
  `02`, `03`, `01`, but nothing says so.
- Nine carton kinds (ADR 0012/0014/0015) with no standard code at all.
- No `incorrect product` and no `hold`.
- No event that tells the outside what was *accepted*, no despatch-advice
  reference, no SSCC list, and no deadline.

The research behind this ADR (the standards survey of the inbound competitive
study) fixes the vocabulary: W13 `01/02/03/05/09` and the CBV dispositions it
lists. The survey names the CBV values but does not define them; the pairing of
each of our kinds with a disposition below is **our decision**, stated so a
reviewer can disagree with it.

Boundary. `inbound-receiving` owns the **domain fact** only. The X12/EANCOM
rendering, the partner profiles, the transport and the 997/999 acknowledgements
are partner-connectivity's later work (the fleet decision D-3 is not taken yet).
Nothing in this ADR renders, serialises or delivers an 856/861/944 or a RECADV.

## Decision

### 1. The vocabulary: one table, in the domain

`internal/domain/receipt/vocabulary.go` holds the mapping as data and
`CodingOf(kind)` returns it. A domain table test asserts every row below and that
every kind the aggregate can raise is in the table (a new kind without a row
fails the build).

| kind | level | W13 | CBV disposition | why |
| --- | --- | --- | --- | --- |
| `Short` | line | `02` short | `mismatch_quantity` | received below expected |
| `Over` | line | `03` over | `mismatch_quantity` | received above expected |
| `Damaged` | line | `01` damaged | `damaged` | damaged units received |
| `IncorrectProduct` (new) | line | `05` incorrect product | `mismatch_class` | a carton refused because the product in it is not the product on its ASN line (see 2) |
| `Hold` (new) | line | `09` hold | `non_conformant` | units declared held (see 3); bizStep `holding` is implied by the kind |
| `UnexpectedCarton` | carton | `05` incorrect product | `mismatch_instance` | an SSCC the ASN never announced: the wrong object |
| `UnknownGtin` | carton | `05` incorrect product | `mismatch_class` | a GTIN the PackagingView cannot resolve: the product class is unknown |
| `MixedCaseDeclaredAsCasePack` | carton | `05` incorrect product | `mismatch_class` | the label names a different SKU than the manifest line |
| `MissingCarton` | carton | `02` short | `mismatch_quantity` | an announced carton never arrived |
| `DuplicateSscc` | carton | **unmapped** | `mismatch_instance` | see below |
| `CaseQtyMismatch` | carton | **unmapped** | `mismatch_quantity` | see below |
| `MixedCartonContentMismatch` | carton | **unmapped** | `mismatch_quantity` | see below |
| `UnlabelledCarton` | carton | **unmapped** | `non_conformant` | see below |
| `CartonOverLimit` | carton | **unmapped** | `non_conformant` | see below |
| `LabelNonCompliant` (ADR 0024) | carton | **unmapped** | `non_conformant` | the scanned label breaks the GS1 rules; refused at the scan, nothing booked |
| `WrongDestination` (ADR 0024) | carton | **unmapped** | `mismatch_instance` | the label's ship-to GLN (AI 410) is not a site of this receiving; refused at the scan |

**`unmapped` is a decision, not a gap.** W13 describes the condition of *goods*
at receipt. Five of our carton kinds describe the *scan or the packaging*, and no
code in the research names them:

- `DuplicateSscc`: nothing is short, over or damaged; the same label was read
  twice or an SSCC already received elsewhere was read again. No W13 code exists
  for a process event and we do not invent one.
- `UnlabelledCarton` and `CartonOverLimit`: labelling and handling limits, accepted
  goods. The CBV disposition `non_conformant` says it; W13 has no code.
- `CaseQtyMismatch` and `MixedCartonContentMismatch`: a quantity that disagrees
  inside a carton. The W13 code would be `02` or `03` depending on the sign of the
  difference, which is data, not kind. The carton is refused or its COUNTED
  quantity is booked, so the effect on the line is already carried by the
  line-level `Short`/`Over`, which have their codes. Coding the carton too would
  count the same units twice (see 4).

An unmapped kind carries its CBV disposition and **no `w13_code`** on the wire
(the field is absent; a consumer reads absence as `unmapped`). Mapping it later is
additive.

Every discrepancy payload gains two additive, optional fields, `w13_code`
(`"01"`, `"02"`, `"03"`, `"05"`, `"09"`, two characters, leading zero kept) and
`cbv_disposition`: the `CartonDiscrepancyRaised` event, the carton findings of the
REST receipt, the line discrepancies of `ReceiptClosed` and of the REST receipt,
and the new advice. The values are derived from the kind by the table and never
stored, so existing rows and goldens stay valid.

### 2. `IncorrectProduct` is raised automatically, from the carton scan

The code already knows when an item is not what its ASN line promised, at the
carton scan: a carton of the manifest is scanned and its label (GS1-128 AI 02)
names a different SKU than the manifest line (`MixedCaseDeclaredAsCasePack`) or a
GTIN that does not resolve (`UnknownGtin` with the carton's SSCC). The scan is
refused and nothing is booked (ADR 0014). That is the only automatic trigger.

A scan of a GTIN whose SKU is not on the ASN at all stays what it is today, the
422 `line-not-on-asn`: there is no line to attach a discrepancy to and changing a
refusal into a recorded finding would not be additive.

At advice time, for each manifest carton that is **still not accepted** and has at
least one of those two findings, its manifest quantity is added to the
`IncorrectProduct` quantity of its line, once per carton however often it was
scanned. The derivation is pure and needs no new column: it reads the carton
ledger and the finding log that are already persisted.

### 3. `Hold` is an explicit command, never automatic

IF-6 books `Damaged` units as held stock in inventory-storage, but nothing in
receiving says that a line should be held for another reason (a quality check, a
documentation query). `Hold` is that declaration, made by an operator or a system:
`Receipt.HoldLine(lineNo, qty, at)` and the use case `HoldReceiptLine`.

- Only on an Open receipt, qty from 1 to the Good units received and not yet held
  on the line; held units are a subset of the Good units, so
  `accepted + rejected + held = received` always holds.
- It changes the receipt version and raises **no** event of its own (no new
  published type for a declaration that the advice carries); the hold is on the
  advice and in the line's persisted `held_qty`.
- It does **not** retract the `ReceiptLineReceived` already handed to
  inventory-storage: the Good units were booked when they were received. The hold
  is the receiving-side declaration; any stock action stays inventory-storage's.
- No REST route, MCP tool or A2A skill is added by this ADR. The command is a use
  case. The MCP tool registry is unchanged on purpose (ADR 0021: the MCP server
  has no report tool, and the authority ADR 0018 would need a new decision for a
  write tool). Exposing the command is a follow-up.

### 4. No double counting

Three rules, so a consumer cannot count the same units twice:

1. **Line-level kinds are authoritative** for quantity and for the per-line W13
   code. The advice quantities of a line are exclusive: `accepted = Good received
   - held`, `rejected = Damaged received`, `held = held`, and their sum is
   `received`.
2. **Carton-level kinds are evidence**, with their own coding, and never add to
   a quantity. They are listed separately (`carton_findings`).
3. **`IncorrectProduct` explains part of a shortfall; it is not an addition to
   it.** A refused carton is not received, so its units are already inside
   `Short` (`expected - received`). The advice entry says how many of those units
   were refused as the wrong product.

A renderer (partner-connectivity) builds W13 segments from the line list. The
carton list is evidence for SSCC detail and the only place where a finding with no
line (`UnexpectedCarton`, a GTIN scan that found nothing) appears; how to render a
W13 with no line is that decision's job (D-3).

### 5. The event `ReceivingAdviceIssued`

`com.warehouse.wms.inbound-receiving.receipt.ReceivingAdviceIssued`, integration
and analytics event like the other receipt events: CloudEvents `subject` = the
receipt id, Kafka key = the ASN number, dataschema
`urn:warehouse:inbound-receiving:<events|analytics>:ReceivingAdviceIssued:v1`, the
same id on both topics (the `FanoutEncoder`).

Raised by `CloseReceipt` in the **same unit of work** as `ReceiptClosed`, after it
(the outbox keeps the order, so a consumer sees `ReceiptClosed` first). It is
exactly once per receipt on two levels: the aggregate closes once (a second close
is `ErrReceiptClosed`, a replayed HTTP request is answered from the idempotency
store), and a marker row `receipt_advices(receipt_id primary key)` is inserted with
`ON CONFLICT DO NOTHING`; only the inserter raises the event.

```
receipt_id, asn_number,
despatch_advice_ref        the ASN number, the RECADV "reference to the despatch advice" (RFF+AAK)
supplier_ref               the supplier as the ASN knows it
owner_ref?                 the depositor, for a 3PL receipt
issued_at                  the close time
last_carton_at             when the last unit was received (definition in 6)
sla_seconds, within_sla    the committed window and whether issued_at is inside it
lines[]                    {line_no, sku, gtin?, expected_qty, received_qty, accepted_qty, rejected_qty, held_qty,
                            sscc_count, ssccs[]?, ssccs_truncated?,
                            discrepancies[] = {kind, quantity, w13_code?, cbv_disposition}}
lines_total, lines_truncated?
carton_findings[]          {kind, sscc?, expected?, actual?, at, w13_code?, cbv_disposition}
carton_findings_total
```

`gtin` is the each GTIN of a received carton of the line when one is known; it is
absent for a line received only by quantity. `quantity` of a discrepancy is the
number of units the exception concerns (`Short`: `expected - received`; `Over`:
`received - expected`; `Damaged`: the damaged units; `IncorrectProduct`: the
refused units; `Hold`: the held units).

**Bounds.** A relay row the broker refuses blocks the outbox (ADR 0007), and the
broker's default message limit is 1 MiB, so the payload is bounded in the domain
and not left to the size of a receipt: at most **1000 lines**, **1000 SSCCs** over
all lines (filled in line order; a cut line has `ssccs_truncated: true` and its
exact `sscc_count`) and **200 carton findings**. When a receipt has more lines,
the lines with a discrepancy come first, then the others in line order up to the
cap, and `lines_total`/`lines_truncated` say so. The advice is then still a true
statement, only not a complete one, and the consumer reads the rest from this
context's REST (`GET /receipts/{receiptId}` for lines and findings,
`GET /asns/{asnNumber}/cartons` for the cartons). The bounds are constants of the
domain, each tested on both sides.

The event is declared in `apis/asyncapi.yaml` with a payload schema and an
example, and carries `x-no-consumer-reason`, like the other producer-only types.

### 6. The SLA and the overdue sweep

The advice is due **at most `RECEIVING_ADVICE_SLA` (default `24h`) after the last
unit was received**. `last_carton_at` is the time of the latest receiving action on
the receipt (a line quantity, an accepted carton or the cases of a scan), kept on
the receipt as `last_received_at`; for a receipt on which nothing was received it
is `opened_at`. A receipt that was open before this change has `NULL`, read as
`opened_at`.

- The advice is issued **at close**, so it is on time unless the close happens
  more than the SLA after `last_carton_at`: `within_sla = issued_at -
  last_carton_at <= sla` (equal is on time).
- The housekeeping sweeper, on its tick, selects the receipts that are still Open
  with `last_received_at` (or `opened_at`) older than the SLA and without an
  overdue marker, and raises **`ReceivingAdviceOverdue`** once per receipt:
  `receipt_id`, `asn_number`, `last_carton_at`, `due_at`, `sla_seconds`,
  `overdue_at`. Overdue means `now - last_carton_at > sla` (strictly). The marker
  `receipt_advices.overdue_at` is written in the same unit of work as the outbox
  rows, with `FOR UPDATE SKIP LOCKED` on the candidates, so replicas do not
  duplicate it. It also increments the counter
  `inbound_receiving_advice_overdue_total` and logs a WARN.
- Closing later still issues the advice, with `within_sla: false`; the marker
  row is shared, so the overdue and the issued facts of one receipt sit in one
  row and the pair is auditable.
- The sweep runs on `HOUSEKEEPING_INTERVAL` (default 1 h; `0` turns the sweeper,
  and with it the overdue sweep, off), so the alarm is late by at most one interval.
  `ReceivingAdviceOverdue` is declared like the other types and is also an
  analytics event the projector ignores.
- The counter `inbound_receiving_advice_issued_total{within_sla}` records every
  advice. `RECEIVING_ADVICE_SLA` is read by cmd/api and used by both the close
  and the sweep, so they cannot disagree. `0` or an invalid value is the default.

### 7. The KPI in the report

KPI: **% receipts with advice inside the SLA.** The projector folds
`ReceivingAdviceIssued` into a new analytical table `advice_facts` (analytics
migration `0003`, one row per receipt, `ON CONFLICT DO NOTHING`), counted in the
UTC day of `issued_at`. `GET /reports/receiving-performance` gains, additively and
next to `dock_to_stock`, an `advice` object per day:

```
advice: { count, within_sla, share_within_sla }   share = within_sla / count, null when count is 0
```

### 8. The migration and the rollout

One OLTP migration, `0007_receiving_advice`, strictly additive:

- `receipts.last_received_at timestamptz NULL` (maintained by `Save`),
- `receipt_lines.held_qty bigint NOT NULL DEFAULT 0 CHECK (held_qty >= 0)`,
- `receipt_advices` with `receipt_id` (primary key), `issued_at`, `within_sla`,
  `overdue_at`, plus the partial index the sweep uses.

**No backfill.** A receipt that was closed before this change gets **no advice**
retroactively: the advice is a commitment made at close, and a late one for an old
receipt is not an event the partner expects. There is no data loss: nothing is
deleted or rewritten, the new columns are nullable or defaulted, and the cluster
holds few or no receipts. Open receipts keep working, with the SLA clock starting
at their `opened_at` when nothing was received.

Rollout order: the **projector first**, then `cmd/api`, then
`cmd/inbound-reports`. A projector that does not know the type yet ignores it (an
unknown type is acknowledged and skipped, ADR 0006), so an inverted order loses no
message from the outbox, but the KPI would have a gap for the advices issued
before the projector runs (the analytics topic is replayable under a new consumer
group). The analytical migration `0003` is applied by the projector; the reports
service must be deployed after it because its query reads `advice_facts`.

## Consequences

- The supplier-facing fact exists, with the standard codes, in a committed window,
  and the SLA is measured. Rendering and delivery are the next context's job and
  start from a stable event.
- `ReceiptClosed.discrepancies` keeps its three line kinds (the contract of every
  consumer and of the analytics projector is unchanged); `IncorrectProduct` and
  `Hold` live on the advice, and the REST receipt gains the two code fields on the
  findings it already returns. A consumer of `CartonDiscrepancyRaised`
  (exception-management) sees two new optional fields and nothing else.
- Every new payload field is additive and optional; no existing event, type, kind
  or field changes. The MCP tool registry golden is untouched.
- The advice is bounded and therefore sometimes partial (lines past 1000, SSCCs
  past 1000, findings past 200); the counts say so and the REST read completes it.
- `Hold` has no adapter yet: it is reachable from code and tests, not from REST or
  MCP, until a follow-up decides its surface and authority.
- `unmapped` carton kinds are visible and countable; the day partner-connectivity
  needs a code for one of them, the table is the single place to change.
- warehouse-infra gets one new env, `RECEIVING_ADVICE_SLA` (chart value
  `config.receivingAdviceSla`, default `24h`), on the API deployment. No consumer
  group is added. The e2e harness needs the same env on the API service (it may
  stay unset: the default applies).

## Alternatives considered and rejected

- **Make `IncorrectProduct` and `Hold` kinds of `ReceiptClosed`.** It would show
  them to every consumer of the closing line kinds (network-fulfillment turns them
  into the vendor receipt, yard-management completes the unload) and to the
  analytics projector, whose store validates the three kinds. The advice is the new
  event that can carry them without breaking a reader.
- **Raise `IncorrectProduct` for a GTIN whose SKU is not on the ASN.** Today that
  is a 422 and a test; turning it into a finding changes an existing answer, and
  there is no line to hold it.
- **Auto-hold `Damaged` units.** IF-6 already books them as held stock; a second
  hold fact for the same units would be the double count rule 1 forbids.
- **Store the codes.** They follow from the kind; a stored copy would drift from
  the table and force a backfill.
- **A separate scheduler or Kubernetes CronJob for the overdue sweep.** The
  sweeper already ticks on `HOUSEKEEPING_INTERVAL` in every replica; a second
  clock would need its own env, chart entry and leader rule.
- **Map the five process kinds to the nearest code.** A code that does not mean
  what it says on the supplier's side is worse than an explicit `unmapped`.
