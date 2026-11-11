# ADR 0024: GS1 logistic-label receiving: validation at the scan, the destination rule, earliest best-before, receiver-assigned SSCC

## Status

Accepted (2026-11-10). Extends [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(three additive payload fields, no new type), [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md),
[ADR 0013](./0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view.md)
and [ADR 0014](./0014-carton-discrepancies-over-limit-mixed-count-and-label-data.md)
(two new carton kinds; the unlabelled carton gets a way to receive our own SSCC) and
[ADR 0023](./0023-receiving-advice-at-close-with-standard-discrepancy-vocabulary.md)
(two rows for its vocabulary table, one optional field on its advice lines). Does not edit
their bodies: the new rows are stated here, in `internal/domain/receipt/vocabulary.go` and
in the living docs.

## Context

The standards survey of the inbound competitive study (item IF-1) says a receiver must accept
every compliant GS1 logistic label, refuse a non-compliant one when it is scanned and not at
close, refuse a unit addressed to another site, show the earliest best-before date of a mixed
pallet (the FIFO best practice puts the earliest date on the label and on the despatch advice)
and may give its own SSCC, from its own GS1 Company Prefix, to a unit that arrives without one.

What the code did before this ADR:

- `gs1.ParseScan` read AIs 00, 01, 02, 10, 17 and 37. Any other AI was `ErrUnknownAI` and the
  whole scan a 400 `invalid-scan`; nothing was recorded. The failed scan left no trace on the
  receipt, so the supplier's label quality could not be measured.
- A `]C1` label carrying 15/21/400/410 was refused outright; one carrying 11/13/17 had the
  extra data dropped on the floor: the SSCC scan used the manifest's lot and expiry and ignored
  the label's.
- Nothing compared the ship-to location of a label with this site.
- A case received by its case GTIN against a carton ASN got an internal `LPN-<ulid>` and the
  finding `UnlabelledCarton` (ADR 0014 section 3). There was no way to give it an SSCC.

The template's shared package now carries the AIs, the GLN value object, `ParseScanAt` (the GS1
century window) and `BuildSSCC` (PR #55 of warehouse-harness-template). This repo re-synced it
unchanged (the commit before this ADR's code).

**Boundary.** Everything here is declarative or local: this context may not call a sibling.
No sibling publishes a site GLN (facility-layout has none), so the receiving GLNs are
configuration. No printing, no ZPL and no printer integration is built here; the floor client
renders what the service returns.

## Decision

### 0. The re-sync and what changed because of it

Only `internal/domain/shared/gs1` differed from the template. The behaviour change:
AIs 11, 13, 15, 21, 400, 410 and 413 used to be `ErrUnknownAI` (HTTP 400 `invalid-scan`) and now
parse. A scan that carried one of them is therefore no longer refused on that ground; its extra
data is carried (section 3) and, for 410, checked (section 2). No test, feature or use case of
this repo relied on the old rejection (searched: every `]C1` scan in the tests carries only
00/01/02/10/17/37). The use case now reads dates with `ParseScanAt(scan, year of the
clock)`, the GS1 century window, instead of `ParseScan` (always 20YY): for any date within
fifty years of today the two agree.

### 1. Label validation at the scan: `LabelNonCompliant`

A scan is a **GS1-128 element string** when it starts with the symbology identifier `]C1`, has
a non-empty body of at most 512 characters (the limit of `scan` in the OpenAPI contract) and the
body holds only printable ASCII (0x21 to 0x7E) and the GS separator (0x1D). Anything else
that fails to parse (junk, `]E0`/`]I1` data that is not a GTIN, an unsupported symbology, a
binary blob) is **not** an element string and stays what it is today: a 400 `invalid-scan`
that records nothing. A raw scan on a receipt that does not exist stays 404 and on a closed
one 409 `receipt-closed`.

A GS1-128 element string that violates the standard, scanned on an **open** receipt, is
**refused and recorded**: nothing is booked, the receipt keeps its version history like for
every other refused scan (the version advances), one finding of kind **`LabelNonCompliant`**
is raised, and the answer is the one of every refused scan, **200** with `accepted: false` and
the finding in `discrepancies` (not a 4xx: the scan was understood and refused, exactly like
`UnexpectedCarton`; clients already read this shape). It is one finding per refused scan.

The finding is a `CartonDiscrepancyRaised` with:

| field | value |
| --- | --- |
| `kind` | `LabelNonCompliant` |
| `expected` | the stable **reason code** (table below) |
| `actual` | the scanned string, GS shown as `<GS>`, cut at **128 characters** (a safe bound for the payload; a longer scan keeps its first 128) |
| `supplier_ref` (new, optional) | the supplier as the ASN knows it (the use case reads the ASN, as the advice does) |
| `sscc` | absent (a label that did not parse has no trustworthy SSCC) |

The reason code comes from the error value the gs1 package returns, never from its text,
except for two cases where the package returns a message and no value (a bad date, a bad
character set), which are told apart by the package's own fixed text and pinned by a test, so
a template change that rewords them fails here first:

| reason code | gs1 error |
| --- | --- |
| `bad_check_digit` | `ErrInvalidSSCC`, `ErrInvalidGTIN` or `ErrInvalidGLN` whose message says the check digit does not match |
| `invalid_identifier` | any other `ErrInvalidSSCC`/`ErrInvalidGTIN`/`ErrInvalidGLN` (length, non-digit) |
| `variable_measure` | `ErrVariableMeasure` |
| `conflicting_ais` | `ErrConflictingAIs` (AI 01 together with AI 02) |
| `missing_ai` | `ErrMissingAI` (AI 02 without 00 and 37, 37 without 02, nothing identifying) |
| `duplicate_ai` | `ErrDuplicateAI` |
| `unknown_ai` | `ErrUnknownAI` |
| `bad_date` | the date AIs 11/13/15/17: not YYMMDD, month or day out of range |
| `bad_character` | lot, serial or purchase order outside the GS1 82 character set |
| `malformed_element` | every other `ErrInvalidScan` (an element shorter than its fixed length, a variable length of 0 or above its maximum, a count that is not a number) |

A reason code is a contract: the set is closed and a new one is additive.

**W13 and CBV.** `LabelNonCompliant` has no W13 code (`unmapped`: W13 describes the
condition of goods, and nothing in the research names a code for a label) and the CBV
disposition `non_conformant` (a failed conformance inspection of the label), the same
pairing as `UnlabelledCarton` and `CartonOverLimit` in the table of ADR 0023.

### 2. The destination rule (AI 410): `WrongDestination`

Configuration `RECEIVING_SITE_GLNS`: a comma-separated list of 13-digit GLNs, every one
validated with `gs1.ParseGLN` at startup (blank entries are ignored, a duplicate is harmless);
an invalid one **refuses to boot**, naming the variable. **Empty or unset turns the rule
off** (backward compatible: a scan with AI 410 is accepted and the GLN is stored).

With the rule on, a raw scan whose label carries AI 410 that is not in the list is refused at
the scan, before the SSCC, the manifest or the packaging are looked at, and recorded as
**`WrongDestination`**: kind `WrongDestination`, `actual` the GLN on the label, `expected`
absent (the list is configuration, not a fact of the ASN), `sscc` the label's SSCC when it
has one, `supplier_ref` as above. Nothing is booked; the answer is 200 `accepted: false`. A
label with no AI 410 is never refused by this rule; a label that fails section 1 is refused
for that reason first.

AI 413 (ship-for) is parsed and stored and **never enforced**.

W13 `unmapped`, CBV **`mismatch_instance`**: the object received is not the one this site was
to receive, the same pairing as `UnexpectedCarton` (the wrong instance), not `mismatch_class`
(the product is not in question) and not `mismatch_quantity`.

**Follow-up, not built here.** The cleaner source of the site GLN is facility-layout owning and
publishing it as an event this context consumes into a local copy (ADR 0003 shape). Until it
does, the configuration is the single source and an operator must keep it right.

### 3. Label data and the earliest dates on mixed pallets

An accepted carton keeps, additively and optionally, what its label said: `best_before` (AI
15), `expiry` (AI 17, or the request's `expiry` which wins as it always did for the case path),
`production_date` (AI 11), `packaging_date` (AI 13), `serial` (AI 21), `purchase_order` (AI
400) and `ship_to_gln` (AI 410). Dates are ISO `YYYY-MM-DD`.

**DD=00.** GS1 writes "day not specified" as `00`. The package already reads a scan's `00` as
the last day of that month, so a date that came from a scan is stored resolved (`270300` is
stored `2027-03-31`). A date string with a literal `-00` day (a request's `expiry`, an ASN
content) is stored as given and **compared as the last day of its month**, by the same rule. A
string that is not a date is never the earliest of anything (it is skipped, not an error).

**Derived, never stored:** `earliest_best_before` and `earliest_expiry`, the minimum over the
ACCEPTED cartons concerned, per **pallet** (cartons sharing a parent SSCC) and per **receipt
line** (cartons whose contents include the line). Equal dates give the date once; a carton
with no date is ignored; when no carton has one the field is absent. A carton's expiry is its
label `expiry`, else the earliest expiry among its contents.

Where it is exposed (all additive and optional):

- `ReceiptCartonReceived`: `sscc_source` (section 4) and the seven label fields above.
- The REST scan answer's carton, the same fields; `GET /receipts/{id}`: per line
  `earliestBestBefore` and `earliestExpiry`, and `pallets[]` `{parentSscc, cartonCount,
  earliestBestBefore?, earliestExpiry?}`.
- `ReceivingAdviceIssued.lines[]`: `earliest_best_before`, `earliest_expiry`.
- `CartonDiscrepancyRaised` and the receipt's findings: `supplier_ref` (sections 1, 2).

### 4. `AssignSscc`: our own SSCC for a carton that arrived without one

**How an unlabelled carton arises today.** `ReceiveCases` (a scan of a case GTIN against a
carton ASN) books the case, gives it `LPN-<ulid>` and raises `UnlabelledCarton`. The
`ReceiptCartonReceived` for that LPN has already left through the outbox, and two consumers
read it, each with its own key: inventory-storage links by `(receipt_id, line_no, lpn)`, while
network-fulfillment stores its carton by `sscc`, else `lpn`. **Re-issuing the event for the
same carton with an SSCC added afterwards would therefore create a second carton in
network-fulfillment.** For that reason this ADR does not retro-label an accepted carton, and
does not add an event type.

**The flow.** `AssignSscc` is a command at the moment of arrival, for a case that has no
label: the operator scans its case GTIN, asks for an SSCC, and the service **receives that one
case with an SSCC of the receiver's own**: it books it exactly like a case scan of one case
(line, quantity, pack GTIN) but the carton is created with the allocated SSCC as its SSCC and
LPN, `sscc_source: receiver`, and the same `UnlabelledCarton` finding is raised against a
carton ASN (a carton that arrived without a label is still that finding; `actual` is the new
SSCC). The answer carries what a printer needs. A receipt of more cartons is more calls.
Receiving the same case by the ordinary GTIN scan (internal LPN, no SSCC) is unchanged.

- **Route:** `POST /receipts/{receiptId}/sscc-assignments`, a keyed POST (`Idempotency-Key`
  required, replay answers the first response with `Idempotency-Replayed: true`), a
  sub-resource collection of verbs-as-nouns like `closures`. Body (camelCase, unknown fields
  rejected): `gtin` (the case GTIN, required), `condition` (default Good), `lot`, `expiry`.
  Tag Receipts; no MCP tool, no A2A skill (the MCP write tools are the separate decision
  D-4; the registry golden does not change).
- **Answer, 201:** `lpn`, `sscc`, `humanReadable` (`(00)` + 18 digits), `elementString` (`00`
  + 18 digits: the data a GS1-128 printer encodes, with AI 00 and no parentheses),
  `symbology` (`GS1-128`), `ssccSource` (`receiver`), `sku`, `units` (eaches), `discrepancies`
  (the `UnlabelledCarton` finding when one was raised), `version`.
- **Configuration `RECEIVER_GS1_COMPANY_PREFIX`:** 7 to 10 digits, validated at startup (an
  invalid value refuses to boot). It is a **licensed GS1 asset the business must supply: there
  is no default value.** Unset = the command answers **409 `sscc-prefix-not-configured`**, an
  honest refusal and not a fake SSCC; nothing else is affected.
- **Allocation.** Extension digit 0 and `gs1.BuildSSCC(0, prefix, serial)`. The serial comes
  from a **Postgres SEQUENCE per prefix** (`sscc_serial_<prefix>`, created on demand and at
  boot, `NO CYCLE`, `MAXVALUE` = the largest serial that fits after the prefix: 10^9 - 1 for a
  7-digit prefix down to 10^6 - 1 for 10 digits). A value is never given twice, even when
  the surrounding transaction rolls back (a rolled back request leaves a gap; **gaps are
  allowed**, reuse is not). An exhausted sequence is 409 `sscc-serial-exhausted`. A changed
  prefix starts a new sequence, so two prefixes never share one. The partial unique index
  `receipt_cartons_accepted_sscc` is the second line against a duplicate.
- **Other refusals.** A GTIN the PackagingView does not know, or whose level is not CASE: 422
  `unknown-gtin` / `scan-kind-mismatch` (nothing is allocated); a SKU not on the ASN: 422
  `line-not-on-asn`; a closed receipt: 409 `receipt-closed`; the prefix check comes before
  any lookup.
- **`sscc_source`** is `supplier` for a carton received by scanning an SSCC label and
  `receiver` for one given an SSCC by this command; it is absent for a carton with no SSCC.
  It is derived on read (an SSCC-carrying row written before this ADR is `supplier`), so no
  backfill. It is on `ReceiptCartonReceived` and the REST carton.
- **Out of scope.** Printing, ZPL and the printer integration: the floor client renders the
  returned data. The KPI "% cartons with receiver-assigned SSCC per supplier" is derivable
  from `sscc_source` plus the supplier of the ASN, but the analytics projector folds no
  carton event today and this ADR does **not** extend it: the report is deferred to the
  decision that gives the projector a carton table (it needs a new analytical migration and a
  rollout order of its own).

### 5. The vocabulary rows (extends the table of ADR 0023)

| kind | level | W13 | CBV disposition | why |
| --- | --- | --- | --- | --- |
| `LabelNonCompliant` | carton | **unmapped** | `non_conformant` | the label failed the standard's checks; the goods are not described by W13 |
| `WrongDestination` | carton | **unmapped** | `mismatch_instance` | the unit is addressed to another location |

There are now **eleven** carton kinds (the nine of ADR 0012/0014 plus these two) and sixteen
kinds in `AllKinds`. `w13_code` is absent on the wire for both, `cbv_disposition` present, as for
every unmapped kind. Neither kind is added to `ReceiptClosed`.

### 6. The migration and the rollout

One OLTP migration, `0008_gs1_label_receiving`, strictly additive and without backfill:

- `receipt_cartons.sscc_source` (`TEXT NULL`, `supplier` or `receiver`) and
  `receipt_cartons.label` (`JSONB NULL`, the label data of section 3);
- `receipt_carton_discrepancies.supplier_ref` (`TEXT NULL`) and the `kind` check widened by the
  two kinds (the constraint is dropped and recreated with the eleven values);
- nothing for the sequences: they are created by the allocator.

Open receipts keep working. Rollout needs no order: a consumer that does not know the new
optional fields ignores them, and a consumer that does not know the two kinds should treat them
as it treats any carton kind it has no rule for (exception-management routes an unknown kind
to problem-solve, read from its code; adding explicit routes for the two kinds is its own
follow-up). warehouse-infra needs two optional env vars on the API deployment
(`RECEIVING_SITE_GLNS`, `RECEIVER_GS1_COMPANY_PREFIX`); the e2e harness needs the same two to
exercise the paths, both may stay unset.

## Consequences

- A label that violates the standard is visible, countable per supplier and refused the moment
  it is scanned. A client that parsed such a 400 `invalid-scan` for a `]C1` string on an open
  receipt now reads a 200 with `accepted: false` and a `LabelNonCompliant` finding; junk input
  and a missing or closed receipt answer as before.
- The destination rule is as good as its configuration: with the variable empty nothing is
  checked, which is the safe default for a site that has not set it.
- `AssignSscc` is available only where a prefix is configured. A mixed carton or one with no
  GTIN visible cannot be given an SSCC by this command; it stays on the internal LPN.
- Every new payload field is additive and optional; the event types stay **ten**; no existing
  type, kind or field changes meaning. The MCP tool registry golden is untouched.
- Carton kinds go from nine to eleven; the REST and AsyncAPI enums, the docs and the web copy
  that lists the kinds must follow (the web micro-frontend is not changed by this ADR).

## Alternatives considered and rejected

- **A 400/422 for a non-compliant label, with the finding written behind the error.** An error
  response commits nothing in this service's use-case shape, and a recorded refusal is how
  every other refused scan already answers.
- **Record every unparsable scan.** Junk (a typo, a wrong symbology, noise) would fill the
  receipt with findings no supplier caused; only an element string that was attempted as a
  GS1 label is a label finding.
- **`AssignSscc` on an accepted unlabelled carton, re-emitting `ReceiptCartonReceived`.** It
  would create a second carton in network-fulfillment (key `sscc` else `lpn`), see section 4.
- **A new event type for the assignment.** The type catalogue stays ten; the assignment is
  part of receiving the carton.
- **A default company prefix, or a made-up one.** The prefix is licensed by GS1 to a company;
  an SSCC under a prefix the business does not own can collide with another company's.
- **One counter row with a lock, instead of a sequence.** A row lock serialises receivers on
  the dock; a sequence does not block and never repeats.
- **Map the new kinds to W13 05.** `WrongDestination` is not an incorrect product and a label
  defect describes no goods; an explicit `unmapped` is better than a code that says otherwise.
