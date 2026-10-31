Feature: Receive cartons by scan (ADR 0012)
  A carton ASN announces SSCC-labelled cartons. Receiving by scan accepts a
  carton by its SSCC, or cases by their case GTIN, or eaches by their each GTIN.
  ReceiptLineReceived stays the only quantity authority; ReceiptCartonReceived
  carries identity and CartonDiscrepancyRaised carries what did not match.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And a walk-in receipt is open for the ASN "ASN-C"

  Scenario: Registering a carton ASN keeps the roll-up lines and exposes the cartons
    When I GET "/asns/ASN-C/cartons"
    Then the response status is 200
    And the response field "ownerRef" is "tenant-7f3a"
    And the response field "cartons.0.sscc" is "003456789012345670"
    And the response field "cartons.0.status" is "expected"
    And the response field "cartons.1.contents.0.qty" is "20"

  Scenario: A homogeneous case ASN is received by SSCC
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670"}'
    Then the response status is 201
    And the response field "accepted" is "true"
    And the response field "kind" is "SSCC"
    And the response field "lines.0.receivedGood" is "20"
    And the response field "cartons.0.sscc" is "003456789012345670"
    And the response field "cartons.0.lpn" is "003456789012345670"
    And the response field "cartons.0.sealed" is "true"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived"

  Scenario: A raw GS1-128 case label is parsed and received
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C100003456789012345670","expectedKind":"SSCC"}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "20"

  Scenario: Cases are received by their case GTIN
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"20001234567851","qty":2}'
    Then the response status is 201
    And the response field "kind" is "CASE_GTIN"
    And the response field "lines.0.receivedGood" is "40"
    And the response field "cartons.0.sscc" is absent
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived, receipt.ReceiptCartonReceived, receipt.CartonDiscrepancyRaised, receipt.CartonDiscrepancyRaised"
    And the response field "discrepancies.0.kind" is "UnlabelledCarton"

  Scenario: Eaches are received by their each GTIN
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"EACH_GTIN","id":"10001234567892","qty":3}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "3"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived"

  Scenario: Scanning the same SSCC twice books nothing the second time
    Given I scanned the carton "003456789012345670"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "DuplicateSscc"
    And the response field "lines.0.receivedGood" is "20"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived, receipt.CartonDiscrepancyRaised"

  Scenario: A carton the ASN never announced is an unexpected carton
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345717"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "UnexpectedCarton"
    And the response field "lines.0.receivedGood" is "0"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.CartonDiscrepancyRaised"

  Scenario: A carton that never arrived is reported missing when the receipt closes
    Given I scanned the carton "003456789012345670"
    When I POST "/receipts/{receipt}/close" with body ''
    Then the response status is 200
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived, receipt.CartonDiscrepancyRaised, receipt.ReceiptClosed"
    When I GET "/asns/ASN-C/cartons"
    Then the response field "cartons.0.status" is "accepted"
    And the response field "cartons.1.status" is "missing"

  Scenario: A label whose count disagrees with the manifest is a case quantity mismatch
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C10000345678901234567002100012345678923718"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "CaseQtyMismatch"
    And the response field "lines.0.receivedGood" is "0"

  Scenario: A GTIN the packaging view does not know is an unknown GTIN
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"EACH_GTIN","id":"6291041500459"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "UnknownGtin"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.CartonDiscrepancyRaised"

  Scenario: A scan of the wrong kind is rejected without changing anything
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"10001234567892"}'
    Then the response status is 422
    And the problem type is "scan-kind-mismatch"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"

  Scenario: A scan with no kind and no raw string is a bad request
    When I POST "/receipts/{receipt}/scans" with body '{}'
    Then the response status is 400
    And the problem type is "invalid-scan-kind"

  Scenario: An SSCC with a wrong check digit is a bad request
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345671"}'
    Then the response status is 400
    And the problem type is "invalid-sscc"
