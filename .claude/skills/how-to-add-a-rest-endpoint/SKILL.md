---
name: how-to-add-a-rest-endpoint
description: Add or change a REST endpoint in this service in the fleet's hexagonal order (domain invariant, use case, port, HTTP adapter, apis/openapi.yaml, generated docs, godog scenario). Use when touching internal/adapters/inbound/http, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Use when asked to add a new REST use case/endpoint to this service. Follow
this order — domain first, adapter last — never the reverse; writing the
HTTP handler before the domain invariant it enforces produces handlers
that validate nothing and use cases that get bypassed.

This walks the exact path `POST /receipts/{receiptId}/lines` took
(`internal/application/usecases/receipt.go`'s `ReceiveLine` +
`internal/adapters/inbound/http/handlers.go`'s `handleReceiveLine`) as the
concrete worked example — read those two files alongside this guide.

## 1. Domain first: does an invariant already exist, or do you need one?

Check `internal/domain/<aggregate>/` for the rule this endpoint enforces.
A REST endpoint should almost never contain business logic itself — it
decodes a request, calls a use case, encodes the result. If the operation
needs a new domain rule (e.g. "a received quantity of zero is invalid"),
add it to the aggregate/value-object in `internal/domain/`, with its own
table-driven unit test, BEFORE touching the application or adapter layers.

## 2. Application: define the use case

Add a new file in `internal/application/usecases/` (one file per use
case, this repo's convention — not one giant `usecases.go`). Shape:

```go
package usecases

type <Verb><Noun>Result struct {
    // fields the caller needs back — domain types, not DTOs
}

// <Verb><Noun> — one sentence: what business capability this represents,
// and the domain rule it enforces (mirror ReceiveLine's doc comment,
// which states the Unlocated-on-shortfall rule right in the doc comment).
type <Verb><Noun> struct {
    Repo   ports.<Aggregate>Repo   // driven ports only — never a concrete adapter
    Events ports.EventPublisher    // if this raises a domain event
    Clock  ports.Clock             // if it needs "now" (never call time.Now() directly)
}

func (uc *<Verb><Noun>) Execute(ctx context.Context, /* domain-typed args */) (<Verb><Noun>Result, error) {
    // 1. load aggregate(s) via the port
    // 2. call the aggregate's own method to apply the rule (never inline
    //    the invariant here — that belongs in internal/domain/)
    // 3. persist via the port
    // 4. publish the domain event via Events, if any
    // 5. return the result
}
```

Add the port to `internal/application/ports/` if it doesn't exist yet —
ports are interfaces ONLY (`TestHexagonalArchitecture` in
`internal/architecture/architecture_test.go` enforces this; a struct,
constant or function in a ports package fails CI, which is why the typed
errors and filters live in `internal/application/repository/` and the
outbox vocabulary in `internal/application/outbox/`).

Write the use case's unit test against the in-memory adapter
(`internal/adapters/outbound/memory/`) — never a real Postgres/HTTP call
in a unit test. Cover the success path AND the domain-rule failure path.

## 3. Adapter: wire the HTTP handler

In `internal/adapters/inbound/http/`:

1. `dto.go` — add the request/response DTO structs (JSON tags, this repo's
   naming convention: `<verb><noun>Request`/`<verb><noun>Response`).
   DTOs live ONLY in the adapter layer — domain types never carry JSON
   tags.
2. `server.go` — add the route (`r.Post("/path/{param}", s.handle<Name>)`
   in the router setup) and the handler function:
   - decode + validate the request (`decodeJSON`), converting to domain
     value objects immediately (`shared.NewSKU`, `shared.NewQuantity`,
     etc.) — a bad value fails here as an RFC 7807 validation error, never
     reaches the use case
   - call the use case's `Handle`
   - map use-case errors to HTTP status via `writeError` (add the new
     typed error to the `problemCatalogue` in `errors.go`, with its slug from
     `.claude/rules/rest-api.md`, before using it)
   - encode the domain result back to the response DTO and `writeJSON`
3. Add the new use case field to the `Server`/`Deps` struct and wire it in
   the composition root (`cmd/api/main.go`'s `buildServer`).

Write at least one httptest per endpoint: one success path, one error
path (validation failure AND/OR the domain-rule failure, whichever this
endpoint can produce).

## 4. Contract: update OpenAPI, then regenerate docs

Add the path to `apis/openapi.yaml` (request/response schemas, the RFC
7807 problem-detail response for each error case — see the existing
`/receipts/{receiptId}/lines` entry for the shape).

Lint the contract (`spectral lint apis/openapi.yaml`). The docs site and its
generated REST reference are a later brief; there is nothing to regenerate yet.

## 5. Behaviour: add a godog scenario

If this endpoint is user-facing behaviour (not purely internal
plumbing), add a `.feature` file under `features/` exercising it
end-to-end against the real HTTP server — see `features/receiving.feature`
for the exact shape this repo's `bdd` CI job expects (Given/When/Then over
real HTTP, not mocked).

## 6. Verify before opening the PR

```bash
make check       # fmt-check vet build lint test
make check-all    # + coverage (90% gate) + arch-test + bdd
```

`make coverage` gates `./internal/domain/...,./internal/application/...`
at 90% — a new use case with no test on its failure path is the most
common way to miss this gate.
