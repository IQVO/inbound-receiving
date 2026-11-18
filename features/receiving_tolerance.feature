Feature: Receiving tolerance policy (ADR 0026)
  Normal commercial variance stops generating discrepancy findings and
  supplier disputes: an over/under band per (ownerRef, supplierRef)
  auto-accepts inside the band, demands an override reason code between the
  band and the hard stop, and refuses (409 tolerance-blocked, no override
  possible) at or beyond the hard stop. Without a declared policy, receiving
  is exactly what it was yesterday: additive only, no default band.
  The scenario clock starts at 2026-10-09T07:00:00Z.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the ASN "ASN-T" of supplier "ACME" is registered with 10000 units of "SKU-1"
    And a walk-in receipt is open for the ASN "ASN-T"
    And the house declares a tolerance policy for supplier "ACME" of 5% over, 10% over hard stop, 2% under, 20% under hard stop

  Scenario: 3% over with a 5% band auto-accepts at close
    When 10300 units are received good on line 1
    And the receipt is closed
    Then the close answers 200
    And the closed receipt reports no discrepancy findings

  Scenario: 12% over hard-stops the close
    When 11200 units are received good on line 1
    And the close is attempted
    Then the close answers 409 tolerance-blocked
    And the receipt is still open

  Scenario: 6% over between band and hard stop needs the override
    When 10600 units are received good on line 1
    And the close is attempted
    Then the close answers 409 tolerance-override-required
    And the receipt is still open

  Scenario: 6% over with an override reason closes on the tolerance kind
    When 10600 units are received good on line 1
    And the receipt is closed with override reason "supplier-agreed"
    Then the close answers 200
    And the closed receipt reports the discrepancy finding "OverTolerance" with reason "supplier-agreed"

  Scenario: An override reason from outside the closed list is refused
    When 10600 units are received good on line 1
    And the receipt is closed with override reason "because"
    Then the close answers 400 invalid-override-reason

  Scenario: A scan that pushes a line at or beyond the hard stop books nothing
    When 12000 units are scanned good by each GTIN
    Then the scan answers 409 tolerance-blocked
    And the receipt has received 0 units of line 1

  Scenario: A scan beyond the band without a reason is refused
    When 10600 units are scanned good by each GTIN
    Then the scan answers 409 tolerance-override-required
    And the receipt has received 0 units of line 1

  Scenario: A scan beyond the band with a reason books
    When 10600 units are scanned good by each GTIN with override reason "recount-confirmed"
    Then the scan answers 201
    And the receipt has received 10600 units of line 1

  Scenario: Without a policy the close is exactly yesterday's
    Given no tolerance policy is declared for supplier "ACME"
    When 10300 units are received good on line 1
    And the receipt is closed
    Then the close answers 200
    And the closed receipt reports the discrepancy finding "Over" with no reason

  Scenario: An owner-keyed policy rules only that owner's receipt
    Given the owner "tenant-7f3a" declares a tolerance policy for supplier "ACME" of 1% over, 5% over hard stop, 1% under, 5% under hard stop
    And the ASN "ASN-OWN" of owner "tenant-7f3a" and supplier "ACME" is registered with 10000 units of "SKU-1"
    And a walk-in receipt is open for the owner's ASN "ASN-OWN"
    When 10300 units are received good on line 1
    And the receipt of the ASN "ASN-OWN" is closed with override reason "supplier-agreed"
    Then the close answers 200
    And the closed receipt reports the discrepancy finding "OverTolerance" with reason "supplier-agreed"

  Scenario: The house policy does not fall back to a client-owned receipt
    Given the ASN "ASN-OWN" of owner "tenant-7f3a" and supplier "ACME" is registered with 10000 units of "SKU-1"
    And a walk-in receipt is open for the owner's ASN "ASN-OWN"
    When 10600 units are received good on line 1
    And the receipt of the ASN "ASN-OWN" is closed
    Then the close answers 200
    And the closed receipt reports the discrepancy finding "Over" with no reason

  Scenario: The policy admin route answers the stored declaration
    When the policy of supplier "ACME" is fetched
    Then the policy answers 200 with over tolerance 0.05 and over hard stop 0.1
    When the policy of supplier "NOBODY" is fetched
    Then the policy answers 404 receiving-policy-not-found
