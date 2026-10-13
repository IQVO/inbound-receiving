---
id: testing
title: Testing
sidebar_label: Testing
---

# Testing

The test pyramid as it exists in this repository, the `make` targets that run
each layer locally and the CI jobs (`.github/workflows/ci.yml`) that run them on
every push and pull request into `develop`/`main`.

## Layers

| Layer | Where | How it runs | Needs |
|---|---|---|---|
| Domain unit tests | `internal/domain/{asn,appointment,receipt,shared}/*_test.go` | `make test` | nothing |
| Use-case unit tests | `internal/application/usecases/*_test.go` (fakes in `fakes_test.go`) | `make test` | nothing |
| Adapter unit tests | `internal/adapters/**/*_test.go` (httptest handlers, MCP tools + `testdata/tool_registry.golden.json`, Kafka consumers with fake readers, encoders with golden CloudEvents, relay, telemetry, memory store) | `make test` | nothing |
| Analytics rules | `internal/analytics/report/report_test.go` (range parsing, day windows, percentiles, freshness) | `make test` | nothing |
| Composition roots | `cmd/*/main_test.go` (env validation, admin mux, drain delay) | `make test` | nothing |
| BDD acceptance (godog) | `features/*.feature`, steps in `features_test.go` + `features_wave2_test.go` | `make bdd` (also part of `make test`) | nothing: in-memory adapters behind an `httptest` server and a stepping clock |
| Architecture fitness | `internal/architecture/*_test.go` | `make arch-test` (also part of `make test`) | nothing |
| Integration (testcontainers) | 15 files with `//go:build integration` | `make integration` | Docker |
| Mutation | gremlins over `internal/domain` | `make mutation` / `make mutation-full` | `gremlins` v0.6.0 |
| Frontend remote | `web/` (vitest + Testing Library) | `npm test` in `web/` | Node 22, sibling `warehouse-ui-kit` checkout |
| Chart render tests | `charts/inbound-receiving/tests/render_test.sh`, `charts/inbound-receiving/tests/test_service_selectors.py` | `bash .../render_test.sh`, `python3 .../test_service_selectors.py` (CI `chart-tests`) | `helm`, PyYAML |

There are **61** `*_test.go` files; 15 of them are integration-tagged.

### BDD features

7 feature files, **71** scenarios (8 of them Scenario Outlines, expanded by
their Examples tables). godog runs with `Strict: true`, so an undefined or
pending step fails the suite.

| Feature file | Feature | Scenarios |
|---|---|---|
| `register_asn.feature` | Register an ASN | 11 |
| `dock_appointments.feature` | Book and manage dock appointments | 13 |
| `receiving.feature` | Receive goods against an ASN | 12 |
| `idempotent_replays.feature` | Idempotent creating requests | 5 |
| `list_receipts_and_docks.feature` | Listing receipts, appointments and dock doors | 17 |
| `dock_door_registry.feature` | Dock-door registry fed by facility-layout | 12 |
| `probes.feature` | Health probe | 1 |

### Integration suites

Every integration test boots its **own** Postgres and/or Kafka through
testcontainers; there is no `DATABASE_URL`, no docker compose and no `t.Skip`
gate (enforced by the fitness tests below).

| File | Covers |
|---|---|
| `internal/adapters/outbound/postgres/repositories_integration_test.go` | repositories, cursors, version guards, constraints |
| `internal/adapters/outbound/postgres/usecases_integration_test.go` | use cases over a real unit of work |
| `internal/adapters/outbound/postgres/analytics_outbox_integration_test.go` | fan-out rows (integration + analytics) in one transaction |
| `internal/adapters/outbound/postgres/helper_integration_test.go` | shared container helper |
| `internal/adapters/outbound/postgres/pgtx/pgtx_integration_test.go` | transaction propagation through `context` |
| `internal/adapters/outbound/outbox/relay_integration_test.go` | relay draining Postgres to Kafka |
| `internal/adapters/outbound/outbox/relay_cap_integration_test.go` | relay attempt cap over real Postgres: a poison row is excluded after N failures and the good row behind it (same key) publishes; the re-arm SQL; `0` = unlimited ([ADR 0007](/docs/adr/0007-outbox-relay-attempt-cap)) |
| `internal/adapters/outbound/outbox/outbox_trace_integration_test.go` | trace continuity across the outbox, real Postgres + Kafka: `TestOutboxTrace_TraceparentCrossesTheOutboxToKafka`, `TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged` ([ADR 0009](/docs/adr/0009-trace-context-crosses-the-outbox)) |
| `internal/adapters/outbound/kafka/publisher_integration_test.go` | `RelaySink` against a real broker |
| `internal/adapters/inbound/kafka/consumers_integration_test.go` | product and dock-door consumers end to end |
| `internal/adapters/inbound/kafka/analytics_consumer_integration_test.go` | projector consumer, DLQ |
| `internal/adapters/outbound/analyticsstore/postgres_integration_test.go` | analytical store (the same contract test also runs against the in-memory store in `contract_test.go`) |
| `internal/adapters/inbound/http/rest_api_integration_test.go` | REST over Postgres |
| `internal/adapters/inbound/mcp/streamable_http_integration_test.go` | MCP over Streamable HTTP |
| `cmd/mcp/main_integration_test.go` | the MCP binary's wiring |
| `internal/application/usecases/usecases_wiring_integration_test.go` | use-case wiring |
| `analytics_e2e_integration_test.go` | api → outbox → Kafka → projector → reports, end to end |

`make integration` and the `integration` CI job both run
`go test -tags=integration ./... -race -count=1`, so every tagged package is
covered.

### Architecture fitness tests

| Test | Rule |
|---|---|
| `TestHexagonalArchitecture` | arch-go rules: domain imports nothing internal but itself; layers only point inwards; adapters never import each other |
| `TestMCPAdapterDependencyRule` | the MCP adapter depends only on the application and domain layers, and nothing but `cmd/mcp` imports it |
| `TestNoAuthMiddlewareReintroduced` | no auth middleware on REST or MCP (fleet rule) |
| `TestKafkaConsumerGroupNeverHardcodedInline` | consumer group ids come from env, never literals |
| `TestReplayConsumersSetCommitInterval` | a full-replay cache consumer (a per-process unique group) must set `CommitInterval`; this repo has none, so the rule is dormant |
| `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope` | every Kafka message is a CloudEvents 1.0 envelope built by `internal/adapters/kafka/cloudevents`; no envelope toggle |
| `TestEventCatalogueMatchesContract`, `TestEventCatalogueDetector` | published event types match `apis/asyncapi.yaml` |
| `TestKafkaIntegrationTestsUseTestcontainers`, `TestPostgresIntegrationTestsUseTestcontainers`, `TestPostgresIntegrationSensorFailsOnBadFixtures` | integration tests use testcontainers, never an external DSN or a skip |
| `TestMCPOptionalToolInputsCarryOmitempty`, `TestMCPOptionalToolInputsSensorFailsOnBadFixtures` | every MCP tool input field whose `jsonschema` description calls it optional ("optional" or "omitted") carries `omitempty`, so the SDK does not mark it required (gap fix D16) |
| `TestOutboxRelaysPassAttemptCap`, `TestOutboxRelayCapSensorFailsOnBadFixtures` | every non-test file under `cmd/` that builds an outbox relay (`NewRelay`) passes `WithMaxAttempts(...)` ([ADR 0007](/docs/adr/0007-outbox-relay-attempt-cap)) |

`violation_test.go` holds deliberately failing fixtures that prove the
detectors work.

### Mutation testing

`.gremlins.yaml`: `workers: 1`, `timeout-coefficient: 30`, thresholds
**`efficacy: 99`** and **`mutant-coverage: 99`** (gremlins fails when the
measured value is ≤ the threshold). The file records the 2026-10-08
measurement: shared 12 mutants, asn 52, appointment 38, receipt 57, all 100 %
efficacy and coverage, so a single surviving mutant fails the gate.

- `make mutation`: gremlins on each of `./internal/domain/{receipt,asn,appointment,shared}`.
- `make mutation-full`: `gremlins unleash ./internal/domain --workers 1 --timeout-coefficient 30`.

### Not present

- **Contract tests against a running server (schemathesis):** none in this
  repo. The REST contract is guarded by Spectral linting (`api-lint`), the
  `docs-api-drift` job and the handler tests; the event contract by
  `TestEventCatalogueMatchesContract` and golden CloudEvents tests.
- **Evals:** `EVALS.md` and `tools/harness_eval*.py` measure the agent
  harness (run against other fleet repos), not this service's behaviour; no CI
  job runs them.

## Make targets

| Target | Command |
|---|---|
| `make test` | `go test ./... -race` |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...` + a **90 %** gate |
| `make bdd` | `go test ./... -run TestFeatures -v` |
| `make arch-test` | `go test ./internal/architecture/... -v` |
| `make integration` | `go test -tags=integration ./... -race -count=1` |
| `make mutation` / `make mutation-full` | see above |
| `make vuln` | `govulncheck ./...` |
| `make check` | `fmt-check vet build lint test` (lefthook pre-push runs it) |
| `make check-all` | `check` + `coverage arch-test bdd` |
| `make check-fast` | `fmt-check vet arch-test` + tests of changed packages (agent Stop hook) |
| `make guide-lint` / `make harness-test` | lint the agent guides / test the agent hooks |

lefthook (`lefthook.yml`) runs `fmt-check`, `vet` and `lint` on pre-commit and
`make check` on pre-push.

## CI jobs (`ci.yml`)

| Job | Runs | When |
|---|---|---|
| `lint` | golangci-lint v2.14.0 | push + PR |
| `guide-lint` | `scripts/harness/guide_lint.py`, `test_hook.py` (advisory, `continue-on-error`) | push + PR |
| `complexity` | golangci-lint with only `gocyclo,gocognit,cyclop,funlen,nestif` + an informational gocyclo report | push + PR |
| `test` | build, vet, `go test ./... -race` with coverage over domain + application, **90 %** gate | push + PR |
| `bdd` | `go test ./... -run TestFeatures -v` | push + PR |
| `integration` | `go test -tags=integration ./... -race -count=1` (testcontainers on the runner's Docker) | push + PR |
| `mutation-fast` | gremlins on each domain package (blocking) | push + PR |
| `mutation` | gremlins on `./internal/domain`; opens/closes a `harness:red` issue | schedule (Mon 06:00 UTC) + dispatch |
| `drift` | deadcode, `go mod tidy -diff`, knip on `web/`, coverage-quality (advisory) | schedule + dispatch |
| `api-lint` | Spectral on `apis/openapi.yaml` and `apis/asyncapi.yaml` (`--fail-severity=warn`) | push + PR |
| `vuln` | govulncheck | push + PR |
| `helm-lint` | `ct lint` + `charts/inbound-receiving/tests/test_service_selectors.py` | PR only |
| `chart-tests` | `bash charts/inbound-receiving/tests/render_test.sh` (api Deployment mounts the ConfigMap via `envFrom`, `OUTBOX_MAX_ATTEMPTS` default, `ENVIRONMENT` on every backend Deployment) + `test_service_selectors.py` | push + PR |
| `docs-api-drift` | `npm run clean-api-docs && npm run gen-api-docs`, fails on a diff under `docs/api-reference/rest` | push + PR |
| `web` | in `web/`: `npm run lint`, `npx tsc -b`, `npm test`, `npm run build` (after building `warehouse-ui-kit` from its `develop`) | push + PR |
| `arch-test` | `go test ./internal/architecture/... -v` | push + PR |
| `trivy-scan` | builds the image, Trivy SARIF upload + blocking gate on CRITICAL/HIGH with a fix | PR only |
| `docker-publish` | build, push to GHCR, cosign sign, SBOM attest | push to `main` |
| `release` | version tag, Helm chart package, GitHub release | push to `main` after `docker-publish` |

Other workflows: `docs.yml` builds this site on PRs touching `docs/**` or
`apis/**` and publishes to GitHub Pages only from `main`; `selftest.yml` tests
the harness scripts; `ai-review.yml` posts an advisory architecture review on
PRs into `develop` that touch domain, application, `apis/` or `charts/`.
