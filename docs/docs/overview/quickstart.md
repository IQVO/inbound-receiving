---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
---

# Quickstart

Run inbound-receiving on your machine, drive a full receiving flow with
`curl`, then point it at Postgres and Kafka. Every response below was captured
from a real run of `cmd/api` built from this repository (in-memory adapters,
`EVENT_PUBLISHER=log`); ids and timestamps will differ on yours.

## Prerequisites

| Tool | Why |
|---|---|
| Go **1.27.2** (`go.mod`) | build and test |
| `golangci-lint` v2.14.0 | `make lint` / `make check` (the Makefile prints the install command if missing) |
| Docker | `make integration` only: testcontainers starts its own Postgres and Kafka |
| Node.js + npm | only for `docs/` (this site) and `web/` (the frontend remote) |
| Postgres, Kafka | optional; without `DATABASE_URL` the binaries use in-memory adapters, without `EVENT_PUBLISHER=kafka` the outbox drains to the log |

## Build and test

The Makefile has no `run` target; it is the local quality gate mirroring CI.

```bash
make help        # list targets
make build       # go build ./...
make test        # go test ./... -race (unit + httptest + godog BDD, no database)
make check       # fmt-check vet build lint test   (the lefthook pre-push gate)
make check-all   # check + coverage (90 % gate) + arch-test + bdd
make integration # go test -tags=integration ./... -race -count=1 (needs Docker)
```

See [Testing](../development/testing.md) for what each layer covers.

## Run the API in memory

```bash
go run ./cmd/api
```

Without `DATABASE_URL` it logs `DATABASE_URL not configured; using in-memory
adapters`, starts the outbox relay with the `log` sink and listens on `:8080`.
With no OpenTelemetry collector on `localhost:4317` you will also see periodic
`traces export: ... connection refused` INFO lines; they are harmless.

### First calls

```bash
curl -s localhost:8080/healthz
# {"status":"ok"}
curl -s localhost:8080/readyz
# {"status":"ready"}
```

Register an ASN. Resource-creating POSTs **require** an `Idempotency-Key`:

```bash
curl -s -X POST localhost:8080/asns -H 'Content-Type: application/json' \
  -d '{"asnNumber":"ASN-1001","supplierRef":"SUP-42","expectedArrival":"2026-10-13T09:00:00Z",
       "lines":[{"lineNo":1,"sku":"SKU-RED-01","expectedQty":10},{"lineNo":2,"sku":"SKU-BLU-02","expectedQty":5}]}'
# 400 {"type":"https://errors.inbound-receiving.warehouse-systems.dev/idempotency-key-required",
#      "title":"Idempotency-Key header is required","status":400,...}

curl -si -X POST localhost:8080/asns -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: k-asn-1001' -d '<same body>'
# 201, ETag: "1", Location: /asns/ASN-1001
# {"asnNumber":"ASN-1001","supplierRef":"SUP-42","expectedArrival":"2026-10-13T09:00:00Z",
#  "state":"Registered","lines":[...],"version":1}
```

Sending the same key and body again replays the same `201`; the same key with
a different body returns `422 idempotency-key-reused`.

Book a dock appointment (in `permissive` door mode any door code is accepted;
`GET /docks` returns `{"mode":"permissive","items":[]}`):

```bash
curl -s -X POST localhost:8080/appointments -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: k-appt-1' \
  -d '{"doorCode":"DOCK-IN-01","carrier":"ACME Freight","windowStart":"2026-10-13T09:00:00Z",
       "windowEnd":"2026-10-13T10:00:00Z","asnNumbers":["ASN-1001"]}'
# 201 {"appointmentId":"appt-aab0ea5f-...","doorCode":"DOCK-IN-01","state":"Booked","version":1,...}
```

Checking in outside the window is refused:
`POST /appointments/{appointmentId}/check-in` →
`409 outside-check-in-window` ("check-in is only allowed from 30 minutes before
the window until it ends").

Open a receipt (walk-in, no appointment), receive lines and close it:

```bash
curl -s -X POST localhost:8080/receipts -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: k-rcpt-1' -d '{"asnNumber":"ASN-1001"}'
# 201 {"receiptId":"rcpt-afafe148-...","state":"Open", ... "version":1}

curl -s -X POST localhost:8080/receipts/rcpt-afafe148-.../lines -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: k-line-1' -d '{"lineNo":1,"quantity":10,"condition":"Good"}'
# 201, ETag: "2"
curl -s -X POST localhost:8080/receipts/rcpt-afafe148-.../lines -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: k-line-2' -d '{"lineNo":2,"quantity":4,"condition":"Good"}'
# 201, ETag: "3"

curl -s -X POST localhost:8080/receipts/rcpt-afafe148-.../close -H 'If-Match: "1"'
# 412 version-mismatch ("The If-Match version is stale")
curl -s -X POST localhost:8080/receipts/rcpt-afafe148-.../close
# 200 {"state":"Closed","closedAt":"...","discrepancies":[{"lineNo":2,"sku":"SKU-BLU-02",
#      "kind":"Short","expectedQty":5,"receivedQty":4,"damagedQty":0}],"version":4}

curl -s localhost:8080/asns/ASN-1001
# {"asnNumber":"ASN-1001",...,"state":"Closed","version":3}
```

The api log shows every event drained by the relay **twice**, once per topic
and with the same id:

```json
{"level":"INFO","msg":"event published (log sink)","topic":"warehouse.inbound-receiving.events","type":"com.warehouse.wms.inbound-receiving.asn.ASNRegistered","subject":"ASN-1001","id":"c3272ac3-..."}
{"level":"INFO","msg":"event published (log sink)","topic":"warehouse.inbound-receiving.analytics","type":"com.warehouse.wms.inbound-receiving.asn.ASNRegistered","subject":"ASN-1001","id":"c3272ac3-..."}
```

`GET /metrics` returns the Prometheus Go and process collectors.

## Run the MCP server

```bash
MCP_ADDR=:8090 go run ./cmd/mcp
```

It logs `mcp server listening (Streamable HTTP)` and serves `/mcp` and `/`.
In memory it has its **own empty store**: data registered through a local
`cmd/api` is not visible unless both share one `DATABASE_URL`. See
[MCP tools](../mcp/tools.md) for the tool list and an example session.

## Run against Postgres

There is no `docker-compose.yml` in this repo. Any Postgres works; for example:

```bash
docker run -d --name ir-pg -e POSTGRES_USER=inbound_receiving -e POSTGRES_PASSWORD=dev \
  -e POSTGRES_DB=inbound_receiving -p 5432:5432 postgres:16
DATABASE_URL='postgres://inbound_receiving:dev@localhost:5432/inbound_receiving?sslmode=disable' \
  go run ./cmd/api
```

`cmd/api` (and `cmd/mcp`) apply the embedded migrations at start. There is no
seed script: create data through the REST calls above.

## Use Kafka

The fleet has **one** Kafka broker: the in-cluster Bitnami broker of the kind
cluster, reachable from the host at `localhost:9092` through its external
access listener (the old docker-compose Kafka is retired).

```bash
DATABASE_URL='postgres://...' \
EVENT_PUBLISHER=kafka KAFKA_BROKERS=localhost:9092 \
PRODUCT_MODE=kafka PRODUCT_CONSUMER_GROUP=inbound-receiving-product-local \
DOCK_DOOR_MODE=kafka DOCK_DOOR_CONSUMER_GROUP=inbound-receiving-dock-doors-local \
  go run ./cmd/api
```

Use your own group ids locally so you do not steal partitions from the
in-cluster deployment (`inbound-receiving-product`,
`inbound-receiving-dock-doors`). In the `kafka` modes `POST /asns` and
`POST /appointments` only accept SKUs and doors already seen on
`warehouse.product-master.events` and `warehouse.facility.events`.

## Run the analytics side

```bash
ANALYTICS_DATABASE_URL='postgres://.../inbound_receiving_analytics?sslmode=disable' \
KAFKA_BROKERS=localhost:9092 ANALYTICS_CONSUMER_GROUP=inbound-receiving-analytics-local \
  go run ./cmd/inbound-projector          # admin :8091 (/healthz, /readyz)

ANALYTICS_DATABASE_URL='postgres://.../inbound_receiving_analytics?sslmode=disable' \
  go run ./cmd/inbound-reports            # :8092
curl -s localhost:8092/reports/freshness
curl -s 'localhost:8092/reports/receiving-performance?from=2026-10-01T00:00:00Z&to=2026-10-12T00:00:00Z'
```

Both need a real Postgres (there is no in-memory mode), and events only reach
the analytics topic when `cmd/api` runs with `EVENT_PUBLISHER=kafka`. Details:
[Runbook — Reports API](../operations/runbook.md#reports-api-cmdinbound-reports).

## In the kind cluster

warehouse-infra deploys the chart with every component on. Through Kong the
API is at `http://localhost:8000/api/inbound-receiving/...` (for example
`curl localhost:8000/api/inbound-receiving/asns`); the frontend remote is
mounted by the console shell. See the [Runbook](../operations/runbook.md).
