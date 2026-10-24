Feature: Register an ASN with cartons (ADR 0012)
  An ASN may announce SSCC-labelled cartons. The lines stay the per-SKU
  roll-up and the cartons must add up to them. A flat ASN is unchanged.

  Scenario: A carton ASN registers with its owner, cartons and a roll-up
    When I POST "/asns" with body '{"asnNumber":"ASN-C2","supplierRef":"ACME","ownerRef":"tenant-7f3a","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],"cartons":[{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}'
    Then the response status is 201
    And the response field "ownerRef" is "tenant-7f3a"
    And the response field "cartons.0.kind" is "HOMOGENEOUS_CASE"
    And the response field "cartons.0.contents.0.gtin" is "10001234567892"
    And the outbox event types are "asn.ASNRegistered"

  Scenario: Cartons that do not add up to the lines are refused
    When I POST "/asns" with body '{"asnNumber":"ASN-C3","supplierRef":"ACME","ownerRef":"tenant-7f3a","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],"cartons":[{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":19}]}]}'
    Then the response status is 422
    And the problem type is "carton-contents-mismatch"
    And the outbox is empty

  Scenario: The same SSCC twice on one ASN is refused
    When I POST "/asns" with body '{"asnNumber":"ASN-C4","supplierRef":"ACME","ownerRef":"tenant-7f3a","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40}],"cartons":[{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]},{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}'
    Then the response status is 422
    And the problem type is "duplicate-sscc"

  Scenario: An SSCC already announced on another ASN is refused
    Given the carton ASN "ASN-C" is registered
    When I POST "/asns" with body '{"asnNumber":"ASN-C5","supplierRef":"ACME","ownerRef":"tenant-7f3a","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],"cartons":[{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}'
    Then the response status is 422
    And the problem type is "duplicate-sscc"

  Scenario: A homogeneous carton holding two SKUs is refused
    When I POST "/asns" with body '{"asnNumber":"ASN-C6","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20},{"lineNo":2,"sku":"SKU-2","expectedQty":5}],"cartons":[{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20},{"lineNo":2,"sku":"SKU-2","gtin":"6291041500213","qty":5}]}]}'
    Then the response status is 422
    And the problem type is "homogeneous-carton-mixed"

  Scenario: An SSCC with a wrong check digit is refused
    When I POST "/asns" with body '{"asnNumber":"ASN-C7","supplierRef":"ACME","ownerRef":"tenant-7f3a","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],"cartons":[{"sscc":"003456789012345671","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}'
    Then the response status is 422
    And the problem type is "invalid-sscc"

  Scenario: A flat ASN is unchanged and has no cartons
    Given the ASN "ASN-1" is registered
    When I GET "/asns/ASN-1"
    Then the response field "ownerRef" is absent
    And the response field "cartons" is absent
    When I GET "/asns/ASN-1/cartons"
    Then the response status is 200
    And the response field "cartons" is ""
    And the outbox event types are "asn.ASNRegistered"

  Scenario: A flat ASN is still received by line
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":30,"condition":"Good"}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "30"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived"
