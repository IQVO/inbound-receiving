---
id: overview
title: API overview
slug: /api-reference
sidebar_position: 1
---

# API overview

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 1.0.0) | Pages under *REST* are generated from the spec; do not edit them by hand. Servers: `http://localhost:8080` (local) and `http://localhost:8000/api/inbound-receiving` (kind cluster through Kong, once the service is wired into the cluster). |
| Events | `apis/asyncapi.yaml` (AsyncAPI 2.6.0, version 1.0.0) | Summarised on the [event catalogue](/docs/api-reference/events). |
| MCP | none yet | **Planned, not built** (ADR 0001, later phases). |

## Conventions

- **Resource-creating `POST`s require `Idempotency-Key`**: `/asns`,
  `/appointments`, `/receipts` and `/receipts/{receiptId}/lines`. A missing key is
  `400 idempotency-key-required`, a replay with the same key and body returns the
  original response, the same key with another body is `422
  idempotency-key-reused`. The action `POST`s (`cancel`, `check-in`, `close`)
  accept the header but do not require it: repeating them is refused by the state
  machine (`409`).
- **Versions and `ETag`.** Mutable resources carry a `version` (starts at 1, +1
  per accepted change) and a matching strong `ETag: "<version>"`. An action
  `POST` may send `If-Match`; a stale value is `412 version-mismatch` and a write
  that loses a race in the database is `409 concurrent-modification`.
- **Errors** are RFC 7807 `application/problem+json` with
  `type = https://errors.inbound-receiving.warehouse-systems.dev/<slug>`. The slugs
  come from one table (`problemCatalogue` in
  `internal/adapters/inbound/http/errors.go`) and are listed in the description of
  the API info page: `malformed-request`, `invalid-query`,
  `idempotency-key-required`, `idempotency-key-reused`, the `invalid-*` slugs for
  ASN number, supplier ref, line number, SKU, quantity, reason, appointment id,
  receipt id, door code, carrier, window and condition, `asn-requires-lines`,
  `duplicate-sku`, `window-in-past`, `appointment-requires-asns`,
  `duplicate-asn-number`, `asn-not-found`, `appointment-not-found`,
  `receipt-not-found`, `asn-already-exists`, `asn-in-progress`, `asn-terminal`,
  `asn-not-receivable`, `appointment-not-booked`, `appointment-not-checked-in`,
  `outside-check-in-window`, `door-window-overlap`, `receipt-already-open`,
  `receipt-closed`, `unknown-sku`, `unknown-dock-door`, `unknown-asn`,
  `unknown-appointment`, `asn-not-on-appointment`, `line-not-on-asn`,
  `version-mismatch`, `concurrent-modification` and `internal-error`.
- **Request bodies** reject unknown fields; JSON field names are camelCase on REST
  (`asnNumber`, `expectedQty`, `windowStart`) and snake_case on the events.
- **Paging.** Lists use `limit` (1 to 500, default 100) and the opaque `cursor`
  that is the previous page's `nextCursor`; a response without `nextCursor` is
  the last page.
- **Auth**: none. The fleet reverted REST and MCP auth on 2026-09-11, and
  `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns.
- **CORS**: `GET` and `POST` from `CORS_ALLOWED_ORIGINS`, exposing `ETag` and
  `Location`.

## Endpoint groups (tags)

| Tag | Endpoints |
| --- | --- |
| `ASNs` | `POST /asns`, `GET /asns`, `GET /asns/{asnNumber}`, `POST /asns/{asnNumber}/cancel` |
| `Appointments` | `POST /appointments`, `GET /appointments`, `GET /appointments/{appointmentId}`, `POST /appointments/{appointmentId}/check-in`, `POST /appointments/{appointmentId}/cancel` |
| `Receipts` | `POST /receipts`, `GET /receipts`, `GET /receipts/{receiptId}`, `POST /receipts/{receiptId}/lines`, `POST /receipts/{receiptId}/close` |
| `Docks` | `GET /docks` |
| `Health` | `GET /healthz`, `GET /readyz` |

`GET /metrics` (Prometheus) is served by the binary but is not part of
`apis/openapi.yaml`.
