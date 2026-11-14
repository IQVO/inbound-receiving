---
title: Architecture decision records
sidebar_label: Index
sidebar_position: 0
slug: /
---

# Architecture decision records

Each status below is copied from the ADR body's own `## Status` line, which is
the authority; ADR bodies are never edited after acceptance. The one exception
is ADRs 0012 to 0015: their bodies still read "Proposed" although they are built,
so the column records the real status and the [Status notes](#status-notes)
below give the evidence.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-inbound-receiving-bounded-context.md) | inbound-receiving as the owner of the inbound dock workflow | Accepted (2026-10-08); `permissive` defaults superseded by 0011 |
| [0002](./0002-aggregates-and-invariants.md) | Aggregates and invariants | Accepted (2026-10-08); the `DockAppointment` aggregate amended by 0019 |
| [0003](./0003-local-copies-and-handover.md) | Local copies, consumed contracts and the handover to inventory-storage | Accepted (2026-10-08); `permissive` mode superseded by 0011; third local copy added by 0019 |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-08); per-key ordering amended by 0007; the `dockappointment` types retired by 0019; their rows marked retired by 0022 |
| [0005](./0005-mcp-server-adoption.md) | MCP server adoption (read-only, unauthenticated, additive) | Accepted (2026-10-08); in-memory fallback superseded by 0011 |
| [0006](./0006-analytics-read-side.md) | Analytics read side: analytics stream, projector and receiving performance report over a separate analytical database | Accepted (2026-10-08) |
| [0007](./0007-outbox-relay-attempt-cap.md) | Outbox relay attempt cap (`OUTBOX_MAX_ATTEMPTS`) | Accepted (2026-10-12) |
| [0008](./0008-projector-telemetry-and-environment.md) | The analytics projector sets up telemetry; every backend Deployment sets ENVIRONMENT | Accepted (2026-10-12) |
| [0009](./0009-trace-context-crosses-the-outbox.md) | The W3C trace context crosses the outbox to Kafka | Accepted (2026-10-12) |
| [0010](./0010-housekeeping-sweeper.md) | Housekeeping sweeper for the append-only tables | Accepted (2026-10-12) |
| [0011](./0011-postgres-and-kafka-are-the-only-runtime.md) | Postgres and Kafka are the only runtime | Accepted (2026-10-19); supersedes the in-memory, `log` and `permissive` statements of 0001, 0003 and 0005 |
| [0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md) | Hierarchical ASN with SSCC cartons and carton-aware receiving | Accepted - implemented (the ADR body still reads "Proposed (2026-10-23)"; see [Status notes](#status-notes)); additive, no existing contract changes meaning |
| [0013](./0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view.md) | Carton receiving amendments: no site, case scans count cases, local PackagingView | Accepted - implemented (the ADR body still reads "Proposed (2026-10-24)"; see [Status notes](#status-notes)); amends 0012 |
| [0014](./0014-carton-discrepancies-over-limit-mixed-count-and-label-data.md) | Carton discrepancies: CartonOverLimit, mixed-carton audit count, label data | Accepted - implemented (the ADR body still reads "Proposed (2026-10-24)"; see [Status notes](#status-notes)); extends 0012 and 0013 |
| [0015](./0015-carton-discrepancies-are-part-of-the-receipt-read-model.md) | Carton discrepancies are part of the receipt read model | Accepted - implemented (the ADR body still reads "Proposed (2026-10-24)"; see [Status notes](#status-notes)); amends 0014 |
| [0016](./0016-a2a-inbound-adapter.md) | A2A inbound adapter (read-only, unauthenticated, additive) | Accepted (2026-10-29) |
| [0017](./0017-fleet-rest-api-guideline-cutover.md) | Fleet REST API guideline cutover (noun routes, keyed POSTs, 409 reuse, keyset paging) | Accepted (2026-11-01) |
| [0018](./0018-agent-authority-levels-and-complete-mcp-surface.md) | Agent authority levels and complete MCP surface (every tool/skill L1, stateless handler, receipt resource, receiving SOP prompt) | Accepted (2026-11-02) |
| [0019](./0019-dockappointment-moves-to-dock-scheduling.md) | The DockAppointment moves to dock-scheduling (410 `appointment-moved`, local copy fed by dock-scheduling events, `republish-appointments` backfill) | Accepted (2026-11-05); amends 0002, 0003, 0004; point 2 amended by 0020; point 5 and Rollout step 4 amended by 0022 |
| [0020](./0020-appointment-copy-follows-reschedules-and-door-reassignments.md) | The appointment copy follows reschedules and door reassignments (`DockAppointmentRescheduled`, `DockAppointmentDoorReassigned`; open receipts keep their door) | Accepted (2026-11-09); amends 0019 |
| [0021](./0021-dock-to-stock-kpi-from-yard-arrival-and-inventory-stow-facts.md) | Dock-to-stock KPI from yard arrival and inventory stow facts (`VisitCheckedIn` and `ContainerStowed` local copies, `ReceiptStowCompleted`, report fields) | Accepted (2026-11-09); extends 0003, 0006, 0019 |
| [0022](./0022-stage-e-removes-the-backfill-the-legacy-encoders-and-replay.md) | Stage E removes the backfill command, the legacy encoders and `Replay` (the four `dockappointment` types retired from the contract; the deprecated GETs and 410 routes stay) | Accepted (2026-11-10); amends 0019, 0004 |
| [0023](./0023-receiving-advice-at-close-with-standard-discrepancy-vocabulary.md) | Receiving advice at close with the standard discrepancy vocabulary (`ReceivingAdviceIssued`, W13 and CBV codes, `IncorrectProduct` and `Hold`, advice SLA and `ReceivingAdviceOverdue`) | Accepted (2026-11-10); extends 0004, 0010, 0012, 0014, 0021 |
| [0024](./0024-gs1-label-receiving-validation-destination-earliest-best-before-and-receiver-assigned-sscc.md) | GS1 logistic-label receiving: validation at the scan (`LabelNonCompliant`), the destination rule (`WrongDestination`, `RECEIVING_SITE_GLNS`), earliest best-before on mixed pallets, receiver-assigned SSCC (`AssignSscc`, `RECEIVER_GS1_COMPANY_PREFIX`) | Accepted (2026-11-10); extends 0004, 0012, 0013, 0014, 0023 |
| [0025](./0025-asn-timeliness-from-the-visit-arrival-copy-and-flat-soi-asn-receiving.md) | ASN timeliness from the visit-arrival copy (`AsnLateForArrival` at `OpenReceipt`, `registered_at`, the per-supplier `asn_timeliness` report block) and flat S-O-I ASN receiving by content-GTIN scan (`MissingContentGtin`) | Accepted (2026-11-11); extends 0004, 0021, 0012, 0014, 0023, 0024 |

## Status notes

ADRs 0012 to 0015 were never re-statused after they were built. Their bodies are
immutable, so the status is recorded here: **Accepted - implemented**, checked
against `origin/develop` on 2026-11-13. Paths are under `internal/adapters/outbound/postgres/migrations/`.

| ADR | Evidence |
| --- | --- |
| 0012 | PR #56 (the ADR) and PR #57 `feature/carton-receiving` (the implementation), both merged 2026-10-24. Migration `0003_cartons_and_packaging.up.sql` (`asn_cartons`, `asn_carton_contents`, `receipt_cartons`). `apis/openapi.yaml`: `registerAsn` (`cartons` on the ASN), `getAsnCartons` (`GET /asns/{asnNumber}/cartons`), `receiveScan` (`POST /receipts/{receiptId}/scans`). `apis/asyncapi.yaml`: `ReceiptCartonReceived`. |
| 0013 | Implemented with PR #57. Migration `0003_cartons_and_packaging.up.sql` (`pack_levels`, the local `PackagingView`; `internal/adapters/outbound/postgres/packaging.go`). `apis/openapi.yaml`: `receiveScan` with the `SSCC`, `CASE_GTIN` and `EACH_GTIN` scan kinds. |
| 0014 | PR #58 `feature/carton-discrepancies`, merged 2026-10-24. `apis/openapi.yaml`: `CartonDiscrepancyKind` (includes `CartonOverLimit` and `MixedCartonContentMismatch`), `countedContents` on a scan, `getCartonLabel` (`GET /receipts/{receiptId}/cartons/{lpn}/label`). `apis/asyncapi.yaml`: `CartonDiscrepancyRaised`. Chart and env: `CARTON_MAX_GROSS_WEIGHT_G`, `CARTON_MAX_LONGEST_SIDE_MM` (`charts/inbound-receiving/templates/configmap.yaml`). |
| 0015 | PR #60 `feature/receipt-carton-discrepancies`, merged 2026-10-24. Migration `0004_receipt_carton_discrepancies.up.sql` (`receipt_carton_discrepancies`). `apis/openapi.yaml`: `getReceipt` (`GET /receipts/{receiptId}`) returns `cartonDiscrepancies` (`ReceiptCartonDiscrepancy`). |

ADR 0017 (fleet REST API guideline cutover) is not covered by this note.
