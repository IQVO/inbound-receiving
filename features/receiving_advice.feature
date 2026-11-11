Feature: Receiving advice at close, with the standard discrepancy vocabulary (ADR 0023)
  When a receipt closes, the supplier or the 3PL depositor is owed a receiving
  advice: per line what was received and accepted, with discrepancies coded in
  the X12 W13 receiving condition codes and GS1 CBV dispositions, inside a
  committed window after the last unit was received (24 h by default). This
  context only raises the domain fact ReceivingAdviceIssued, exactly once per
  receipt, in the close transaction; rendering it as EANCOM or X12 is
  partner-connectivity's job. An Open receipt past the window raises
  ReceivingAdviceOverdue once, from the housekeeping sweep.
  The scenario clock starts at 2026-10-09T07:00:00Z; ASN-1 has line 1 for
  40 units of SKU-1 and line 2 for 5 units of SKU-2.

  Background:
    Given the ASN "ASN-1" is registered
    And a walk-in receipt is open for the ASN "ASN-1"

  Scenario: Closing a receipt emits ReceivingAdviceIssued with W13-coded lines
    Given 34 units of line 1 were received as "Good"
    And 4 units of line 1 were received as "Damaged"
    And 5 units of line 2 were received as "Good"
    When the receipt is closed
    Then the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptLineReceived, receipt.ReceiptLineReceived, receipt.ReceiptClosed, receipt.ReceivingAdviceIssued"
    And the receiving advice is for the ASN "ASN-1" from supplier "ACME" and is inside the SLA
    And the advice line 1 received 38, accepted 34, rejected 4 and held 0
    And the advice line 1 has the discrepancy "Short" of 2 coded "02" and "mismatch_quantity"
    And the advice line 1 has the discrepancy "Damaged" of 4 coded "01" and "damaged"
    And the advice line 2 has no discrepancy

  Scenario: An over-received line is coded 03
    Given 41 units of line 1 were received as "Good"
    When the receipt is closed
    Then the advice line 1 has the discrepancy "Over" of 1 coded "03" and "mismatch_quantity"

  Scenario: A line the operator declared held is coded 09, non-conformant
    Given 40 units of line 1 were received as "Good"
    And the operator holds 5 units of line 1
    When the receipt is closed
    Then the advice line 1 received 40, accepted 35, rejected 0 and held 5
    And the advice line 1 has the discrepancy "Hold" of 5 coded "09" and "non_conformant"

  Scenario: Holding more than the Good units is refused
    Given 3 units of line 1 were received as "Good"
    When the operator holds 4 units of line 1
    Then the hold is refused

  Scenario: A late close is inside no SLA but still issues one advice
    Given 10 units of line 1 were received as "Good"
    And the clock is at "2026-10-10T07:00:01Z"
    When the receipt is closed
    Then the receiving advice is for the ASN "ASN-1" from supplier "ACME" and is outside the SLA
    And the outbox holds 1 ReceivingAdviceIssued event

  Scenario: Closing at exactly the SLA is still inside it
    Given 10 units of line 1 were received as "Good"
    And the clock is at "2026-10-10T07:00:00Z"
    When the receipt is closed
    Then the receiving advice is for the ASN "ASN-1" from supplier "ACME" and is inside the SLA

  Scenario: An open receipt past the SLA raises ReceivingAdviceOverdue once
    Given 10 units of line 1 were received as "Good"
    And the clock is at "2026-10-10T08:00:00Z"
    When the housekeeping sweep runs
    Then the outbox holds 1 ReceivingAdviceOverdue event
    And the overdue alarm says the advice was due at "2026-10-10T07:00:00Z"
    When the clock is at "2026-10-12T08:00:00Z"
    And the housekeeping sweep runs
    Then the outbox holds 1 ReceivingAdviceOverdue event

  Scenario: A receipt inside the SLA raises no alarm
    Given 10 units of line 1 were received as "Good"
    And the clock is at "2026-10-10T06:59:59Z"
    When the housekeeping sweep runs
    Then the outbox holds 0 ReceivingAdviceOverdue events

  Scenario: A closed receipt is not overdue
    Given 10 units of line 1 were received as "Good"
    And the receipt is closed
    And the clock is at "2026-10-20T07:00:00Z"
    When the housekeeping sweep runs
    Then the outbox holds 0 ReceivingAdviceOverdue events

  Scenario: Closing an overdue receipt still issues the advice, outside the SLA
    Given 10 units of line 1 were received as "Good"
    And the clock is at "2026-10-10T08:00:00Z"
    And the housekeeping sweep runs
    When the receipt is closed
    Then the receiving advice is for the ASN "ASN-1" from supplier "ACME" and is outside the SLA
    And the outbox holds 1 ReceivingAdviceOverdue event
    And the outbox holds 1 ReceivingAdviceIssued event

  Scenario: The REST receipt carries the same coding
    Given 34 units of line 1 were received as "Good"
    When the receipt is closed
    And I GET "/receipts/{receipt}"
    Then the response field "discrepancies.0.kind" is "Short"
    And the response field "discrepancies.0.w13Code" is "02"
    And the response field "discrepancies.0.cbvDisposition" is "mismatch_quantity"
