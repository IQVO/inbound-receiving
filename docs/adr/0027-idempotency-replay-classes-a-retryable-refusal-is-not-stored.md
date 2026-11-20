# ADR 0027: Idempotency replay classes — a retryable refusal is not stored

## Status

Accepted (2026-11-19). Implements the fleet decision of 2026-11-18
(warehouse-harness-template `docs/idempotency-replay-classes.md`). Supersedes
the sentence "every stored answer, 4xx included, is replayed" of
[ADR 0017](./0017-fleet-rest-api-guideline-cutover.md) decision 2 for the
refusals classed `RETRYABLE` below; the rest of ADR 0017 stands. Does not edit
other ADR bodies.

## Context

`RequireIdempotencyKey` (`internal/adapters/inbound/http/idempotency.go`) hands
the request to a store behind `ports.IdempotencyStore`. The store stored every
answer below 500, so a refusal that depends on state that can change was
pinned under its key for the retention window. The case that triggered the
rule: network-fulfillment registers ASNs with the deterministic key
`nf-asn-<asnNumber>` and retries a `422 unknown-sku` (the product copy has not
caught up); the retry was answered from the store, so the key could never
succeed.

Phase-0 facts, read from the code:

- **Postgres store** (`outbound/postgres/idempotency_store.go`): `Do` begins a
  transaction, inserts the key row, runs the handler behind a SAVEPOINT with
  the transaction in the context and, for a `Transient` response, returns
  without writing anything: the deferred rollback drops the key row and every
  write of the handler. Otherwise it releases the savepoint (healing it when a
  statement failed), writes status, body and headers with one UPDATE and
  commits. Replay reads the row without an age check; the sweeper deletes rows
  after `IDEMPOTENCY_KEY_TTL` (default 24 h; it deletes only
  `idempotency_keys`, `outbox_events` and `processed_events`,
  `sweeper.go:171-186`).
- **Memory store** (`outbound/memory/idempotency_store.go`): the same contract
  (`Transient` stores nothing), one mutex over lookup and handler.
- **The hook exists**: `idempotency.Response.Transient`, set by the middleware
  from `status >= 500` only.
- **A stored 4xx commits nothing from the use case that refused.** Every write
  use case is ONE `UoW.Do` (`asn.go:57`, `:142`; `receipt.go:45`, `:172`,
  `:241`; `scan.go:137`; `assign_sscc.go:68`; `policy.go:42`). Inside the
  middleware's transaction `UnitOfWork.Do` joins it in a SAVEPOINT and a
  returned error rolls that savepoint back (`unit_of_work.go`, `runIn`): only
  the key row and the stored answer commit. A scan that is refused with a
  finding is not an error: it is a 200 that commits `CartonDiscrepancyRaised`
  (`handlers_receipt.go:134-165`).
- **The fingerprint is method, path and body** (`idempotency.go`
  `fingerprint`); headers, `If-Match` included, are not part of it.

## Decision

1. **Every problem type has exactly one replay class**, a field on its
   catalogue entry (`problem.class`, `errors.go`; the constructors `bad`,
   `notFound`, `conflict`, `unprocessable` take it as an argument):
   - `FINAL`: the answer is a function of the request alone (or of a fact that
     never changes back). Stored and replayed.
   - `OUTCOME`: the command committed state and then answered an error that
     must be remembered. Stored, and the writes commit with the stored answer.
   - `RETRYABLE`: nothing committed and the answer depends on state that can
     change. Not stored: the key is released and a retry executes again.
2. **The middleware reads the class and holds no list of slugs.** The error
   writer (`writeProblem`) marks the request's context with the class
   (`replay_class.go`); `serveIdempotent` sets `Response.Transient` from it:
   a 2xx is stored, a 5xx never is, every other answer is stored only when its
   class is `FINAL` or `OUTCOME`. A refusal that declared no class (written
   outside the catalogue) is released like `RETRYABLE`. The stores are
   unchanged: `Transient` already rolls everything back and releases the key.
3. **A problem type without a class fails a unit test**
   (`TestEveryProblemTypeDeclaresAReplayClass`), and an AST test fails a
   problem declared anywhere that the catalogue accessor does not list.
   `TestReplayClassTableMatchesTheCatalogue` holds the table below equal to
   the catalogue.
4. **Releasing a key makes a different body under it a new request**, not
   `idempotency-key-reused`: nothing is left to compare against.
5. **A concurrent duplicate while the first ends `RETRYABLE`**: in Postgres the
   second request blocks on the key's primary-key index until the first
   transaction ends (`idempotency_store.go`, type comment "Concurrency"); the
   first rolled back, so the second's insert succeeds and it EXECUTES as a
   fresh request (not an error, not a replay). It sees a replay only when the
   first ends `FINAL`, `OUTCOME` or 2xx. In memory the mutex serialises them
   the same way.

### OUTCOME: none in this service

An `OUTCOME` needs a refusal that commits state. None does: every refusal is a
returned error that rolls back its savepoint (above) and the only
"refuse-and-record" path, a scan finding, is a 2xx. So no problem type is
`OUTCOME`; the class exists in the vocabulary and the mechanism (a stored 4xx
keeps the handler's writes, in both stores) is proven with a synthetic problem
that is not in the catalogue (`SyntheticOutcomeEntry`). A future refusal that
commits state must be declared `OUTCOME` in the PR that adds it.

## Class table

The table is held equal to the catalogue by test. `invalid-sscc` is two
entries (422 at registration, 400 for a scanned one). Rows `FINAL` with the
reason "request fields" validate the request's own body and path before any
state is read.

| Problem type | Status | Class | Reason |
| --- | --- | --- | --- |
| `malformed-request` | 400 | RETRYABLE | JUDGEMENT 1. Mixed slug: invalid JSON is body-only, but an unparsable `If-Match` (`handlers.go:30-39`) answers the same slug and the header is not in the fingerprint. |
| `invalid-limit` | 400 | FINAL | Request fields (GET only). |
| `invalid-cursor` | 400 | FINAL | Request fields (GET only). |
| `unknown-query-parameter` | 400 | FINAL | Request fields (GET only). |
| `invalid-query` | 400 | FINAL | Request fields (GET only). |
| `idempotency-key-required` | 400 | FINAL | Request header, answered before the store. |
| `idempotency-key-invalid` | 400 | FINAL | Request header, answered before the store. |
| `idempotency-key-reused` | 409 | RETRYABLE | Answered by the middleware before the store, never stored; depends on a key row that a RETRYABLE answer releases and the sweeper deletes. |
| `invalid-asn-number` | 400 | FINAL | Request fields. |
| `invalid-supplier-ref` | 400 | FINAL | Request fields. |
| `asn-requires-lines` | 400 | FINAL | Request fields. |
| `invalid-line-no` | 400 | FINAL | Request fields. |
| `duplicate-sku` | 400 | FINAL | Request fields. |
| `invalid-sku` | 400 | FINAL | Request fields. |
| `invalid-quantity` | 400 | FINAL | Request fields. |
| `invalid-reason` | 400 | FINAL | Request fields. |
| `invalid-appointment-id` | 400 | FINAL | Request fields. |
| `invalid-receipt-id` | 400 | FINAL | Request fields. |
| `invalid-door-code` | 400 | FINAL | Request fields. |
| `invalid-override-reason` | 400 | FINAL | A closed list compiled into the service. |
| `invalid-tolerance-band` | 400 | FINAL | Request fields. |
| `invalid-carrier` | 400 | FINAL | Request fields. |
| `invalid-window` | 400 | FINAL | Request fields. |
| `appointment-requires-asns` | 400 | FINAL | Request fields. |
| `duplicate-asn-number` | 400 | FINAL | Request fields. |
| `invalid-condition` | 400 | FINAL | Request fields. |
| `invalid-owner-ref` | 400 | FINAL | Request fields. |
| `invalid-carton-kind` | 400 | FINAL | Request fields. |
| `invalid-carton` | 400 | FINAL | Request fields. |
| `invalid-sscc` | 422 | FINAL | Request fields (check digit). |
| `invalid-scan-kind` | 400 | FINAL | Request fields. |
| `invalid-scan` | 400 | FINAL | Request fields (the clock only picks the century of a two-digit year, `scan_parse.go:21`). |
| `invalid-cases` | 400 | FINAL | Request fields. |
| `unsupported-gtin` | 400 | FINAL | Request fields. |
| `invalid-gtin` | 400 | FINAL | Request fields. |
| `invalid-sscc` | 400 | FINAL | Request fields (a scanned SSCC). |
| `asn-not-found` | 404 | RETRYABLE | `asn.go:143-146`: `CancelAsn` loads a number the caller chose; another request (`POST /asns`, network-fulfillment `nf-asn-<n>`) delivers it. |
| `appointment-not-found` | 404 | RETRYABLE | Not on a keyed route (`GET /appointments/{id}`); the copy is fed by dock-scheduling events (ADR 0019). |
| `receipt-not-found` | 404 | FINAL | JUDGEMENT 2. The id is minted here (`receipt.go:69`) and no receipt is deleted, so an unknown id never becomes known. |
| `receiving-policy-not-found` | 404 | RETRYABLE | Not on a keyed route (`GET /receiving-policies/{supplierRef}`; the use cases swallow it, `policy.go:50-51`, `:98`); a policy is declared by another request. |
| `carton-not-found` | 404 | RETRYABLE | Not on a keyed route (`queries_carton.go:117`); the carton ledger grows with scans. |
| `asn-already-exists` | 409 | FINAL | JUDGEMENT 3. `asn.go:61-64`, `:69-71`: a registered number stays taken (no ASN is deleted). network-fulfillment treats this 409 as success. |
| `asn-in-progress` | 409 | RETRYABLE | `asn/asn.go:355-357`: the ASN leaves Receiving when its receipt closes. |
| `asn-terminal` | 409 | FINAL | JUDGEMENT 4. `asn/asn.go:359`, `:378`: Closed and Cancelled have no exit transition. |
| `appointment-cancelled` | 409 | FINAL | JUDGEMENT 5. `receipt.go:121-123`; a cancelled copy stays cancelled (`consumers_appointment_follow.go:49-50`). |
| `asn-not-receivable` | 409 | RETRYABLE | Mixed slug: `receipt/receipt.go:230-231` (open on a snapshot that is not receivable) and `asn/asn.go:388-389` (complete an ASN not yet Receiving): a terminal state and a state not reached yet. |
| `receipt-already-open` | 409 | RETRYABLE | `receipt.go:127-131`: the open receipt of the ASN closes later. |
| `receipt-closed` | 409 | FINAL | JUDGEMENT 6. `receipt/receipt.go:314`, `tolerance.go:171`, `assign_sscc.go:78-80`: a Closed receipt is never reopened. |
| `tolerance-override-required` | 409 | RETRYABLE | `tolerance.go:159-161`: depends on the received quantities (`receipt.go:172-190`) and on the policy (`policy.go:62`); the unit of work is rolled back. |
| `tolerance-blocked` | 409 | RETRYABLE | `tolerance.go:155-159`: the receipt STAYS OPEN, so more receiving or a changed policy lets the same close pass. Not a terminal state. |
| `sscc-prefix-not-configured` | 409 | RETRYABLE | `assign_sscc.go:64-66`: deployment configuration (`RECEIVER_GS1_COMPANY_PREFIX`). |
| `sscc-serial-exhausted` | 409 | RETRYABLE | `ssccserials.go:78`: exhausted for the current prefix; configuring another lifts it. |
| `concurrent-modification` | 409 | RETRYABLE | `repository.ErrConcurrentModification` from the version-guarded saves (`receipt_repository.go:193`, `:248`): nothing committed, the retry reads the new version. |
| `unknown-sku` | 422 | RETRYABLE | `asn.go:54-56`, `:106-118`: `known_skus` is a copy fed from product-master (ADR 0003). network-fulfillment already retries it. |
| `unknown-asn` | 422 | RETRYABLE | `receipt.go:58-61`: another request delivers the ASN. |
| `unknown-appointment` | 422 | RETRYABLE | `receipt.go:111-114`: the copy is fed by dock-scheduling events. |
| `asn-not-on-appointment` | 422 | RETRYABLE | `receipt.go:118-120`: the copy follows reschedules (ADR 0020), so what it covers changes. |
| `line-not-on-asn` | 422 | RETRYABLE | JUDGEMENT 7. Mixed slug: a line number outside the receipt's fixed snapshot is receipt-only (`receipt/receipt.go:314-317`), a SKU resolved through the packaging view is not (`assign_sscc.go:120-122`, `scan_receive.go:168-171`, `carton_cases.go:53`). |
| `duplicate-sscc` | 422 | RETRYABLE | `asn.go:58-60`, `:82-103`: the SSCC sits on another ASN that is not cancelled; cancelling it frees the SSCC. |
| `homogeneous-carton-mixed` | 422 | FINAL | Request fields (`asn/carton.go:222`). |
| `carton-contents-mismatch` | 422 | FINAL | Request fields (`asn/carton.go:192`, `:276`, `:283`). |
| `counted-contents-not-mixed` | 422 | RETRYABLE | `carton_accept.go:23-37`: reached only after state branches (duplicate carton, received elsewhere, ledger); a state change turns the same request into a 200 finding. |
| `invalid-counted-contents` | 400 | FINAL | Request fields. |
| `counted-sku-unknown` | 422 | RETRYABLE | `scan_receive.go:105-109`: the packaging view is fed by product-master. |
| `scan-kind-mismatch` | 422 | RETRYABLE | JUDGEMENT 8. Mixed: against an SSCC request-only (`scan.go:204`), against a GTIN it depends on the pack level in the packaging view (`scan_receive.go:146`, `:165`, `assign_sscc.go:117-119`). |
| `unsupported-pack-level` | 422 | RETRYABLE | `scan.go:224`: the level of a GTIN comes from the packaging view. |
| `unknown-gtin` | 422 | RETRYABLE | `assign_sscc.go:110-113`: the packaging view is fed by product-master. |
| `version-mismatch` | 412 | RETRYABLE | JUDGEMENT 9. `writer.go:81-84` compares `If-Match` with the loaded version, and the header is not in the fingerprint: a client that re-reads and retries under the same key with the fresh `If-Match` must execute, not get the stale 412 back. |
| `internal-error` | 500 | RETRYABLE | A 5xx is never stored. |
| `dependency-unavailable` | 503 | RETRYABLE | A 5xx is never stored (`/readyz`, not an idempotent route). |
| `appointment-moved` | 410 | FINAL | The route moved whatever the request (`handlers_appointment.go:28`); unkeyed. |
| `resource-not-found` | 404 | FINAL | A path that can never name a resource (`nul.go`); answered before the middleware. |

## Judgement calls

When unsure between `FINAL` and `RETRYABLE` the choice is `RETRYABLE`.

1. **`malformed-request` is RETRYABLE**, the opposite of the usual example.
   One slug covers a bad body and an unparsable `If-Match`; the fingerprint
   leaves headers out, so a stored 400 would pin the corrected header's retry.
   Follow-up: its own slug for the header (or `If-Match` in the fingerprint,
   which changes what a stored key means and needs its own decision).
2. **`receipt-not-found` is FINAL**: this service mints the id and deletes
   nothing. (ASN and appointment ids are NOT minted here: RETRYABLE.)
3. **`asn-already-exists` is FINAL**: a registered number stays taken; the one
   deterministic-key caller already treats it as success.
4. **`asn-terminal` is FINAL**: Closed and Cancelled have no exit transition.
5. **`appointment-cancelled` is FINAL**: the copy never leaves Cancelled.
6. **`receipt-closed` is FINAL**: a Closed receipt is never reopened.
   `tolerance-blocked` is NOT in this group: its receipt stays open.
7. **`line-not-on-asn` is RETRYABLE**: it mixes a receipt-only cause with a
   packaging-view one.
8. **`scan-kind-mismatch` is RETRYABLE** for the same reason. `asn-not-receivable`
   also mixes a terminal state with one not reached yet. Splitting these three
   slugs is a recorded follow-up, not part of this change.
9. **`version-mismatch` is RETRYABLE** although a stale version stays stale:
   the header is outside the fingerprint (see 1).

## Consequences

- A refusal classed RETRYABLE is no longer replayed: a client or a caller with
  a deterministic key retries it and it executes. FINAL refusals and every 2xx
  are byte-identical to before; 5xx and panics were never stored.
- `apis/openapi.yaml` says which refusals are replayed and which release the
  key (descriptions only; `docs/docs/api/` is regenerated).
- Callers. network-fulfillment (`internal/adapters/outbound/inboundreceiving/
  client.go`, read only, key `nf-asn-<asnNumber>`): `201`, `200` and `409
  asn-already-exists` are success; every `422` and `400` is a verdict except
  `unknown-sku` and the slug `concurrent-update`, which is retried. Effects:
  `unknown-sku` now works on retry (this change); `409 concurrent-modification`
  is not in its retry list (the slug it names is `concurrent-update`) and falls
  through to a generic error; a `422 duplicate-sscc` is a verdict there though
  it is now retryable here. Those are consumer-side follow-ups in that repo.
- **Retention is not touched.** Replay reads a row without an age check and the
  sweeper deletes it after the TTL, so a row can outlive 24 hours by one sweep
  interval. Recorded as an open point; the stores are unchanged.
- MCP and A2A are read-only here (`CLAUDE.md` rule 8, ADR 0016, ADR 0018), so
  no other write surface stores answers.
