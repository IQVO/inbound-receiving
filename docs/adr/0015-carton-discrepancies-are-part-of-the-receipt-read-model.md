# ADR 0015: Carton discrepancies are part of the receipt read model

## Status

Proposed (2026-10-24). Amends [ADR 0014](./0014-carton-discrepancies-over-limit-mixed-count-and-label-data.md)
(and the carton-discrepancy rules of [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md)).
The events, `ReceiptClosed` and the existing responses do not change meaning.

## Context

ADR 0012 and 0014 raise carton discrepancies as `CartonDiscrepancyRaised` events and return them once,
in the answer of `POST /receipts/{receiptId}/scans`. Nothing stored them on the receipt, so
`GET /receipts/{receiptId}` could not show them, a `MissingCarton` raised at close was visible only on
the event stream, and the web discrepancies screen had to keep the findings of its own scans in
`sessionStorage` and derive missing cartons from the ASN carton tree. A second browser, a cleared
session or an MCP/API client saw nothing.

## Decisions

1. **The receipt keeps every carton discrepancy it raises.** The aggregate appends a finding
   (`kind`, optional `sscc`, `expected`, `actual`, and the instant `at`) in the same call that creates
   the `CartonDiscrepancyRaised` event, so the stored list and the event stream cannot disagree. This
   covers all nine kinds: every scan finding, and the `MissingCarton` findings that `Close` produces
   (their `at` is the close time, the manifest SSCC is in `expected`). The list is append-only and in
   raise order.
2. **Stored in table `receipt_carton_discrepancies`** (migration 0004; additive, `ON DELETE CASCADE`
   with the receipt, primary key `(receipt_id, position)`). It is written by the receipt repository in
   the transaction that saves the receipt, its carton ledger and the outbox rows (ADR 0011), so a save
   refused by the version guard stores none of them. Receipts that closed before the migration have no
   rows and read as `[]`: the data was never kept and is not reconstructed.
3. **`GET /receipts/{receiptId}` (and every response of the `Receipt` schema: open, close, list)
   gains `cartonDiscrepancies[]`**: `{kind, sscc?, expected?, actual?, at}`. It is optional in the
   schema (additive: an older client ignores it, a consumer written before this ADR stays valid), but
   the service always sends it, `[]` when there are none, never `null`.
4. **`discrepancies` and `ReceiptClosed` are unchanged.** They keep the three line-level kinds
   `Short`, `Over` and `Damaged` (ADR 0014); carton findings are never added to them.
5. **The web discrepancies screen reads the receipt.** It lists `cartonDiscrepancies`, keeps the scan
   form (a scan posts, then the screen reads the receipt again), and no longer uses `sessionStorage` or
   the ASN carton tree for this list.

## Consequences

- One more table and one more insert per save that raised a finding; the insert sends the receipt's
  whole list and skips positions already stored. A receipt raises tens of findings, not thousands; if a
  bulk case-scan receipt ever raises more, the repository can send only the tail.
- Tools that read a receipt (MCP `get_receipt`, reports) are unchanged in this ADR; exposing the list
  there is a separate additive change.
- The event contract is untouched: same types, same payloads, same topics.
