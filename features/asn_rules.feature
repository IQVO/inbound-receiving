# Documentation-derived rules of the Asn aggregate and its lifecycle.
#
# Derived from warehouse-docs contexts/inbound-receiving: aggregate-design-canvas
# (Asn: state transitions, enforced invariants, handled commands, created events),
# use-cases (RegisterAsn, CancelAsn, ListAsns), domain-events (ASNRegistered,
# ASNCancelled payloads) and troubleshooting (asn-* problem types).
# Every scenario names one rule; boundaries are Outline rows (at the limit
# accepted, one past it refused).

Feature: ASN rules from the documentation
  An advance ship notice is validated at registration, moves only along
  Registered -> Receiving -> Closed or Registered -> Cancelled, and publishes
  exactly two events: ASNRegistered and ASNCancelled.

  @bdd
  Scenario Outline: The ASN number is 1..64 characters of [A-Za-z0-9._-]
    When I POST "/asns" with body '{"asnNumber":"<number>","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is <status>
    And the outbox event types are "<events>"

    Examples:
      | number                                                            | status | events            |
      | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  | 201    | asn.ASNRegistered |
      | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA | 400    |                   |
      | asn.v2_2026-10-09                                                 | 201    | asn.ASNRegistered |
      | ASN/1                                                             | 400    |                   |
      | ASN#1                                                             | 400    |                   |
      | ASÑ-1                                                             | 400    |                   |

  @bdd
  Scenario Outline: The supplier reference is at most 64 characters without control characters
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"<ref>","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is <status>
    And the outbox event types are "<events>"

    Examples:
      | ref                                                               | status | events            |
      | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  | 201    | asn.ASNRegistered |
      | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA | 400    |                   |
      | éééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé  | 201    | asn.ASNRegistered |
      | ééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé | 400    |                   |
      | ACME\u0007Foods                                                   | 400    |                   |

  @bdd
  Scenario Outline: An expected quantity is 1..2147483647
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":<qty>}]}'
    Then the response status is <status>
    And the response field "lines.0.expectedQty" is "<echo>"

    Examples:
      | qty        | status | echo       |
      | 1          | 201    | 1          |
      | 2147483647 | 201    | 2147483647 |

  @bdd
  Scenario Outline: A quantity outside 1..2147483647 is refused as invalid-quantity
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":<qty>}]}'
    Then the response status is 400
    And the problem type is "invalid-quantity"
    And the outbox is empty

    Examples:
      | qty        |
      | 2147483648 |
      | -1         |

  @bdd
  Scenario Outline: Line numbers must be exactly 1..n in order
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[<lines>]}'
    Then the response status is 400
    And the problem type is "invalid-line-no"
    And the outbox is empty

    Examples:
      | lines                                                                                       |
      | {"lineNo":0,"sku":"SKU-1","expectedQty":1}                                                  |
      | {"lineNo":2,"sku":"SKU-2","expectedQty":1},{"lineNo":1,"sku":"SKU-1","expectedQty":1}       |
      | {"lineNo":1,"sku":"SKU-1","expectedQty":1},{"lineNo":3,"sku":"SKU-3","expectedQty":1}       |

  @bdd
  Scenario: A cancelled ASN keeps its number, so it cannot be registered again
    Given the ASN "ASN-1001" is registered
    And the ASN "ASN-1001" is cancelled
    When I POST "/asns" with body '{"asnNumber":"ASN-1001","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 409
    And the problem type is "asn-already-exists"
    And the outbox event types are "asn.ASNRegistered, asn.ASNCancelled"

  @bdd
  Scenario: Until product-master announces a SKU the registration is refused, afterwards it succeeds
    Given the product registry is enforced and knows "SKU-1"
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10},{"lineNo":2,"sku":"SKU-2","expectedQty":4}]}'
    Then the response status is 422
    And the problem type is "unknown-sku"
    When product-master announces event "evt-sku-2" registering SKU "SKU-2"
    Then the consumer reports "applied"
    When I POST "/asns" with body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":10},{"lineNo":2,"sku":"SKU-2","expectedQty":4}]}'
    Then the response status is 201
    And the outbox event types are "asn.ASNRegistered"

  @bdd
  Scenario: ASNRegistered carries the announced lines and omits what the supplier did not give
    When I POST "/asns" with body '{"asnNumber":"ASN-7","supplierRef":"ACME Foods","expectedArrival":"2026-10-10T08:00:00Z","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40},{"lineNo":2,"sku":"SKU-2","expectedQty":5}]}'
    Then the response status is 201
    And the last "asn.ASNRegistered" event has "subject" equal to "ASN-7"
    And the last "asn.ASNRegistered" event has "data.asn_number" equal to "ASN-7"
    And the last "asn.ASNRegistered" event has "data.supplier_ref" equal to "ACME Foods"
    And the last "asn.ASNRegistered" event has "data.expected_arrival" equal to "2026-10-10T08:00:00Z"
    And the last "asn.ASNRegistered" event has "data.lines.1.line_no" equal to "2"
    And the last "asn.ASNRegistered" event has "data.lines.1.sku" equal to "SKU-2"
    And the last "asn.ASNRegistered" event has "data.lines.1.expected_qty" equal to "5"
    And the last "asn.ASNRegistered" event has no "data.cartons"
    And the last "asn.ASNRegistered" event has no "data.owner_ref"
    When I POST "/asns" with body '{"asnNumber":"ASN-8","supplierRef":"ACME Foods","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the last "asn.ASNRegistered" event has "data.asn_number" equal to "ASN-8"
    And the last "asn.ASNRegistered" event has no "data.expected_arrival"

  @bdd
  Scenario: Cancelling an ASN that is being received is refused and changes nothing
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/asns/ASN-1/cancellations" with body '{"reason":"changed our mind"}'
    Then the response status is 409
    And the problem type is "asn-in-progress"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Receiving"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"

  @bdd
  Scenario: Cancelling a closed ASN is refused as terminal
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    And the receipt is closed
    When I POST "/asns/ASN-1/cancellations" with body ''
    Then the response status is 409
    And the problem type is "asn-terminal"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Closed"

  @bdd
  Scenario: A current If-Match lets the cancellation through
    Given the ASN "ASN-1" is registered
    When I POST "/asns/ASN-1/cancellations" with If-Match "1" and body ''
    Then the response status is 200
    And the response field "state" is "Cancelled"
    And the ETag is "2"

  @bdd
  Scenario: Cancelling an ASN that was never registered
    When I POST "/asns/ASN-404/cancellations" with body ''
    Then the response status is 404
    And the problem type is "asn-not-found"
    And the outbox is empty

  @bdd
  Scenario: ASNCancelled carries the reason when one was given and omits it otherwise
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    When I POST "/asns/ASN-1/cancellations" with body '{"reason":"Supplier cancelled the shipment"}'
    Then the last "asn.ASNCancelled" event has "subject" equal to "ASN-1"
    And the last "asn.ASNCancelled" event has "data.reason" equal to "Supplier cancelled the shipment"
    When I POST "/asns/ASN-2/cancellations" with body ''
    Then the last "asn.ASNCancelled" event has "data.asn_number" equal to "ASN-2"
    And the last "asn.ASNCancelled" event has no "data.reason"

  @bdd
  Scenario Outline: A cancellation reason is at most 200 characters
    Given the ASN "ASN-1" is registered
    When I cancel the ASN "ASN-1" with a reason of <length> characters
    Then the response status is <status>
    And the outbox holds <cancelled> "asn.ASNCancelled" events

    Examples:
      | length | status | cancelled |
      | 200    | 200    | 1         |
      | 201    | 400    | 0         |

  @bdd
  Scenario: An ASN advances one version per transition: Registered, Receiving, Closed
    Given the ASN "ASN-1" is registered
    When I GET "/asns/ASN-1"
    Then the ETag is "1"
    And the response field "state" is "Registered"
    When a walk-in receipt is open for the ASN "ASN-1"
    And I GET "/asns/ASN-1"
    Then the ETag is "2"
    And the response field "state" is "Receiving"
    When the receipt is closed
    And I GET "/asns/ASN-1"
    Then the ETag is "3"
    And the response field "state" is "Closed"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptClosed, receipt.ReceivingAdviceIssued"

  @bdd
  Scenario: A closed ASN is not receivable again
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    And the receipt is closed
    When I POST "/receipts" with body '{"asnNumber":"ASN-1"}'
    Then the response status is 409
    And the problem type is "asn-not-receivable"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Closed"

  @bdd
  Scenario Outline: The ASN list narrows to each lifecycle state
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    And the ASN "ASN-3" is registered
    And the ASN "ASN-4" is registered
    And a walk-in receipt is open for the ASN "ASN-2"
    And a walk-in receipt is open for the ASN "ASN-3"
    And the receipt is closed
    And the ASN "ASN-4" is cancelled
    When I list "/asns" with query "state=<state>"
    Then the listed "asnNumber" values are "<asn>"

    Examples:
      | state      | asn   |
      | Registered | ASN-1 |
      | Receiving  | ASN-2 |
      | Closed     | ASN-3 |
      | Cancelled  | ASN-4 |
