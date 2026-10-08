Feature: Register an ASN
  A supplier's advance ship notice is registered with its numbered lines.
  Registration is a creating POST: it needs an Idempotency-Key.

  Scenario: Registering a new ASN creates it at version 1
    When I POST "/asns" with body '{"asnNumber":"ASN-1001","supplierRef":"ACME","expectedArrival":"2026-10-10T08:00:00Z","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40},{"lineNo":2,"sku":"SKU-2","expectedQty":5}]}'
    Then the response status is 201
    And the response field "asnNumber" is "ASN-1001"
    And the response field "state" is "Registered"
    And the response field "version" is "1"
    And the response field "lines.1.sku" is "SKU-2"
    And the ETag is "1"
    And the outbox event types are "asn.ASNRegistered"

  Scenario: Reading a registered ASN
    Given the ASN "ASN-1001" is registered
    When I GET "/asns/ASN-1001"
    Then the response status is 200
    And the response field "supplierRef" is "ACME"
    And the response field "expectedArrival" is "2026-10-10T08:00:00Z"

  Scenario: Reading an unknown ASN
    When I GET "/asns/ASN-404"
    Then the response status is 404
    And the problem type is "asn-not-found"

  Scenario: Registering the same number twice
    Given the ASN "ASN-1001" is registered
    When I POST "/asns" with body '{"asnNumber":"ASN-1001","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 409
    And the problem type is "asn-already-exists"
    And the outbox event types are "asn.ASNRegistered"

  Scenario Outline: Invalid ASNs are rejected
    When I POST "/asns" with body '<body>'
    Then the response status is 400
    And the problem type is "<slug>"
    And the outbox is empty

    Examples:
      | body                                                                                                                       | slug                |
      | {"asnNumber":"A B","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}                              | invalid-asn-number  |
      | {"asnNumber":"A","supplierRef":" ","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}                                   | invalid-supplier-ref |
      | {"asnNumber":"A","supplierRef":"ACME","lines":[]}                                                                           | asn-requires-lines  |
      | {"asnNumber":"A","supplierRef":"ACME","lines":[{"lineNo":2,"sku":"SKU-1","expectedQty":1}]}                                | invalid-line-no     |
      | {"asnNumber":"A","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1},{"lineNo":2,"sku":"SKU-1","expectedQty":1}]} | duplicate-sku       |
      | {"asnNumber":"A","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"a/b","expectedQty":1}]}                                  | invalid-sku         |
      | {"asnNumber":"A","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":0}]}                                | invalid-quantity    |
      | {"asnNumber":"A","supplierRef":"ACME","extra":1,"lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}                      | malformed-request   |
      | {"asnNumber":                                                                                                               | malformed-request   |

  Scenario: An unknown SKU is refused when the product registry is enforced
    Given the product registry is enforced and knows "SKU-1"
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1},{"lineNo":2,"sku":"SKU-9","expectedQty":1}]}'
    Then the response status is 422
    And the problem type is "unknown-sku"
    And the outbox is empty

  Scenario: Known SKUs are accepted when the product registry is enforced
    Given the product registry is enforced and knows "SKU-1, SKU-2"
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1},{"lineNo":2,"sku":"SKU-2","expectedQty":1}]}'
    Then the response status is 201

  Scenario: Cancelling a registered ASN
    Given the ASN "ASN-1001" is registered
    When I POST "/asns/ASN-1001/cancel" with body '{"reason":"Supplier cancelled the shipment"}'
    Then the response status is 200
    And the response field "state" is "Cancelled"
    And the response field "version" is "2"
    And the outbox event types are "asn.ASNRegistered, asn.ASNCancelled"

  Scenario: A cancelled ASN cannot be cancelled again
    Given the ASN "ASN-1001" is registered
    And the ASN "ASN-1001" is cancelled
    When I POST "/asns/ASN-1001/cancel" with body ''
    Then the response status is 409
    And the problem type is "asn-terminal"

  Scenario: Cancelling with a stale version
    Given the ASN "ASN-1001" is registered
    When I POST "/asns/ASN-1001/cancel" with If-Match "7" and body ''
    Then the response status is 412
    And the problem type is "version-mismatch"

  Scenario: Listing ASNs pages by number
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    And the ASN "ASN-3" is registered
    When I list "/asns" with query "limit=2"
    Then the listed "asnNumber" values are "ASN-1, ASN-2"
    And the response field "nextCursor" is present
    When I list the next page
    Then the listed "asnNumber" values are "ASN-3"
    And the response field "nextCursor" is absent
