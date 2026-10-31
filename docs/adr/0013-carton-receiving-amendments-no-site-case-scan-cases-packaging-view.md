# ADR 0013: Carton receiving amendments: no site, case scans count cases, local PackagingView

## Status

Proposed (2026-10-24). Amends [ADR 0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md)
where the implementation resolved its open points; everything else in 0012 stands.
Source: `.hermes/plans/mastercase-briefs/contracts.md` section 8 (R6, R7, R8) of the
warehouse-systems workspace.

## Decisions

1. **No `siteId` on carton events (R6).** This service has no site concept: staged stock is
   unlocated, and inventory-storage derives the site at `StowContainer` from the bin's site.
   `ReceiptCartonReceived` therefore carries no site, and an internal license plate is
   `LPN-<ulid>`, not `LPN-<siteId>-<ulid>`. The sentence in 0012 that the service needs a site
   no longer applies.
2. **A `CASE_GTIN` scan counts cases (R7).** `qty` on a case-GTIN scan is a number of cases
   (default 1); the service multiplies by the active CASE `unitsPerPack` from `PackagingView`.
   An `EACH_GTIN` scan adds `qty` eaches (default 1); an `SSCC` scan accepts the whole carton
   manifest. Events and the ledger always carry eaches, and `ReceiptLineReceived` records the
   `cases` count and `pack_gtin` as optional fields next to its unchanged `quantity`.
3. **`PackagingView` is a local projection (R8).** It is fed by a dedicated consumer of
   product-master's `ProductPackLevelDeclared` / `ProductPackLevelRetired` events (stable group
   from `PACKAGING_CONSUMER_GROUP`, required like the other two groups, with an inbox for
   idempotency). It is looked up by GTIN and by SKU plus level. No scan or receipt calls
   product-master synchronously.

## Consequences

- The chart renders `PACKAGING_CONSUMER_GROUP` and refuses to render without
  `config.packagingConsumerGroup`; warehouse-infra must set it for this service.
- Payloads are snake_case, this service's shipped casing (the contract's camelCase names map
  mechanically).
- 0012's open points on `siteId` and on case-scan quantity are closed by this ADR.
