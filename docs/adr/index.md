---
title: Architecture decision records
sidebar_label: Index
sidebar_position: 0
slug: /
---

# Architecture decision records

Each status below is copied from the ADR body's own `## Status` line, which is
the authority; ADR bodies are never edited after acceptance.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-inbound-receiving-bounded-context.md) | inbound-receiving as the owner of the inbound dock workflow | Accepted (2026-10-08); `permissive` defaults superseded by 0011 |
| [0002](./0002-aggregates-and-invariants.md) | Aggregates and invariants | Accepted (2026-10-08) |
| [0003](./0003-local-copies-and-handover.md) | Local copies, consumed contracts and the handover to inventory-storage | Accepted (2026-10-08); `permissive` mode superseded by 0011 |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-08); per-key ordering amended by 0007 |
| [0005](./0005-mcp-server-adoption.md) | MCP server adoption (read-only, unauthenticated, additive) | Accepted (2026-10-08); in-memory fallback superseded by 0011 |
| [0006](./0006-analytics-read-side.md) | Analytics read side: analytics stream, projector and receiving performance report over a separate analytical database | Accepted (2026-10-08) |
| [0007](./0007-outbox-relay-attempt-cap.md) | Outbox relay attempt cap (`OUTBOX_MAX_ATTEMPTS`) | Accepted (2026-10-12) |
| [0008](./0008-projector-telemetry-and-environment.md) | The analytics projector sets up telemetry; every backend Deployment sets ENVIRONMENT | Accepted (2026-10-12) |
| [0009](./0009-trace-context-crosses-the-outbox.md) | The W3C trace context crosses the outbox to Kafka | Accepted (2026-10-12) |
| [0010](./0010-housekeeping-sweeper.md) | Housekeeping sweeper for the append-only tables | Accepted (2026-10-12) |
| [0011](./0011-postgres-and-kafka-are-the-only-runtime.md) | Postgres and Kafka are the only runtime | Accepted (2026-10-19); supersedes the in-memory, `log` and `permissive` statements of 0001, 0003 and 0005 |
| [0012](./0012-hierarchical-asn-with-sscc-cartons-and-carton-aware-receiving.md) | Hierarchical ASN with SSCC cartons and carton-aware receiving | Proposed (2026-10-23); additive, no existing contract changes meaning |
| [0013](./0013-carton-receiving-amendments-no-site-case-scan-cases-packaging-view.md) | Carton receiving amendments: no site, case scans count cases, local PackagingView | Proposed (2026-10-24); amends 0012 |
