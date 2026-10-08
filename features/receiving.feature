Feature: Receive goods against an ASN
  A receipt counts what physically arrived against the ASN lines, Good and
  Damaged apart. Closing it compares against the expectation, closes the ASN
  and completes the linked appointment in one unit of work, and publishes the discrepancies.

  Background:
    Given the ASN "ASN-1" is registered

  Scenario: Opening a walk-in receipt puts the ASN into Receiving
    When I POST "/receipts" with body '{"asnNumber":"ASN-1"}'
    Then the response status is 201
    And the response field "state" is "Open"
    And the response field "lines.0.expectedQty" is "40"
    And the response field "lines.0.receivedGood" is "0"
    And the response field "appointmentId" is absent
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Receiving"

  Scenario: Only one receipt may be open per ASN
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts" with body '{"asnNumber":"ASN-1"}'
    Then the response status is 409
    And the problem type is "receipt-already-open"

  Scenario: A cancelled ASN cannot be received
    Given the ASN "ASN-2" is registered
    And the ASN "ASN-2" is cancelled
    When I POST "/receipts" with body '{"asnNumber":"ASN-2"}'
    Then the response status is 409
    And the problem type is "asn-not-receivable"

  Scenario: Receiving against an unknown ASN
    When I POST "/receipts" with body '{"asnNumber":"ASN-404"}'
    Then the response status is 422
    And the problem type is "unknown-asn"

  Scenario: Receiving Good and Damaged units
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":30,"condition":"Good"}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "30"
    And the response field "version" is "2"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":4,"condition":"Damaged"}'
    Then the response status is 201
    And the response field "lines.0.receivedDamaged" is "4"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptLineReceived"

  Scenario Outline: Invalid receive requests are refused
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '<body>'
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | body                                          | status | slug              |
      | {"lineNo":1,"quantity":1,"condition":"Broken"} | 400    | invalid-condition |
      | {"lineNo":1,"quantity":0,"condition":"Good"}   | 400    | invalid-quantity  |
      | {"lineNo":1,"quantity":1,"condition":"Good","x":1} | 400 | malformed-request |
      | {"lineNo":9,"quantity":1,"condition":"Good"}   | 422    | line-not-on-asn   |

  Scenario: Closing reports Short, Damaged and Over discrepancies
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 30 units of line 1 were received as "Good"
    And 4 units of line 1 were received as "Damaged"
    And 7 units of line 2 were received as "Good"
    When I POST "/receipts/{receipt}/close" with body ''
    Then the response status is 200
    And the response field "state" is "Closed"
    And the response field "discrepancies.0.kind" is "Short"
    And the response field "discrepancies.1.kind" is "Damaged"
    And the response field "discrepancies.2.kind" is "Over"
    And the response field "closedAt" is present
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened, receipt.ReceiptLineReceived, receipt.ReceiptLineReceived, receipt.ReceiptLineReceived, receipt.ReceiptClosed"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Closed"

  Scenario: Closing a fully matching receipt has no discrepancies
    Given the ASN "ASN-2" is registered
    And a walk-in receipt is open for the ASN "ASN-2"
    And 40 units of line 1 were received as "Good"
    And 5 units of line 2 were received as "Good"
    When I POST "/receipts/{receipt}/close" with body ''
    Then the response status is 200
    And the response field "discrepancies.0" is absent

  Scenario: A closed receipt takes no more lines and cannot be closed again
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 1 units of line 1 were received as "Good"
    And the receipt is closed
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":1,"condition":"Good"}'
    Then the response status is 409
    And the problem type is "receipt-closed"
    When I POST "/receipts/{receipt}/close" with body ''
    Then the response status is 409
    And the problem type is "receipt-closed"

  Scenario: Closing a receipt of an appointment completes the appointment
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And the clock is at "2026-10-09T08:00:00Z"
    And the appointment is checked in
    And a receipt is open for the ASN "ASN-1" on the appointment
    When I POST "/receipts/{receipt}/close" with body ''
    Then the response status is 200
    When I GET "/appointments/{appointment}"
    Then the response field "state" is "Completed"

  Scenario: A receipt needs a checked-in appointment
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I POST "/receipts" with body '{"asnNumber":"ASN-1","appointmentId":"{appointment}"}'
    Then the response status is 409
    And the problem type is "appointment-not-checked-in"

  Scenario: A receipt rejects an ASN that the appointment does not cover
    Given the ASN "ASN-2" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And the clock is at "2026-10-09T08:00:00Z"
    And the appointment is checked in
    When I POST "/receipts" with body '{"asnNumber":"ASN-2","appointmentId":"{appointment}"}'
    Then the response status is 422
    And the problem type is "asn-not-on-appointment"
