---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
---

# Troubleshooting

Symptom → cause → check → fix, built from the failure paths that exist in the
code. Problem-document `type` URIs are
`https://errors.inbound-receiving.warehouse-systems.dev/<slug>`
(`internal/adapters/inbound/http/errors.go`). Log messages are quoted exactly;
see [Observability](./observability.md) for their fields.

## Boot and readiness

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| Pod exits at start with `DATABASE_URL is required`, `KAFKA_BROKERS is required`, `PRODUCT_CONSUMER_GROUP is required` or `DOCK_DOOR_CONSUMER_GROUP is required` | `cmd/api` `loadConfig` refuses an incomplete environment before opening anything (ADR 0011); `cmd/mcp` needs `DATABASE_URL` | `kubectl logs` first line; rendered env of the Deployment | set the named variable (the chart refuses to render without it) |
| Exit with `EVENT_PUBLISHER=log is not supported`, `PRODUCT_MODE=permissive is not supported` (or `DOCK_DOOR_MODE`, or `off`) | a retired value: Postgres and Kafka are the only runtime | env | unset the variable or set it to `kafka` |
| Projector/reports exit with `ANALYTICS_DATABASE_URL is required ...` or `KAFKA_BROKERS is required ...` | required env missing | env of the analytics Deployments | set `analytics.database.*` and `kafka.enabled` |
| Pod never answers `/healthz`, restarts after ~15–20 s with `run migrations (after 5 attempts): ...` or `ping postgres (after 5 attempts): ...` | Postgres unreachable or wrong DSN; the listener starts only after migrations + first ping | `retrying` WARN lines (`op`, `attempt`, `err`) | fix DSN/network/credentials; the budget is 5 attempts with 1+2+4+8 = 15 s of backoff and is not configurable |
| Migration fails with a lock or `prepared statement` error | `DATABASE_URL` points at PgBouncer in transaction mode and no `MIGRATIONS_DATABASE_URL` is set | which DSN the Secret holds | add the direct DSN under the `MIGRATIONS_DATABASE_URL` key (api, mcp) / make `ANALYTICS_DATABASE_URL` direct (projector) |
| Migration error `Dirty database version 1` | a previous migration failed half-way | `SELECT * FROM schema_migrations` | fix the cause, set `dirty = false` on the right version, restart |
| `/readyz` is `200` but writes return `422 unknown-sku` / `unknown-dock-door` right after deploy | readiness never waits for the local-copy consumers; a new group is still replaying the source topic | `product registered processed` / `location slot event processed` INFO lines still streaming | wait for the replay, or run `permissive` until the copy is filled |
| `/readyz` returns `503` | `{"status":"not_ready"}`: shutdown has started (pod `Terminating`, expected). A `dependency-unavailable` problem (`detail: not ready: postgres, kafka`): that dependency does not answer (cached 3 s) | `readiness: dependency is not ready` WARN lines with the cause | restore the named dependency; the pod turns ready by itself |
| Pod restarts with `product-consumer stopped unexpectedly` / `outbox relay stopped unexpectedly` | a worker returned on its own (usually the broker became unreachable); the service exits so the orchestrator restarts it | the ERROR line before the exit | restore Kafka; expected during a broker outage |
| MCP tools return nothing although REST shows data (local) | `cmd/mcp` points at a different `DATABASE_URL` than `cmd/api` | the DSN of each | point both at the same Postgres |

## REST problem types

| Status / slug | Cause | Check | Fix |
|---|---|---|---|
| `400 idempotency-key-required` | `POST /asns`, `/appointments`, `/receipts` or `/receipts/{receiptId}/lines` sent without `Idempotency-Key` | request headers | send a unique key (≤ 255 chars) per logical request |
| `400 malformed-request` | invalid JSON, unknown field, unparsable `If-Match`, body over the limit, or a key longer than 255 chars | request body/headers | fix the request |
| `422 idempotency-key-reused` | the same key was used earlier with a different method + path + body (SHA-256 fingerprint) | `idempotency_keys.request_hash` for that key | use a new key for a new request; a true retry must resend identical bytes |
| Retry with the same key returns the **old** response | intended: a committed key replays its stored status, headers and body | `idempotency_keys.status_code` | none; 5xx responses are never stored, so they can be retried with the same key |
| `500` with `idempotency key vanished while replaying; retry the request` in the log | two concurrent requests with one key and the first rolled back | log `idempotency store failed` | retry the request |
| `409 concurrent-modification` | another request saved the same aggregate between load and save (version guard) | — | re-fetch (`GET`) and retry |
| `412 version-mismatch` | `If-Match` carried a stale version | compare with the current `ETag` | re-fetch and resend with the new `ETag` |
| `409 asn-already-exists` | ASN number already registered | `GET /asns/{asnNumber}` | use a new number, or treat as already done |
| `409 asn-in-progress`, `asn-terminal`, `asn-not-receivable` | ASN state does not allow the command (cancel while receiving, act on Closed/Cancelled, open a receipt on a non-receivable ASN) | `GET /asns/{asnNumber}` `state` | follow the lifecycle in [Receiving workflow](../overview/receiving-workflow.md) |
| `409 appointment-not-booked`, `appointment-not-checked-in`, `outside-check-in-window` | appointment state or time does not allow check-in/cancel/open | `GET /appointments/{appointmentId}` | check in inside the allowed window first |
| `409 door-window-overlap` | another active (Booked/CheckedIn) appointment overlaps the window on that door | `GET /appointments?door=...` | pick another window or door |
| `409 receipt-already-open` | the ASN already has an open receipt (unique partial index `receipts_one_open_per_asn`) | `GET /receipts?asnNumber=...&state=Open` | continue the open receipt |
| `409 receipt-closed` | line received on a closed receipt | `GET /receipts/{receiptId}` | open a new receipt only if the ASN is still receivable |
| `422 unknown-sku` | `PRODUCT_MODE=kafka` and the SKU is not in `known_skus` | `SELECT * FROM known_skus WHERE sku = ...`; product consumer logs | register the product in product-master; wait for the event |
| `422 unknown-dock-door` | `DOCK_DOOR_MODE=kafka` and the door is not an `Inbound`/`Both` door in `dock_doors` | `GET /docks`; dock-door consumer logs | register/flag the door in facility-layout |
| `422 unknown-asn`, `unknown-appointment`, `asn-not-on-appointment`, `line-not-on-asn` | a referenced aggregate or line does not exist / is not covered | the referenced resource | fix the reference |
| `400` validation slugs (`invalid-sku`, `invalid-quantity`, `invalid-window`, `window-in-past`, `duplicate-sku`, …) | domain value rejected | `detail` of the problem | fix the input |
| `500 internal-error` | any unmapped error (usually the database) | api logs around the request | fix the dependency |
| Browser call blocked by CORS | origin not in `CORS_ALLOWED_ORIGINS` (default only `http://localhost:5173`) | browser console | add the origin; in kind Kong's CORS plugin handles browsers |

## Kafka and the outbox

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| Events are not on the topic yet, `outbox_events.published_at` is NULL and `outbox relay pass failed` says `Unknown Topic Or Partition` | the first write to a topic the broker has not created yet; auto-creation answers once with that error | the next pass succeeds | none, the relay retries; the row is not lost |
| Outbox backlog grows; `outbox relay pass failed` every interval | broker unreachable or rejecting the head row | `SELECT id, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT 5` | fix Kafka; the relay resumes by itself. A row that keeps failing blocks later rows only until it reaches `OUTBOX_MAX_ATTEMPTS` (default 20); then it is excluded and logged at ERROR |
| `outbox relay: row reached the attempt cap ...` ERROR; one event never reaches Kafka | a poison row (too large, rejected by the broker) failed `OUTBOX_MAX_ATTEMPTS` times and is excluded; later rows, same key included, overtook it ([ADR 0007](/docs/adr/0007-outbox-relay-attempt-cap)) | `SELECT id, topic, event_type, key, attempts, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20` | fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>` (runbook, "Re-arm a row") |
| Downstream sees the same event twice | at-least-once relay (crash between send and `published_at` update) | identical CloudEvents `id` | expected; consumers dedupe on `id` |
| Every event appears on two topics | intended: `FanoutEncoder` writes an integration row and an analytics row with the same `id` | `outbox_events.topic` | none |
| Local-copy consumer lag grows, same `partition`/`offset` in `handling failed; retrying the same message` | a transient error (usually Postgres) — the loop never skips | ERROR lines | fix the dependency; the message is retried with 200 ms → 5 s backoff |
| A product/door event seems ignored | not a CloudEvent, malformed payload or invalid value: logged at WARN and committed past (no DLQ for these consumers) | `skipping ...` WARN lines | fix the producer; replay by resetting the group offset |
| A consumer is silent although its topic now has events | it started before its topic existed and kafka-go does not pick the topic up later | no `product registered processed` lines; the topic exists | restart the pod (create or seed the topics before the first start). Known limit, still open (ADR 0011): `/readyz` pings the broker topic-less and the worker does not fail on a missing consumed topic, so nothing alerts on it |

## Analytics

| Symptom | Cause | Check | Fix |
|---|---|---|---|
| `GET /reports/freshness` returns `null`s | the projector has applied nothing yet (or `cmd/api` has published nothing) | `analytics_processed_events` row count; `outbox_events` | wait for events or fix the relay |
| `lag_seconds` keeps growing | projector down, blocked on a transient error, or no new events | projector logs (`analytics consumer handling failed; ...`), consumer-group lag | fix the analytical database / Kafka. A quiet warehouse also grows the lag |
| DLQ `warehouse.inbound-receiving.analytics.dlq` grows | deterministic failures: undecodable payload of a known type, `subject` not matching the aggregate id, invalid event, or SQLSTATE class 22/23 from the store | `x-dlq-error` header; `analytics: dead-lettering ...` ERROR | fix the cause, re-produce the message (see [Runbook](./runbook.md#inspect-and-replay-the-analytics-dlq)) |
| Projector pod restarts with `analytics consumer stopped unexpectedly` | the Kafka reader failed outside the retry loop | previous container logs | usually broker/network; restart is the intended recovery |
| `400 invalid-report-range` | `from`/`to` not RFC 3339, `from >= to`, or range > 366 days | query string | fix the range |
| `500 report-store-error` | analytical query failed or hit the 15 s `statement_timeout` | reports log `report query failed` | check the analytical database; narrow the range |
| Reports reachable through Kong return 404 | Kong routes only to the api Service; reports are served by `cmd/inbound-reports` | Service `<release>-reports` | call the reports Service in-cluster |

## Housekeeping

| Symptom | Likely cause | Check | Fix |
|---|---|---|---|
| `housekeeping sweep failed` / `analytics housekeeping sweep failed` every interval | the database rejects the DELETE (permissions, lock timeout, outage) | the `error` field; `SELECT count(*) FROM idempotency_keys WHERE created_at < now() - interval '24 hours'` | fix the database; the next tick retries. Serving is not affected |
| A client retry after a day runs the handler again instead of replaying | the key passed `IDEMPOTENCY_KEY_TTL` (default `24h`) and was swept | `idempotency_keys` for that key (gone) | intended; raise `config.idempotencyKeyTTL` if clients retry later |
| `outbox_events` still grows | unpublished rows are never swept (relay backlog or rows excluded at the attempt cap) | `SELECT count(*) FROM outbox_events WHERE published_at IS NULL` | fix the relay (see above); re-arm or delete excluded rows |

## Not present in this service

- **Circuit breakers:** none. There are no synchronous calls to other
  contexts; outbound dependencies are Postgres and Kafka only.
- **Unbounded tables:** none since gap fix D24. `idempotency_keys`,
  published `outbox_events`, `processed_events` and
  `analytics_processed_events` are swept (see
  [Runbook](./runbook.md#housekeeping), [ADR 0010](/docs/adr/0010-housekeeping-sweeper)).
