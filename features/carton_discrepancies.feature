Feature: Carton discrepancies (ADR 0012, ADR 0014)
  Every carton discrepancy kind is raised as CartonDiscrepancyRaised and shown
  in the scan response. ReceiptClosed keeps its three v1 kinds. A carton that
  is physically on the dock is never rejected: findings that need a human are
  raised next to an accepted carton.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the packaging view knows SKU "SKU-2" with each GTIN "10001234567908" and case GTIN "20001234567905" of 5
    And the mixed carton ASN "ASN-M" is registered
    And a walk-in receipt is open for the ASN "ASN-M"

  Scenario: UnexpectedCarton - a valid SSCC that is not on the manifest
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345717"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "UnexpectedCarton"
    And the response field "discrepancies.0.sscc" is "003456789012345717"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.CartonDiscrepancyRaised"

  Scenario: MissingCarton - a manifest carton never scanned is reported at close
    Given I scanned the carton "003456789012345670"
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the response status is 200
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived, receipt.CartonDiscrepancyRaised, receipt.ReceiptClosed"
    When I GET "/asns/ASN-M/cartons"
    Then the response field "cartons.1.sscc" is "003456789012345687"
    And the response field "cartons.1.status" is "missing"

  Scenario: DuplicateSscc - the same SSCC scanned twice books nothing the second time
    Given I scanned the carton "003456789012345687"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "DuplicateSscc"
    And the response field "lines.0.receivedGood" is "20"

  Scenario: UnknownGtin - a GTIN the packaging view does not know
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"EACH_GTIN","id":"6291041500459"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "UnknownGtin"
    And the response field "discrepancies.0.actual" is "06291041500459"

  Scenario: CaseQtyMismatch - the label counts what the manifest does not
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C10000345678901234567002100012345678923718"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "CaseQtyMismatch"
    And the response field "discrepancies.0.expected" is "20"
    And the response field "discrepancies.0.actual" is "18"
    And the response field "lines.0.receivedGood" is "0"

  Scenario: MixedCaseDeclaredAsCasePack - the label of a homogeneous case names another SKU
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C10000345678901234567002100012345679083720"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "MixedCaseDeclaredAsCasePack"
    And the response field "discrepancies.0.expected" is "SKU-1"
    And the response field "discrepancies.0.actual" is "SKU-2"

  Scenario: UnlabelledCarton - a case with no SSCC is accepted, gets an internal plate and a label
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"20001234567851"}'
    Then the response status is 201
    And the response field "accepted" is "true"
    And the response field "discrepancies.0.kind" is "UnlabelledCarton"
    And the response field "cartons.0.sscc" is absent
    And I remember the response field "cartons.0.lpn" as the carton plate
    When I GET "/receipts/{receipt}/cartons/{lpn}/label"
    Then the response status is 200
    And the response field "symbology" is "CODE128"
    And the response field "sku" is "SKU-1"
    And the response field "units" is "20"
    And the response field "humanReadable" is present

  Scenario: CartonOverLimit - an over-weight, over-long carton is still accepted
    Given the carton limits are 20000 g and 1200 mm
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670"}'
    Then the response status is 201
    And the response field "accepted" is "true"
    And the response field "lines.0.receivedGood" is "20"
    And the response field "discrepancies.0.kind" is "CartonOverLimit"
    And the response field "discrepancies.0.expected" is "grossWeightG:20000,longestSideMm:1200"
    And the response field "discrepancies.0.actual" is "grossWeightG:25000,longestSideMm:1300"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptCartonReceived, receipt.CartonDiscrepancyRaised"

  Scenario: CartonOverLimit is not checked while the limits are unset
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670"}'
    Then the response status is 201
    And the response field "discrepancies.0" is absent

  Scenario: MixedCartonContentMismatch - the received quantity is the counted one
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687","countedContents":[{"sku":"SKU-1","qty":18},{"sku":"SKU-2","qty":5}]}'
    Then the response status is 201
    And the response field "accepted" is "true"
    And the response field "discrepancies.0.kind" is "MixedCartonContentMismatch"
    And the response field "discrepancies.0.expected" is "SKU-1:20"
    And the response field "discrepancies.0.actual" is "SKU-1:18"
    And the response field "lines.0.receivedGood" is "18"
    And the response field "lines.1.receivedGood" is "5"

  Scenario: A mixed carton whose count matches the manifest raises nothing
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687","countedContents":[{"sku":"SKU-1","qty":20},{"sku":"SKU-2","qty":5}]}'
    Then the response status is 201
    And the response field "discrepancies.0" is absent
    And the response field "lines.0.receivedGood" is "20"

  Scenario: A count on a homogeneous carton is refused and changes nothing
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670","countedContents":[{"sku":"SKU-1","qty":20}]}'
    Then the response status is 422
    And the problem type is "counted-contents-not-mixed"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"

  Scenario: The label of a carton the receipt never accepted is not found
    When I GET "/receipts/{receipt}/cartons/LPN-NOPE/label"
    Then the response status is 404
    And the problem type is "carton-not-found"
