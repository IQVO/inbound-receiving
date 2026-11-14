# Documentation-derived rules of the Receipt aggregate, OpenReceipt, ReceiveLine,
# CloseReceipt and the events they publish.
#
# Derived from warehouse-docs contexts/inbound-receiving: aggregate-design-canvas
# (Receipt: enforced invariants, state transitions, corrective policies),
# use-cases (OpenReceipt, ReceiveLine, CloseReceipt, GetReceipt, cross-cutting
# optimistic concurrency), domain-events (payloads, keys, envelope),
# troubleshooting (receipt-*, line-not-on-asn, version-mismatch rows).
# The scenario clock starts at 2026-10-09T07:00:00Z. ASN-1 announces
# SKU-1 x 40 on line 1 and SKU-2 x 5 on line 2.

Feature: Receipt rules from the documentation
  A receipt counts Good and Damaged units per line, accepts over-receipt,
  reports every difference when it closes and is never corrected.

  Background:
    Given the ASN "ASN-1" is registered

  @bdd
  Scenario Outline: The discrepancy kinds follow good + damaged against the expected 40
    Given a walk-in receipt is open for the ASN "ASN-1"
    And line 1 is counted <good> Good and <damaged> Damaged
    And line 2 is counted 5 Good and 0 Damaged
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the response status is 200
    And the response discrepancies are "<kinds>"

    Examples:
      | good | damaged | kinds              |
      | 40   | 0       | none               |
      | 39   | 0       | Short@1            |
      | 41   | 0       | Over@1             |
      | 38   | 2       | Damaged@1          |
      | 37   | 2       | Short@1, Damaged@1 |
      | 40   | 1       | Over@1, Damaged@1  |
      | 0    | 40      | Damaged@1          |
      | 0    | 0       | Short@1            |

  @bdd
  Scenario: Closing a receipt nothing was received on is allowed and every line is short
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the response status is 200
    And the response field "state" is "Closed"
    And the response discrepancies are "Short@1, Short@2"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Closed"

  @bdd
  Scenario: An open receipt reports the discrepancies as they stand now, without publishing anything
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 30 units of line 1 were received as "Good"
    When I GET "/receipts/{receipt}"
    Then the response field "state" is "Open"
    And the response discrepancies are "Short@1, Short@2"
    And the response field "closedAt" is absent
    And the outbox holds 0 "receipt.ReceiptClosed" events

  @bdd
  Scenario: Over-receipt is accepted at the dock and only reported at close
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":45,"condition":"Good"}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "45"
    And the response field "lines.0.expectedQty" is "40"
    Given line 2 is counted 5 Good and 0 Damaged
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the response discrepancies are "Over@1"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.expected_qty" equal to "40"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.received_qty" equal to "45"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.damaged_qty" equal to "0"

  @bdd
  Scenario Outline: A line's running total, good plus damaged, stays within 2147483647
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 2147483000 units of line 1 were received as "Good"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":<qty>,"condition":"<condition>"}'
    Then the response status is <status>

    Examples:
      | condition | qty | status |
      | Damaged   | 647 | 201    |
      | Good      | 647 | 201    |
      | Damaged   | 648 | 400    |
      | Good      | 648 | 400    |

  @bdd
  Scenario: A refused quantity leaves the line total where it was
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 2147483647 units of line 1 were received as "Good"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":1,"condition":"Good"}'
    Then the response status is 400
    And the problem type is "invalid-quantity"
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "2147483647"
    And the response field "version" is "2"

  @bdd
  Scenario Outline: A single receive of 1..2147483647 units is accepted and anything else refused
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":1,"quantity":<qty>,"condition":"Good"}'
    Then the response status is <status>

    Examples:
      | qty        | status |
      | 1          | 201    |
      | 2147483647 | 201    |
      | 2147483648 | 400    |
      | -5         | 400    |

  @bdd
  Scenario Outline: A refused receive changes nothing: no count, no version, no event
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/lines" with body '<body>'
    Then the response status is <status>
    And the problem type is "<slug>"
    When I GET "/receipts/{receipt}"
    Then the response field "version" is "1"
    And the response field "lines.0.receivedGood" is "0"
    And the response field "lines.0.receivedDamaged" is "0"
    And the outbox event types are "asn.ASNRegistered, receipt.ReceiptOpened"

    Examples:
      | body                                              | status | slug              |
      | {"lineNo":1,"quantity":3,"condition":"good"}      | 400    | invalid-condition |
      | {"lineNo":1,"quantity":0,"condition":"Good"}      | 400    | invalid-quantity  |
      | {"lineNo":0,"quantity":3,"condition":"Good"}      | 422    | line-not-on-asn   |
      | {"lineNo":3,"quantity":3,"condition":"Good"}      | 422    | line-not-on-asn   |

  @bdd
  Scenario: A stale If-Match on a receive is refused, a current one counts the units
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I receive with If-Match "5" the body '{"lineNo":1,"quantity":6,"condition":"Good"}'
    Then the response status is 412
    And the problem type is "version-mismatch"
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "0"
    When I receive with If-Match "1" the body '{"lineNo":1,"quantity":6,"condition":"Good"}'
    Then the response status is 201
    And the response field "lines.0.receivedGood" is "6"
    And the ETag is "2"

  @bdd
  Scenario: A stale If-Match on a close is refused and the receipt stays open
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I close the receipt with If-Match "9"
    Then the response status is 412
    And the problem type is "version-mismatch"
    When I GET "/receipts/{receipt}"
    Then the response field "state" is "Open"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Receiving"
    When I close the receipt with If-Match "1"
    Then the response status is 200
    And the response field "state" is "Closed"

  @bdd
  Scenario: A receipt is opened and closed with its own version, one per accepted change
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 10 units of line 1 were received as "Good"
    And 2 units of line 1 were received as "Damaged"
    When the receipt is closed
    And I GET "/receipts/{receipt}"
    Then the response field "version" is "4"
    And the response field "state" is "Closed"

  @bdd
  Scenario: A refused open on an unknown appointment leaves the ASN registered
    When I POST "/receipts" with body '{"asnNumber":"ASN-1","appointmentId":"appt-00000000-0000-4000-8000-0000000000ff"}'
    Then the response status is 422
    And the problem type is "unknown-appointment"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Registered"
    And the ETag is "1"
    And the outbox event types are "asn.ASNRegistered"

  @bdd
  Scenario: A refused open on a cancelled appointment leaves the ASN registered
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And dock-scheduling cancels the appointment
    When I POST "/receipts" with body '{"asnNumber":"ASN-1","appointmentId":"{appointment}"}'
    Then the response status is 409
    And the problem type is "appointment-cancelled"
    When I GET "/asns/ASN-1"
    Then the response field "state" is "Registered"
    And the outbox event types are "asn.ASNRegistered"

  @bdd
  Scenario: A refused second open leaves the ASN and the first receipt as they were
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 5 units of line 1 were received as "Good"
    When I POST "/receipts" with body '{"asnNumber":"ASN-1"}'
    Then the response status is 409
    And the problem type is "receipt-already-open"
    When I GET "/asns/ASN-1"
    Then the ETag is "2"
    And the outbox holds 1 "receipt.ReceiptOpened" event
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "5"
    And the response field "state" is "Open"

  @bdd
  Scenario: One appointment that covers two ASNs opens a receipt per ASN at the same door
    Given the ASN "ASN-2" is registered
    And an appointment at door "DOOR-3" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASNs "ASN-1, ASN-2"
    When I POST "/receipts" with body '{"asnNumber":"ASN-1","appointmentId":"{appointment}"}'
    Then the response status is 201
    And the response field "doorCode" is "DOOR-3"
    When I POST "/receipts" with body '{"asnNumber":"ASN-2","appointmentId":"{appointment}"}'
    Then the response status is 201
    And the response field "doorCode" is "DOOR-3"
    And the response field "asnNumber" is "ASN-2"

  @bdd
  Scenario: ReceiptOpened of a walk-in carries no appointment and no door
    Given a walk-in receipt is open for the ASN "ASN-1"
    Then the response field "appointmentId" is absent
    And the response field "doorCode" is absent
    And the last "receipt.ReceiptOpened" event has "subject" equal to "{receipt}"
    And the last "receipt.ReceiptOpened" event has "data.receipt_id" equal to "{receipt}"
    And the last "receipt.ReceiptOpened" event has "data.asn_number" equal to "ASN-1"
    And the last "receipt.ReceiptOpened" event has "data.opened_at" equal to "2026-10-09T07:00:00Z"
    And the last "receipt.ReceiptOpened" event has no "data.appointment_id"
    And the last "receipt.ReceiptOpened" event has no "data.door_code"

  @bdd
  Scenario: ReceiptOpened of an appointment receipt carries the appointment and its door
    Given an appointment at door "DOOR-7" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And a receipt is open for the ASN "ASN-1" on the appointment
    Then the last "receipt.ReceiptOpened" event has "data.appointment_id" equal to "{appointment}"
    And the last "receipt.ReceiptOpened" event has "data.door_code" equal to "DOOR-7"

  @bdd
  Scenario Outline: ReceiptLineReceived hands over every count with its condition, at the moment it was counted
    Given a walk-in receipt is open for the ASN "ASN-1"
    And the clock is at "2026-10-09T09:15:00Z"
    When I POST "/receipts/{receipt}/lines" with body '{"lineNo":2,"quantity":12,"condition":"<condition>"}'
    Then the response status is 201
    And the last "receipt.ReceiptLineReceived" event has "subject" equal to "{receipt}"
    And the last "receipt.ReceiptLineReceived" event has "data.receipt_id" equal to "{receipt}"
    And the last "receipt.ReceiptLineReceived" event has "data.asn_number" equal to "ASN-1"
    And the last "receipt.ReceiptLineReceived" event has "data.line_no" equal to "2"
    And the last "receipt.ReceiptLineReceived" event has "data.sku" equal to "SKU-2"
    And the last "receipt.ReceiptLineReceived" event has "data.quantity" equal to "12"
    And the last "receipt.ReceiptLineReceived" event has "data.condition" equal to "<condition>"
    And the last "receipt.ReceiptLineReceived" event has "data.received_at" equal to "2026-10-09T09:15:00Z"

    Examples:
      | condition |
      | Good      |
      | Damaged   |

  @bdd
  Scenario: ReceiptClosed lists each discrepancy with the expected, received and damaged quantity
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 30 units of line 1 were received as "Good"
    And 4 units of line 1 were received as "Damaged"
    And 7 units of line 2 were received as "Good"
    And the clock is at "2026-10-09T11:30:00Z"
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the last "receipt.ReceiptClosed" event has "subject" equal to "{receipt}"
    And the last "receipt.ReceiptClosed" event has "data.closed_at" equal to "2026-10-09T11:30:00Z"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.kind" equal to "Short"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.line_no" equal to "1"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.sku" equal to "SKU-1"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.expected_qty" equal to "40"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.received_qty" equal to "34"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.0.damaged_qty" equal to "4"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.1.kind" equal to "Damaged"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.2.kind" equal to "Over"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.2.line_no" equal to "2"
    And the last "receipt.ReceiptClosed" event has "data.discrepancies.2.received_qty" equal to "7"

  @bdd
  Scenario: ReceiptClosed of a matching receipt carries an empty discrepancies array, not null
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 40 units of line 1 were received as "Good"
    And 5 units of line 2 were received as "Good"
    When I POST "/receipts/{receipt}/closures" with body ''
    Then the last "receipt.ReceiptClosed" event has "data.discrepancies" equal to ""
    And the last "receipt.ReceiptClosed" event has no "data.discrepancies.0"

  @bdd
  Scenario: Every event of one ASN is keyed by its number, and receipt events are subject to the receipt
    Given a walk-in receipt is open for the ASN "ASN-1"
    And 40 units of line 1 were received as "Good"
    And the receipt is closed
    Then every outbox event is keyed by the ASN "ASN-1"
    And the last "asn.ASNRegistered" event has "subject" equal to "ASN-1"
    And the last "receipt.ReceiptLineReceived" event has "subject" equal to "{receipt}"
    And the last "receipt.ReceiptClosed" event has "subject" equal to "{receipt}"

  @bdd
  Scenario: Events are CloudEvents 1.0 from /warehouse/inbound-receiving with a versioned dataschema
    Given a walk-in receipt is open for the ASN "ASN-1"
    And the receipt is closed
    Then the last "receipt.ReceiptClosed" event has "specversion" equal to "1.0"
    And the last "receipt.ReceiptClosed" event has "source" equal to "/warehouse/inbound-receiving"
    And the last "receipt.ReceiptClosed" event has "type" equal to "com.warehouse.wms.inbound-receiving.receipt.ReceiptClosed"
    And the last "receipt.ReceiptClosed" event has "datacontenttype" equal to "application/json"
    And the last "receipt.ReceiptClosed" event has "dataschema" equal to "urn:warehouse:inbound-receiving:events:ReceiptClosed:v1"
    And the last "receipt.ReceiptOpened" event has "dataschema" equal to "urn:warehouse:inbound-receiving:events:ReceiptOpened:v1"

  @bdd
  Scenario Outline: A receipt that does not exist, or whose id is malformed, is refused with its own problem
    When I GET "<path>"
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | path                                            | status | slug               |
      | /receipts/rcpt-00000000-0000-4000-8000-000000000001 | 404    | receipt-not-found  |
      | /receipts/not-a-receipt                         | 400    | invalid-receipt-id |

  @bdd
  Scenario Outline: Receiving into or closing a receipt that does not exist
    When I POST "<path>" with body '<body>'
    Then the response status is <status>
    And the problem type is "<slug>"
    And the outbox event types are "asn.ASNRegistered"

    Examples:
      | path                                                         | body                                  | status | slug               |
      | /receipts/rcpt-00000000-0000-4000-8000-000000000001/lines    | {"lineNo":1,"quantity":1,"condition":"Good"} | 404    | receipt-not-found  |
      | /receipts/rcpt-00000000-0000-4000-8000-000000000001/closures |                                       | 404    | receipt-not-found  |
      | /receipts/not-a-receipt/lines                                | {"lineNo":1,"quantity":1,"condition":"Good"} | 400    | invalid-receipt-id |

  @bdd
  Scenario: A scan on a closed receipt is refused and books nothing
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And a walk-in receipt is open for the ASN "ASN-1"
    And the receipt is closed
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"EACH_GTIN","id":"10001234567892","qty":3}'
    Then the response status is 409
    And the problem type is "receipt-closed"
    And the outbox holds 0 "receipt.ReceiptLineReceived" events

  @bdd
  Scenario Outline: A case scan books 1..1000 cases and an absent quantity is one case
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"20001234567851"<qty>}'
    Then the response status is <status>
    When I GET "/receipts/{receipt}"
    Then the response field "lines.0.receivedGood" is "<booked>"

    Examples:
      | qty          | status | booked |
      |              | 201    | 20     |
      | ,"qty":1000  | 201    | 20000  |
      | ,"qty":1001  | 400    | 0      |
      | ,"qty":-1    | 400    | 0      |
