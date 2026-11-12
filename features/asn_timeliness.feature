Feature: ASN timeliness and flat S-O-I ASNs (ADR 0025)
  Retailers require the ASN before yard entry (the standards survey: Best Buy
  "required prior to yard entry"), and the most common DESADV/856 form is
  FLAT (S-O-I): line-level SKU/quantity with no SSCC-to-content mapping. The
  visit_arrivals local copy (ADR 0021) already holds the gate arrival;
  OpenReceipt -- the only place an ASN and an appointment meet -- compares the
  ASN's registration time with it and raises AsnLateForArrival once when the
  ASN came in after the truck. A flat ASN is received by content-GTIN scan
  instead of being refused carton by carton as UnexpectedCarton.
  The scenario clock starts at 2026-10-09T07:00:00Z.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-T"

  Scenario: An ASN registered after gate-in raises AsnLateForArrival at open
    Given the ASN "ASN-T" is registered
    And the yard checks the appointment's trailer in at "2026-10-09T06:59:59Z"
    When a receipt is open for the ASN "ASN-T" on the appointment
    Then the outbox holds 1 AsnLateForArrival event

  Scenario: An ASN registered before gate-in raises nothing
    Given the ASN "ASN-T" is registered
    And the yard checks the appointment's trailer in at "2026-10-09T07:00:00Z"
    When a receipt is open for the ASN "ASN-T" on the appointment
    Then the outbox holds 0 AsnLateForArrival events

  Scenario: Without a gate arrival the ASN cannot be late
    Given the ASN "ASN-T" is registered
    When a receipt is open for the ASN "ASN-T" on the appointment
    Then the outbox holds 0 AsnLateForArrival events

  Scenario: A walk-in receipt has no arrival to compare
    Given the ASN "ASN-T" is registered
    And the yard checks the appointment's trailer in at "2026-10-09T06:00:00Z"
    When a walk-in receipt is open for the ASN "ASN-T"
    Then the outbox holds 0 AsnLateForArrival events

  Scenario: The event is raised once even when the open is replayed
    Given the ASN "ASN-T" is registered
    And the yard checks the appointment's trailer in at "2026-10-09T06:00:00Z"
    And a receipt is open for the ASN "ASN-T" on the appointment
    Then the outbox holds 1 AsnLateForArrival event
    When I POST "/receipts" with body '{"asnNumber":"ASN-T","appointmentId":"{appointment}"}'
    Then the response status is 409
    And the outbox holds 1 AsnLateForArrival event

  Scenario: A flat ASN receives by content-GTIN scan
    Given the flat ASN "ASN-F" with lines but no cartons is registered
    And a walk-in receipt is open for the ASN "ASN-F"
    When I scan "]C10000345678901234567002100012345678923712"
    Then the scan is accepted
    And the receipt's line 1 has 12 good units

  Scenario: A flat-ASN carton label without a content GTIN is refused with a finding
    Given the flat ASN "ASN-F" with lines but no cartons is registered
    And a walk-in receipt is open for the ASN "ASN-F"
    When I scan "]C100003456789012345694"
    Then the scan is refused with the finding "MissingContentGtin"
    And the receipt's line 1 has 0 good units

  Scenario: A hierarchical ASN still refuses an unknown SSCC
    Given the pallet carton ASN "ASN-H" is registered
    And a walk-in receipt is open for the ASN "ASN-H"
    When I scan "]C10000345678901234569402100012345678923720"
    Then the scan is refused with the finding "UnexpectedCarton"
