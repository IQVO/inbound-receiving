# ADR 0014: Carton discrepancies: CartonOverLimit, mixed-carton audit count, label data

## Status

Proposed (2026-10-24). Extends [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md)
and [ADR 0013](./0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view.md); nothing
in them changes meaning. Source: `.hermes/plans/mastercase-briefs/contracts.md` section 9 (R9, R10,
R11) of the warehouse-systems workspace.

## Context

ADR 0012 left three things open: the `CartonOverLimit` kind (this service holds no site handling
limits), what a scan of a MIXED carton does when the dock worker counts it, and what happens to a case
that arrives with no readable SSCC. Section 9 of the contracts decided all three.

## Decisions

1. **`CartonOverLimit` is checked at acceptance (R9).** Limits come from the env
   `CARTON_MAX_GROSS_WEIGHT_G` and `CARTON_MAX_LONGEST_SIDE_MM` (chart `config.cartonMaxGrossWeightG` /
   `config.cartonMaxLongestSideMm`); `0` or unset means unchecked, a negative or unparsable value logs a
   warning and is unchecked, and the chart refuses a negative value. They are compared with the carton's
   DECLARED `grossWeightG` and the longest side of its declared `dims` (the ASN is read only when a limit
   is configured). Node-topology limits arrive in Phase 4, so there is no cross-context call. An
   over-limit carton is **still accepted**: it is physically on the dock, and goods on the dock are never
   silently rejected. The service raises one `CartonDiscrepancyRaised` of kind `CartonOverLimit` per
   carton, with `expected` the breached limits and `actual` the declared values, written as
   `grossWeightG:<n>` and `longestSideMm:<n>` pairs joined by a comma (one finding per carton because the
   exception case is keyed by `(receiptId, sscc, kind)`). A carton that declared nothing is never over a limit.
2. **A MIXED carton can be audited by count (R10).** A mixed carton has no pack GTIN, so its SSCC scan
   accepts the manifest per SKU, each line in eaches. The scan may carry `countedContents[{sku, qty}]`.
   When it does, the quantity RECEIVED is the counted one, never the label's, and every SKU whose count
   differs from the manifest raises the new kind **`MixedCartonContentMismatch`** with `expected` and
   `actual` written as `sku:qty`. A manifest SKU missing from the count counts as zero; a SKU the manifest
   does not list is booked on its ASN line (it needs an each GTIN in the PackagingView, otherwise 422
   `counted-sku-unknown`). `countedContents` on a homogeneous carton is 422 `counted-contents-not-mixed`,
   on a non-SSCC scan 400 `invalid-scan`; a repeated SKU or a negative count is 400
   `invalid-counted-contents`. A case GTIN scan can never resolve to a MIXED carton.
3. **An unlabelled carton gets an internal LPN and label data (R11).** A case received without a
   readable SSCC against a carton ASN raises `UnlabelledCarton` (already so) and is given the internal
   plate `LPN-<ulid>` (`idgen.NewLPN`); SSCC-labelled cartons keep their SSCC as LPN. New
   `GET /receipts/{receiptId}/cartons/{lpn}/label` returns label DATA only (`lpn`, `symbology`
   `CODE128`, `humanReadable`, `sku` when the carton holds one SKU, `units` in eaches, `receivedAt`); the
   service has no ZPL or printer driver and the floor client prints through the browser. A carton the
   receipt has not accepted is 404 `carton-not-found`.
4. **Carton findings still travel only on `CartonDiscrepancyRaised`.** `ReceiptClosed` keeps its three
   v1 kinds (`Short`, `Over`, `Damaged`). The API response of an accepted scan lists the findings it raised.
5. **The API and event contracts are documented.** `apis/openapi.yaml` now describes
   `POST /receipts/{receiptId}/scans`, `GET /asns/{asnNumber}/cartons`, the optional `ownerRef` and
   `cartons` of `POST /asns`, and the label endpoint; `apis/asyncapi.yaml` describes
   `ReceiptCartonReceived` and `CartonDiscrepancyRaised` (integration and analytics topics) and the
   optional fields added to `ASNRegistered` and `ReceiptLineReceived`. There is no endpoint that adds a
   carton to an existing ASN.

## Consequences

- exception-management (R12) consumes `CartonDiscrepancyRaised` for the two new kinds; `CartonOverLimit`
  and `UnlabelledCarton` are informational for the receiving lead, the rest block in problem-solve.
- Every new env var is rendered by the chart (ConfigMap, Deployment, helpers validation, values, render
  test) and documented in the configuration reference.
- Mixed-carton audit changes the booked quantity of a scan; a retry is still protected by the
  `Idempotency-Key` that `POST /receipts/{id}/scans` requires.
