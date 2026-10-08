---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Source of truth: `apis/openapi.yaml`. Keep this list in sync with it. Kong
path prefix: `/api/inbound-receiving`.

- `POST /asns`                              -> `RegisterAsn` (201; `Idempotency-Key` required)
- `GET  /asns?limit=&cursor=&state=`        -> `ListAsns`
- `GET  /asns/{asnNumber}`                  -> `GetAsn`
- `POST /asns/{asnNumber}/cancel`           -> `CancelAsn` (200)
- `POST /appointments`                      -> `BookAppointment` (201; `Idempotency-Key` required)
- `GET  /appointments?door=&state=&from=&to=&limit=&cursor=` -> `ListAppointments`
- `GET  /appointments/{appointmentId}`      -> `GetAppointment`
- `POST /appointments/{appointmentId}/check-in` -> `CheckInAppointment` (200)
- `POST /appointments/{appointmentId}/cancel`   -> `CancelAppointment` (200)
- `POST /receipts`                          -> `OpenReceipt` (201; body `{asnNumber, appointmentId?}`; `Idempotency-Key` required)
- `GET  /receipts?asnNumber=&state=&limit=&cursor=` -> `ListReceipts`
- `GET  /receipts/{receiptId}`              -> `GetReceipt`
- `POST /receipts/{receiptId}/lines`        -> `ReceiveLine` (201; `Idempotency-Key` required: receiving is not naturally idempotent)
- `POST /receipts/{receiptId}/close`        -> `CloseReceipt` (200)
- `GET  /docks`                             -> `ListDocks` (the `dock_doors` local copy)
- `GET  /healthz`, `GET /readyz` (503 while draining), `GET /metrics`

## Conventions

- Errors: RFC 7807 `application/problem+json`,
  `type = https://errors.inbound-receiving.warehouse-systems.dev/<slug>`. One
  lookup table maps typed errors to (status, slug, title). The slug catalogue
  is listed in `apis/openapi.yaml`'s `info.description` (`malformed-request`,
  `idempotency-key-required`, `idempotency-key-reused`, `invalid-*`,
  `asn-not-found`, `appointment-not-found`, `receipt-not-found`,
  `asn-already-exists`, `asn-in-progress`, `asn-terminal`,
  `asn-not-receivable`, `appointment-not-booked`,
  `appointment-not-checked-in`, `outside-check-in-window`,
  `door-window-overlap`, `receipt-already-open`, `receipt-closed`,
  `unknown-sku`, `unknown-dock-door`, `unknown-asn`, `unknown-appointment`,
  `asn-not-on-appointment`, `line-not-on-asn`, `version-mismatch`,
  `concurrent-modification`, `internal-error`).
- Request bodies reject unknown fields (`DisallowUnknownFields`); JSON is
  camelCase (events are snake_case).
- Every resource-creating POST requires `Idempotency-Key` (the fleet
  idempotency middleware): missing = 400 `idempotency-key-required`, same key
  with a different body = 422 `idempotency-key-reused`. The action POSTs
  (`cancel`, `check-in`, `close`) accept it but do not require it.
- Optimistic concurrency: bodies carry `version`, single-resource responses
  carry `ETag: "<version>"`; an action POST may send `If-Match` (stale = 412
  `version-mismatch`); a lost database race is 409 `concurrent-modification`.
- Lists use cursor paging (`limit` 1..500 default 100, opaque `cursor`).
- Auth: none (fleet-wide revert 2026-09-11; `TestNoAuthMiddlewareReintroduced`).
