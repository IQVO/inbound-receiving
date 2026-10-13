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
| [0001](./0001-inbound-receiving-bounded-context.md) | inbound-receiving as the owner of the inbound dock workflow | Accepted (2026-10-08) |
| [0002](./0002-aggregates-and-invariants.md) | Aggregates and invariants | Accepted (2026-10-08) |
| [0003](./0003-local-copies-and-handover.md) | Local copies, consumed contracts and the handover to inventory-storage | Accepted (2026-10-08) |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-08); per-key ordering amended by 0007 |
| [0005](./0005-mcp-server-adoption.md) | MCP server adoption (read-only, unauthenticated, additive) | Accepted (2026-10-08) |
| [0006](./0006-analytics-read-side.md) | Analytics read side: analytics stream, projector and receiving performance report over a separate analytical database | Accepted (2026-10-08) |
| [0007](./0007-outbox-relay-attempt-cap.md) | Outbox relay attempt cap (`OUTBOX_MAX_ATTEMPTS`) | Accepted (2026-10-12) |
| [0008](./0008-projector-telemetry-and-environment.md) | The analytics projector sets up telemetry; every backend Deployment sets ENVIRONMENT | Accepted (2026-10-12) |
