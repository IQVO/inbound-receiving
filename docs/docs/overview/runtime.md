---
id: runtime
title: Runtime and integration mechanics
sidebar_position: 4
---

# Runtime and integration mechanics

## Layers

Hexagonal architecture, enforced by the fitness tests in `internal/architecture`:

| Layer | Path |
| --- | --- |
| Domain | `internal/domain/{asn,appointment,receipt,shared}` |
| Application (use cases, ports) | `internal/application/{usecases,ports,repository,outbox,idempotency}` |
| Inbound adapters | `internal/adapters/inbound/http` (REST), `internal/adapters/inbound/kafka` (the product and dock-door consumers) |
| Outbound adapters | `internal/adapters/outbound/{postgres,memory,kafka,outbox,clock,idgen,telemetry}` |
| CloudEvents helper | `internal/adapters/kafka/cloudevents` |
| Composition root | `cmd/api/main.go` (the only binary on `develop`) |
| Boot helper | `internal/bootretry` |

The domain imports nothing internal but itself; adapters never import each
other; only `cmd/api` wires them.

## Writing: one unit of work per command

Every write use case (`internal/application/usecases/writer.go`) runs inside one
`ports.UnitOfWork`: load the aggregate(s), apply the aggregate command, save each
guarded by the version that was loaded, encode the raised events
(`EventEncoder`) and insert them into `outbox_events`. With Postgres the unit of
work is one transaction (`postgres/unit_of_work.go`); without `DATABASE_URL` the
in-memory adapters provide the same contract. `CloseReceipt` saves the receipt,
the ASN and the appointment in a single unit of work, so their events reach the
outbox together.

## Publishing: transactional outbox

- The encoder (`internal/adapters/outbound/kafka/encoder.go`) mints the
  CloudEvents `id` once, at encode time, and the row stores it, so a relay retry
  republishes the same id. Topic `warehouse.inbound-receiving.events`.
- The relay (`internal/adapters/outbound/outbox/relay.go`) drains every
  `OUTBOX_RELAY_INTERVAL` (default 1 s); a full batch (100 rows) is followed
  immediately by another pass. `OutboxRepo.Drain` claims rows with
  `FOR UPDATE SKIP LOCKED`, sends them one at a time in id order, marks each
  published, and stops at the first failure (recording `attempts` and
  `last_error`). Delivery is at-least-once.
- `EVENT_PUBLISHER=kafka` uses `RelaySink` (kafka-go writer, `RequireAll` acks,
  10 ms batch timeout, the `Hash` balancer on the message key); the default
  `log` sink only logs each message so the outbox still drains without a broker.

## Consuming: two local-copy consumers

`cmd/api` starts a consumer only for a copy whose mode is `kafka`
(`startConsumers`). Each decodes only through `internal/adapters/kafka/cloudevents`,
dispatches on the full `type`, runs the effect and the `processed_events` claim
in one unit of work and commits the offset only after that
(`FetchMessage` then `CommitMessages`, `internal/adapters/inbound/kafka/kafka.go`).
A transient failure retries the same message with backoff (200 ms doubling to
5 s) and never commits past it; a message that is not a valid CloudEvent, a
malformed payload or an invalid value is logged at WARN and committed past.

## Configuration (`cmd/api`)

| Variable | Meaning | Default |
| --- | --- | --- |
| `HTTP_ADDR` | listen address | `:8080` |
| `DATABASE_URL` | Postgres DSN; unset = in-memory adapters | unset |
| `MIGRATIONS_DATABASE_URL` | direct DSN for the boot migrations | `DATABASE_URL` |
| `EVENT_PUBLISHER` | `kafka` or `log` | `log` |
| `KAFKA_BROKERS` | comma-separated brokers; required by `EVENT_PUBLISHER=kafka` and by either mode below being `kafka` | unset |
| `OUTBOX_RELAY_INTERVAL` | relay poll interval (Go duration or seconds) | `1s` |
| `PRODUCT_MODE` | `permissive` or `kafka`: enforce known SKUs | `permissive` |
| `PRODUCT_CONSUMER_GROUP` | consumer group of the product consumer; **required** when `PRODUCT_MODE=kafka` | unset |
| `DOCK_DOOR_MODE` | `permissive` or `kafka`: enforce known inbound doors | `permissive` |
| `DOCK_DOOR_CONSUMER_GROUP` | consumer group of the dock-door consumer; **required** when `DOCK_DOOR_MODE=kafka` | unset |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC collector | `localhost:4317` |
| `SHUTDOWN_DRAIN_DELAY` | wait after flipping `/readyz` before closing | `5s` |
| `LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT`, `CORS_ALLOWED_ORIGINS` | logging and CORS | see `main.go` |

A `kafka` mode with an unset group, or without brokers, is a boot error
(`loadConfig`), not a silent no-op. With `permissive` the consumer is not
started and its group id is ignored. The first start under a new group replays
the topic from the first offset; readiness does not wait for the replay, which
is why the cluster flips to `kafka` only afterwards.

## Boot and shutdown

Migrations (golang-migrate, embedded in `postgres/migrations`) and the first
Postgres ping retry with backoff (`internal/bootretry`); on exhaustion the
process refuses to boot. Shutdown order: `/readyz` flips to 503, the drain delay
elapses, HTTP drains, the consumers (outbox writers) stop, the relay stops last
so nothing committed is stranded, then the pool closes.

## Observability

OpenTelemetry traces and metrics (`internal/adapters/outbound/telemetry`),
`otelchi` spans labelled by route pattern, request-duration metrics, and a
Prometheus handler on `GET /metrics`. `GET /healthz` is liveness and
`GET /readyz` readiness.
