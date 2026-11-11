Feature: GS1 logistic-label receiving (ADR 0024)
  A scanned GS1-128 label is validated at the scan: a label that breaks the
  standard, or that names another site, is refused AND recorded as a carton
  finding (nothing is booked). What an accepted label says (its dates) is kept,
  and the earliest best-before of a pallet is reported. A case that arrives with
  no label gets an SSCC from the receiver's own GS1 company prefix.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the pallet carton ASN "ASN-P" is registered
    And a walk-in receipt is open for the ASN "ASN-P"

  Scenario: a (01)+(02) label is refused and a LabelNonCompliant finding recorded
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C1011000123456789202100012345678923720"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "LabelNonCompliant"
    And the response field "discrepancies.0.expected" is "conflicting_ais"
    And the response field "discrepancies.0.supplierRef" is "ACME"
    And the response field "discrepancies.0.cbvDisposition" is "non_conformant"
    And the response field "discrepancies.0.w13Code" is absent
    And the response field "lines.0.receivedGood" is "0"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.CartonDiscrepancyRaised"
    When I GET "/receipts/{receipt}"
    Then the response field "cartonDiscrepancies.0.kind" is "LabelNonCompliant"
    And the response field "cartonDiscrepancies.0.supplierRef" is "ACME"

  Scenario: text that is not a GS1-128 element string is still a 400 and records nothing
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"hello"}'
    Then the response status is 400
    And the problem type is "invalid-scan"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"

  Scenario: a label for another GLN is refused as WrongDestination and one for this site is accepted
    Given the receiving site GLNs are "4012345000009"
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C1000034567890123456704107612345000008"}'
    Then the response status is 200
    And the response field "accepted" is "false"
    And the response field "discrepancies.0.kind" is "WrongDestination"
    And the response field "discrepancies.0.actual" is "7612345000008"
    And the response field "discrepancies.0.sscc" is "003456789012345670"
    And the response field "discrepancies.0.cbvDisposition" is "mismatch_instance"
    And the response field "lines.0.receivedGood" is "0"
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C1000034567890123456704104012345000009"}'
    Then the response status is 201
    And the response field "accepted" is "true"
    And the response field "cartons.0.shipToGln" is "4012345000009"

  Scenario: the destination rule is off while no site GLN is configured
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C1000034567890123456704107612345000008"}'
    Then the response status is 201
    And the response field "cartons.0.shipToGln" is "7612345000008"

  Scenario: a mixed pallet reports the earliest best-before
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C1000034567890123456701527030017270630"}'
    Then the response status is 201
    And the response field "cartons.0.bestBefore" is "2027-03-31"
    And the response field "cartons.0.ssccSource" is "supplier"
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C100003456789012345687"}'
    Then the response status is 201
    When I GET "/receipts/{receipt}"
    Then the response field "pallets.0.parentSscc" is "003456789099999995"
    And the response field "pallets.0.cartonCount" is "2"
    And the response field "pallets.0.earliestBestBefore" is "2027-03-31"
    And the response field "pallets.0.earliestExpiry" is "2027-06-30"
    And the response field "lines.0.earliestBestBefore" is "2027-03-31"
    When I POST "/receipts/{receipt}/scans" with body '{"scan":"]C100003456789012345687"}'
    Then the response status is 200

  Scenario: an unlabelled carton gets a receiver-assigned SSCC
    Given the receiver GS1 company prefix is "0614141"
    When I POST "/receipts/{receipt}/sscc-assignments" with body '{"gtin":"20001234567851"}'
    Then the response status is 201
    And the response field "sscc" is "006141410000000012"
    And the response field "humanReadable" is "(00)006141410000000012"
    And the response field "elementString" is "00006141410000000012"
    And the response field "ssccSource" is "receiver"
    And the response field "units" is "20"
    And the response field "discrepancies.0.kind" is "UnlabelledCarton"
    When I POST "/receipts/{receipt}/sscc-assignments" with body '{"gtin":"20001234567851"}'
    Then the response field "sscc" is "006141410000000029"
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "40"

  Scenario: AssignSscc without a configured prefix answers 409
    When I POST "/receipts/{receipt}/sscc-assignments" with body '{"gtin":"20001234567851"}'
    Then the response status is 409
    And the problem type is "sscc-prefix-not-configured"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"
