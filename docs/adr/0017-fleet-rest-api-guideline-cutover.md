# ADR 0017: Fleet REST API guideline cutover

## Status

Accepted (2026-11-01). Follows [ADR 0002](./0002-aggregates-and-invariants.md)
(version guards, `If-Match`) and adopts the fleet REST API guideline
(`.spectral.yaml` ruleset, linted in CI).

## Context

The fleet standardises its REST surfaces: noun resources for state
transitions, an idempotency key on every `POST`, one status for key reuse, and
keyset paging for collections. inbound-receiving had verb sub-routes
(`/cancel`, `/check-in`, `/close`), keys on creating `POST`s only, `422` for
key reuse and an unpaged `GET /docks`.

## Decision

1. **Noun transition routes** (hard cutover, no aliases):
   `POST /asns/{asnNumber}/cancellations`,
   `POST /appointments/{appointmentId}/check-ins`,
   `POST /appointments/{appointmentId}/cancellations`,
   `POST /receipts/{receiptId}/closures`.
2. **Every `POST` is keyed.** `Idempotency-Key` is 1-128 printable ASCII
   (`400 idempotency-key-required` / `400 idempotency-key-invalid`). A replay
   returns the stored response with `Idempotency-Replayed: true`; the same key
   with a different request is `409 idempotency-key-reused` (was `422`). The
   key row commits in the same Postgres transaction as the aggregates and the
   outbox rows.
3. **Keyset paging.** `GET /asns`, `/appointments`, `/receipts` and `/docks`
   return `{items, nextCursor}` taking `limit` (1-500) and an opaque `cursor`;
   `400 invalid-limit`, `400 invalid-cursor`, and `400 unknown-query-parameter`
   for any undeclared parameter. `GET /asns/{asnNumber}/cartons` is exempt as a
   `view`: one ASN's bounded, versioned (`ETag`) snapshot.
4. The MCP `list_docks` tool follows the pages and still returns every door.

## Consequences

Breaking for REST callers (warehouse-floor-client, e2e-tests, the `web/`
remote, which is updated here). Event contracts are unchanged.
