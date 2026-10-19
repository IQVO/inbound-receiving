# ADR 0011: Postgres and Kafka are the only runtime

## Status

Accepted (2026-10-19). Fleet decision of 2026-10-19: "everything must use real
database and broker". Supersedes the in-memory, `log` and `permissive`
statements of [ADR 0001](./0001-inbound-receiving-bounded-context.md),
[ADR 0003](./0003-local-copies-and-handover.md) and
[ADR 0005](./0005-mcp-server-adoption.md) (each carries a one-line pointer
here). Sixth service of the fleet migration, after product-master, workforce-management,
facility-layout, labor-performance and process-path-management.

## Context

Until now the shipped binaries had silent fallbacks, each a way to boot
"healthy" and lose data:

- `cmd/api` and `cmd/mcp` without `DATABASE_URL` ran on in-memory adapters
  (`internal/adapters/outbound/memory`): state lost on restart, `cmd/mcp` and
  `cmd/api` each with their own empty store.
- `EVENT_PUBLISHER` defaulted to `log`: the outbox relay marked rows published
  after writing them to the log. Nothing reached Kafka.
- `KAFKA_BROKERS` was optional.
- `PRODUCT_MODE` and `DOCK_DOOR_MODE` defaulted to `permissive`: neither
  consumer started and every SKU and door code was accepted.
- `/readyz` reported only the shutdown drain gate, so a pod with a dead
  Postgres or Kafka stayed in the Service.

The chart rendered those defaults (`eventPublisher: "log"`, both modes
`permissive`, `kafka.enabled: false`). warehouse-infra already overrides all of
them (`eventPublisher: kafka`, both modes `kafka` with stable groups,
`kafka.enabled: true`, the platform brokers), so the cluster has been running
the real wiring; the repository defaults were the lie.

## Decision

1. **Mandatory configuration, boot error otherwise**, raised by `loadConfig`
   before anything is opened or the listener binds, naming the variable:
   - `cmd/api`: `DATABASE_URL`, `KAFKA_BROKERS`, `PRODUCT_CONSUMER_GROUP`,
     `DOCK_DOOR_CONSUMER_GROUP`.
   - `cmd/mcp`: `DATABASE_URL` (it reads the same Postgres; it needs no Kafka).
   - `cmd/inbound-projector`: `ANALYTICS_DATABASE_URL`, `KAFKA_BROKERS`
     (unchanged). Its `ANALYTICS_CONSUMER_GROUP` keeps its fixed default
     `inbound-receiving-analytics`: it is a name, not a mode switch.
   - `cmd/inbound-reports`: `ANALYTICS_DATABASE_URL` (unchanged; no Kafka).
2. **The `*_MODE` names stay** (docs and warehouse-infra set them) **but the
   only mode is `kafka`**, and it is the default. `PRODUCT_MODE` and
   `DOCK_DOOR_MODE` set to `permissive` or `off` are boot errors
   (`PRODUCT_MODE=permissive is not supported: this context runs on real
   Kafka`); an unknown value stays a boot error. Both consumers always start,
   and the SKU and door copies are always enforced on `POST /asns` and
   `POST /appointments`. `GET /docks` and the MCP `list_docks` always report
   `mode: kafka`. The use cases keep their `Mode` parameter: `permissive` is
   still a valid value for unit and BDD tests, and nothing under `cmd/**` can
   select it.
3. **`EVENT_PUBLISHER` is kept** because the chart and warehouse-infra set it.
   Unset or `kafka` is accepted; `log` is a boot error
   (`EVENT_PUBLISHER=log is not supported: this context publishes to real
   Kafka`). The relay is always on; `OUTBOX_MAX_ATTEMPTS` keeps its default
   of 20 ([ADR 0007](./0007-outbox-relay-attempt-cap.md)). The log sink is no
   longer wired by any binary.
4. **No in-memory runtime path in `cmd/**`.** The memory adapters stay in
   `internal/adapters/outbound/memory` as test doubles (unit, handler and BDD
   tests). `TestRuntimeBinaryHasNoInMemoryAdapters` fails the build if a
   non-test file under `cmd/**` imports them, and `TestRuntimeBinarySensorBites`
   proves the detector flags a plain, aliased, blank and dot import and passes
   look-alikes and comments. The clock and id generator the binary needs
   already live in `adapters/outbound/clock` and `idgen`, so nothing moved.
5. **Readiness is real.** `GET /readyz` is 200 only while the drain gate is
   open AND Postgres answers a ping AND a Kafka broker answers a topic-less
   metadata request. A failure is a 503 `application/problem+json`
   (`dependency-unavailable`, detail `not ready: postgres, kafka` naming the
   failing dependencies); draining stays `{"status":"not_ready"}`. The
   verdict is cached 3 s and a round is bounded to 2 s, so probes cannot
   hammer either dependency; the check ignores the caller's cancellation. The
   Kafka check dials a fresh connection per call (kafka-go's `Client.Metadata`
   would keep answering from a cached cluster layout after the broker is
   gone) and **never requires topics**: the relay writer sets
   `AllowAutoTopicCreation` and the broker auto-creates the output topics on
   first write, so a not-ready pod, which receives no traffic, could never be
   driven to create them. `/healthz` stays liveness only. The projector's
   `/readyz` (admin port) follows the same rule with the analytical database
   in place of the OLTP one. `cmd/inbound-reports` and `cmd/mcp` serve only
   `/healthz` and are unchanged.
6. **NUL guard.** A `rejectNUL` middleware answers `404 resource-not-found` for
   a NUL (U+0000) in the request path and `400 malformed-request` for one in a
   body (a raw byte or the `\u0000` escape), before routing and before the
   Idempotency-Key middleware, so a rejected request never records its key.
   PostgreSQL text cannot hold NUL (SQLSTATE 22021, then 25P02) and on sibling
   services a fuzzed NUL became a 500 on the real runtime. Here the domain
   value objects already reject control characters in every field, and a probe
   of every path, query and body field on the real runtime found no 500, so
   this is defence in depth: one uniform answer, and a field added later
   without validation cannot reintroduce the defect.
7. **Migrations at boot stay** (with `internal/bootretry` backoff;
   `MIGRATIONS_DATABASE_URL` fallback unchanged). The service never creates
   topics; on boot it logs the topics it consumes and produces. A topic that
   does not exist yet is created by the broker on the first write; the first
   relay publish may fail once with `Unknown Topic Or Partition` while that
   happens and succeeds on the next pass (the relay retries, the row is not
   lost).
8. **The proof.** `cmd/api` `TestRunOnRealPostgresAndKafka` (integration tag)
   boots the real `run(ctx)` in-process against testcontainers Postgres and
   Kafka and drives it only through REST and Kafka: it feeds both local copies
   from Kafka (a duplicate delivery is deduped by CloudEvents id), asserts the
   SKU copy is enforced (422 before the ProductRegistered event, 201 after),
   walks ASN, appointment and receipt, asserts the rows in Postgres, the
   CloudEvents envelope and key on both output topics, an idempotent replay,
   the NUL guard, a restart with no loss and no duplicate, and `/readyz`
   following Postgres and then Kafka going away. `cmd/mcp` keeps its
   Postgres integration test.
9. **Chart.** `database.url|existingSecret`, `kafka.brokers` and both consumer
   groups are required (the render fails naming the value); `productMode`,
   `dockDoorMode` and `eventPublisher` default to `kafka` and refuse anything
   else; `extraEnv` may not set `EVENT_PUBLISHER` other than `kafka` nor any
   `*_MODE`; `KAFKA_BROKERS` and the groups are always rendered. `kafka.enabled`
   is kept only because warehouse-infra sets it: `true` or absent, never
   `false`. The chart tests render warehouse-infra's current values
   (`tests/warehouse-infra-platform-values.yaml` + `tests/warehouse-infra-values.yaml`)
   and prove they still render with every required variable present: no infra
   values change is needed.
10. **Local run.** `docker-compose.yml` brings up Postgres 16 and Kafka
    (`confluent-local`, KRaft); the README and the quickstart run the real
    binaries against them.

11. **A dead worker is a dead service.** Observed on a real run: when the
    broker goes away, kafka-go's reader returns a dial error, both consumer
    goroutines log `... stopped` and return, and the process stayed up and
    `/readyz` went green again once Kafka was back, with no consumer running.
    `cmd/api` now watches the relay and both consumers and exits non-zero
    (`<worker> stopped unexpectedly: exiting so the orchestrator restarts the
    service`) when one returns on its own, exactly as `cmd/inbound-projector`
    already did. The integration test stops Kafka and asserts the exit.

## Consequences

- A misconfigured deployment fails at boot with the variable named, instead of
  coming up "healthy" with no relay, no consumers and no state.
- Local development needs Docker (`docker compose up -d --wait`); the
  1-command in-memory `go run` is gone. Unit and BDD tests are unaffected
  (they use the test doubles).
- A fresh deployment enforces the SKU and door copies from the first request:
  until the consumers have replayed `warehouse.product-master.events` and
  `warehouse.facility.events`, a write returns `422 unknown-sku` /
  `unknown-dock-door` (`/readyz` does not wait for the consumers, see the
  runbook). That is the behaviour the cluster already had.
- `GET /docks` and `list_docks` can no longer report `permissive`.
- A Kafka outage that outlasts a consumer's dial error restarts the pods
  (decision 11) instead of leaving them alive with dead consumers; with the
  broker down they are not ready anyway.
- A consumer that starts while its topic does not exist yet (a fresh cluster
  before the first product-master or facility-layout event) does not pick the
  topic up once it appears: verified on a real broker, it stayed silent for
  more than two minutes and consumed at once after a restart. `/readyz` does
  not wait for it (topics are never required). Production topics exist; on a
  fresh environment publish the first events (or create the topics) before
  starting the service, or restart it afterwards (quickstart, runbook).
  Failing readiness or the worker on a missing consumed topic is a follow-up.
- A Kafka outage makes every pod not ready (the verdict is cached 3 s), so
  the Service stops routing to it. Rows already committed wait unpublished in
  `outbox_events` and the relay drains them on recovery.
- The `contract` job: this repository has no Schemathesis job in
  `.github/workflows/ci.yml` or the Makefile, so there is nothing to move onto
  service containers. Adding one is a separate decision.

## Out of scope

- `*_MODE=permissive|http` for REST edges to sibling contexts: none exists in
  this service (it consumes siblings only through Kafka and calls none over
  REST), so there is nothing to follow up here.
- A Schemathesis `contract` job (see above) and the Postgres-backed
  Idempotency-Key hook it would need; the idempotency store is already the
  Postgres one in the shipped binary.
- `analyticsstore.Memory`, an in-memory store used by the analytics contract
  test, lives in the production package; no binary imports it. Moving it under
  a test-only package is a follow-up.
- warehouse-infra: no change is required.
