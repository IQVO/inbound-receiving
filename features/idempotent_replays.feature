Feature: Idempotent creating requests
  Resource-creating POSTs require an Idempotency-Key. A retry with the same key
  and request replays the first response and changes nothing; the same key with
  another request is refused. This is what keeps a network retry of a receive
  scan from counting the units twice.

  Scenario: A creating POST without a key is refused
    When I POST "/asns" without an idempotency key and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 400
    And the problem type is "idempotency-key-required"
    And the outbox is empty

  Scenario: Replaying a registration returns the first response and publishes nothing new
    When I POST "/asns" with key "k-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    And I POST "/asns" with key "k-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 201
    And the response field "version" is "1"
    And the outbox event types are "asn.ASNRegistered"

  Scenario: Reusing a key for another request is refused
    When I POST "/asns" with key "k-1" and body '{"asnNumber":"ASN-1","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    And I POST "/asns" with key "k-1" and body '{"asnNumber":"ASN-2","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":1}]}'
    Then the response status is 422
    And the problem type is "idempotency-key-reused"

  Scenario: Replaying a receive scan counts the units once
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with key "scan-42" and body '{"lineNo":1,"quantity":40,"condition":"Good"}'
    And I POST "/receipts/{receipt}/lines" with key "scan-42" and body '{"lineNo":1,"quantity":40,"condition":"Good"}'
    Then the response status is 201
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "40"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived"

  Scenario: Receiving requires a key as well
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" without an idempotency key and body '{"lineNo":1,"quantity":1,"condition":"Good"}'
    Then the response status is 400
    And the problem type is "idempotency-key-required"
