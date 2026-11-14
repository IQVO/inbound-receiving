# Documentation-derived cross-cutting rules: idempotency, list queries,
# readiness and the receiving reports service.
#
# Derived from warehouse-docs contexts/inbound-receiving: use-cases (cross-cutting
# behaviour, Queries, analytics read side), troubleshooting (REST problem types,
# Boot and readiness, Analytics rows) and the repo's ADR 0017 (fleet REST
# guideline: Idempotency-Key is 1-128 printable ASCII, list slugs invalid-limit /
# invalid-cursor / unknown-query-parameter).
# The scenario clock starts at 2026-10-09T07:00:00Z.

Feature: Operational rules from the documentation
  Every POST is replay-safe, every list answers a typed problem, /readyz
  tells an orchestrator when to route traffic and the reports service refuses
  a range it cannot answer.

  @bdd
  Scenario: A retried receipt opening replays the first answer and opens nothing twice
    Given the ASN "ASN-1" is registered
    When I POST "/receipts" with key "open-1" and body '{"asnNumber":"ASN-1"}'
    And I POST "/receipts" with key "open-1" and body '{"asnNumber":"ASN-1"}'
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    When I list "/receipts" with query "limit=10"
    Then the list has 1 item
    And the outbox holds 1 "receipt.ReceiptOpened" event
    When I GET "/asns/ASN-1"
    Then the ETag is "2"

  @bdd
  Scenario: A retried close replays the 200 instead of answering receipt-closed
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/closures" with key "close-1" and body ''
    Then the response status is 200
    When I POST "/receipts/{receipt}/closures" with key "close-1" and body ''
    Then the response status is 200
    And the response header "Idempotency-Replayed" is "true"
    And the response field "state" is "Closed"
    And the outbox holds 1 "receipt.ReceiptClosed" event

  @bdd
  Scenario: The first execution is never marked as a replay
    When I POST "/asns" with key "first-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 201
    And the response header "Idempotency-Replayed" is ""

  @bdd
  Scenario: The key is global: the same key on another route is refused and does nothing
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    When I POST "/asns/ASN-1/cancellations" with key "shared-key" and body ''
    Then the response status is 200
    When I POST "/asns/ASN-2/cancellations" with key "shared-key" and body ''
    Then the response status is 409
    And the problem type is "idempotency-key-reused"
    When I GET "/asns/ASN-2"
    Then the response field "state" is "Registered"

  @bdd
  Scenario: A refused request is replayed as refused, even after the cause is gone
    When I POST "/receipts" with key "open-9" and body '{"asnNumber":"ASN-9"}'
    Then the response status is 422
    And the problem type is "unknown-asn"
    Given the ASN "ASN-9" is registered
    When I POST "/receipts" with key "open-9" and body '{"asnNumber":"ASN-9"}'
    Then the response status is 422
    And the problem type is "unknown-asn"
    And the response header "Idempotency-Replayed" is "true"
    When I GET "/asns/ASN-9"
    Then the response field "state" is "Registered"

  @bdd
  Scenario: A replay returns the stored answer, not the aggregate as it is now
    When I POST "/asns" with key "reg-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}'
    And I POST "/receipts" with body '{"asnNumber":"ASN-1"}'
    And I POST "/asns" with key "reg-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10}]}'
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    And the response field "state" is "Registered"
    And the response field "version" is "1"
    And the ETag is "1"

  @bdd
  Scenario Outline: An Idempotency-Key is at most 128 characters
    When I POST "/asns" with an idempotency key of <length> characters and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is <status>

    Examples:
      | length | status |
      | 1      | 201    |
      | 128    | 201    |
      | 129    | 400    |

  @bdd
  Scenario: A key longer than 128 characters is idempotency-key-invalid and registers nothing
    When I POST "/asns" with an idempotency key of 129 characters and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the problem type is "idempotency-key-invalid"
    And the outbox is empty

  @bdd
  Scenario: A key with a character outside printable ASCII is idempotency-key-invalid
    When I POST "/asns" with key "clé-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 400
    And the problem type is "idempotency-key-invalid"
    And the outbox is empty

  @bdd
  Scenario Outline: A malformed list query names its own problem
    Given the ASN "ASN-1" is registered
    When I list "<path>" with query "<query>"
    Then the response status is 400
    And the problem type is "<slug>"

    Examples:
      | path          | query              | slug                    |
      | /receipts     | limit=0            | invalid-limit           |
      | /receipts     | limit=501          | invalid-limit           |
      | /receipts     | limit=many         | invalid-limit           |
      | /receipts     | cursor=!!!         | invalid-cursor          |
      | /receipts     | state=Maybe        | invalid-query           |
      | /receipts     | colour=red         | unknown-query-parameter |
      | /asns         | limit=0            | invalid-limit           |
      | /asns         | limit=501          | invalid-limit           |
      | /asns         | cursor=!!!         | invalid-cursor          |
      | /asns         | state=Maybe        | invalid-query           |
      | /asns         | asnNumber=ASN-1    | unknown-query-parameter |
      | /appointments | limit=501          | invalid-limit           |
      | /appointments | from=yesterday     | invalid-query           |
      | /appointments | door=has%20space   | invalid-query           |
      | /appointments | colour=red         | unknown-query-parameter |

  @bdd
  Scenario Outline: A page limit of 1 and of 500 is accepted
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    When I list "/asns" with query "limit=<limit>"
    Then the response status is 200
    And the list has <items> items

    Examples:
      | limit | items |
      | 1     | 1     |
      | 500   | 2     |

  @bdd
  Scenario: A ready service answers 200 ready while Postgres and Kafka answer
    Given the readiness gate watches postgres and kafka and reuses a verdict for 1h
    When I GET "/readyz"
    Then the response status is 200
    And the response field "status" is "ready"

  @bdd
  Scenario: A draining service is not ready, yet stays alive
    Given the readiness gate watches postgres and kafka and reuses a verdict for 1h
    And shutdown begins
    When I GET "/readyz"
    Then the response status is 503
    And the response field "status" is "not_ready"
    When I GET "/healthz"
    Then the response status is 200

  @bdd
  Scenario Outline: A dependency that does not answer is named in a dependency-unavailable problem
    Given the readiness gate watches postgres and kafka and reuses a verdict for 1h
    And <down> stops answering
    When I GET "/readyz"
    Then the response status is 503
    And the problem type is "dependency-unavailable"
    And the response field "detail" is "<detail>"

    Examples:
      | down     | detail                  |
      | postgres | not ready: postgres     |
      | kafka    | not ready: kafka        |

  @bdd
  Scenario: Both dependencies down are named in the order they are checked
    Given the readiness gate watches postgres and kafka and reuses a verdict for 1h
    And kafka stops answering
    And postgres stops answering
    When I GET "/readyz"
    Then the response field "detail" is "not ready: postgres, kafka"

  @bdd
  Scenario: A dependency verdict is reused, so probes do not hammer Postgres or Kafka
    Given the readiness gate watches postgres and kafka and reuses a verdict for 1h
    And postgres stops answering
    When I GET "/readyz"
    Then the response status is 503
    When postgres starts answering
    And I GET "/readyz"
    Then the response status is 503

  @bdd
  Scenario: The pod turns ready by itself once the dependency answers and the verdict expires
    Given the readiness gate watches postgres and kafka and reuses a verdict for 10ms
    And kafka stops answering
    When I GET "/readyz"
    Then the response status is 503
    When kafka starts answering
    And the verdict window passes
    And I GET "/readyz"
    Then the response status is 200
    And the response field "status" is "ready"

  @bdd
  Scenario: Without a range the report covers the 30 days ending now
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    When I GET "/reports/receiving-performance"
    Then the response status is 200
    And the response field "from" is "2026-10-15T10:00:00Z"
    And the response field "to" is "2026-11-14T10:00:00Z"
    And the report was computed over "2026-10-15T10:00:00Z" to "2026-11-14T10:00:00Z"
    And the response field "days.0.day" is "2026-10-15"
    And the response field "days.30.day" is "2026-11-14"
    And the response field "days.31.day" is absent

  @bdd
  Scenario: A lone to bound ends the 30 days there, a lone from bound runs until now
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    When I GET "/reports/receiving-performance?to=2026-11-01T00:00:00Z"
    Then the report was computed over "2026-10-02T00:00:00Z" to "2026-11-01T00:00:00Z"
    When I GET "/reports/receiving-performance?from=2026-11-01T00:00:00Z"
    Then the report was computed over "2026-11-01T00:00:00Z" to "2026-11-14T10:00:00Z"

  @bdd
  Scenario Outline: The report range is a non-empty RFC 3339 range of at most 366 days
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    When I GET "/reports/receiving-performance?from=<from>&to=<to>"
    Then the response status is <status>

    Examples:
      | from                 | to                   | status |
      | 2025-11-13T00:00:00Z | 2026-11-14T00:00:00Z | 200    |
      | 2025-11-12T00:00:00Z | 2026-11-14T00:00:00Z | 400    |
      | 2026-11-14T00:00:00Z | 2026-11-14T00:00:00Z | 400    |
      | 2026-11-15T00:00:00Z | 2026-11-14T00:00:00Z | 400    |
      | yesterday            | 2026-11-14T00:00:00Z | 400    |
      | 2026-11-01T00:00:00Z | 2026-11-14            | 400    |

  @bdd
  Scenario: A refused range is invalid-report-range
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    When I GET "/reports/receiving-performance?from=2026-11-14T00:00:00Z&to=2026-11-14T00:00:00Z"
    Then the problem type is "invalid-report-range"

  @bdd
  Scenario: Freshness is null until the projection has applied an event
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    When I GET "/reports/freshness"
    Then the response status is 200
    And the response field "as_of" is "<nil>"
    And the response field "lag_seconds" is "<nil>"

  @bdd
  Scenario Outline: Freshness is how far the clock is past the newest applied event, never negative
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    And the analytical store's newest applied event is at "<applied>"
    When I GET "/reports/freshness"
    Then the response field "as_of" is "<applied>"
    And the response field "lag_seconds" is "<lag>"

    Examples:
      | applied              | lag |
      | 2026-11-14T09:58:30Z | 90  |
      | 2026-11-14T10:00:00Z | 0   |
      | 2026-11-14T10:05:00Z | 0   |

  @bdd
  Scenario Outline: A failing analytical store is a report-store-error that does not echo the cause
    Given the reports service runs at "2026-11-14T10:00:00Z" over an analytical store that has applied nothing
    And the analytical store fails every query
    When I GET "<path>"
    Then the response status is 500
    And the problem type is "report-store-error"
    And the response field "detail" is "the analytical database query failed"

    Examples:
      | path                             |
      | /reports/receiving-performance   |
      | /reports/freshness               |
