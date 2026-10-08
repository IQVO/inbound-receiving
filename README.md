# inbound-receiving

Inbound Receiving is the WMS-tier bounded context of the warehouse-systems
fleet that owns the inbound dock workflow: the supplier's advance ship notice
(ASN), the carrier's dock appointment and the counted receipt, with the
short / over / damaged discrepancies found when a receipt closes. Hexagonal Go,
Postgres, REST, Kafka (CloudEvents 1.0).

This repository currently holds the contracts and the finished domain model;
the service, packaging, MCP, analytics and web layers follow in later phases.

- Why it exists and how it relates to the other contexts:
  [ADR 0001](docs/adr/0001-inbound-receiving-bounded-context.md)
- Aggregates and invariants:
  [ADR 0002](docs/adr/0002-aggregates-and-invariants.md)
- Local copies, consumed contracts and the handover to inventory-storage:
  [ADR 0003](docs/adr/0003-local-copies-and-handover.md)
- Event catalogue: [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md)
- Contracts: [`apis/openapi.yaml`](apis/openapi.yaml) (REST),
  [`apis/asyncapi.yaml`](apis/asyncapi.yaml) (events on
  `warehouse.inbound-receiving.events` and the consumed channels)
- Agent guides: `.claude/rules/` ; harness: [`HARNESS.md`](HARNESS.md)

## Domain (`internal/domain`)

| Package | Aggregate | Highlights |
|---|---|---|
| `asn` | `Asn` | lines numbered `1..n`, unique SKUs; `Registered -> Receiving -> Closed` or `Cancelled` |
| `appointment` | `DockAppointment` | window <= 4 h, no overlap per door (`Schedule`), check-in from 30 min before the window |
| `receipt` | `Receipt` | Good / Damaged counts per line, over-receipt allowed, discrepancies on close |
| `shared` | value types | SKU, quantity, reason |

## Endpoints (summary)

| Method | Path | Use case |
|---|---|---|
| POST, GET | `/asns` | RegisterAsn, ListAsns |
| GET | `/asns/{asnNumber}` | GetAsn |
| POST | `/asns/{asnNumber}/cancel` | CancelAsn |
| POST, GET | `/appointments` | BookAppointment, ListAppointments |
| GET | `/appointments/{appointmentId}` | GetAppointment |
| POST | `/appointments/{appointmentId}/check-in`, `/cancel` | CheckInAppointment, CancelAppointment |
| POST, GET | `/receipts` | OpenReceipt, ListReceipts |
| GET | `/receipts/{receiptId}` | GetReceipt |
| POST | `/receipts/{receiptId}/lines` | ReceiveLine |
| POST | `/receipts/{receiptId}/close` | CloseReceipt |
| GET | `/docks` | ListDocks |

## Quality gates

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage (>= 90 % over domain+application) + arch-test + bdd
make mutation     # gremlins on every package under internal/domain
```

Scaffolded from [warehouse-harness-template](https://github.com/IQVO/warehouse-harness-template).
Study project: not production software.
