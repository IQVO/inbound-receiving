Feature: Carton discrepancies are part of the receipt (ADR 0015)
  Every carton discrepancy raised on a receipt, by a scan or by the close, is
  kept on the receipt and read back from GET /receipts/{id}, in raise order.
  Until ADR 0015 they only travelled on the scan answer and on
  CartonDiscrepancyRaised.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the mixed carton ASN "ASN-M" is registered
    And a walk-in receipt is open for the ASN "ASN-M"

  Scenario: a receipt with no finding lists an empty array, never null
    When I GET "/receipts/{receipt}"
    Then the response status is 200
    And the response field "cartonDiscrepancies" is ""
    And the response field "cartonDiscrepancies.0.kind" is absent

  Scenario: a refused scan is listed on the receipt afterwards
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345717"}'
    Then the response field "discrepancies.0.kind" is "UnexpectedCarton"
    When I GET "/receipts/{receipt}"
    Then the response status is 200
    And the response field "cartonDiscrepancies.0.kind" is "UnexpectedCarton"
    And the response field "cartonDiscrepancies.0.sscc" is "003456789012345717"
    And the response field "cartonDiscrepancies.0.at" is present
    And the response field "cartonDiscrepancies.0.expected" is absent
    And the response field "cartonDiscrepancies.0.actual" is absent

  Scenario: findings are listed in the order they were raised
    Given I scanned the carton "003456789012345687"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687"}'
    And I POST "/receipts/{receipt}/scans" with body '{"kind":"EACH_GTIN","id":"6291041500459"}'
    And I GET "/receipts/{receipt}"
    Then the response field "cartonDiscrepancies.0.kind" is "DuplicateSscc"
    And the response field "cartonDiscrepancies.0.sscc" is "003456789012345687"
    And the response field "cartonDiscrepancies.1.kind" is "UnknownGtin"
    And the response field "cartonDiscrepancies.1.actual" is "06291041500459"
    And the response field "cartonDiscrepancies.2.kind" is absent

  Scenario: the MissingCarton found at close is kept on the closed receipt
    Given I scanned the carton "003456789012345670"
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the response status is 200
    And the response field "cartonDiscrepancies.0.kind" is "MissingCarton"
    When I GET "/receipts/{receipt}"
    Then the response field "state" is "Closed"
    And the response field "cartonDiscrepancies.0.kind" is "MissingCarton"
    And the response field "cartonDiscrepancies.0.expected" is "003456789012345687"
    And the response field "cartonDiscrepancies.0.sscc" is absent
    And the response field "cartonDiscrepancies.0.at" is present
    And the response field "discrepancies.0.kind" is "Short"
    And the response field "discrepancies.1.kind" is "Short"
    And the response field "discrepancies.2.kind" is absent
