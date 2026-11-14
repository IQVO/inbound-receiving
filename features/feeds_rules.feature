# Documentation-derived rules of the local copies this context keeps from
# other contexts' events, and of the dock-to-stock fact built on them.
#
# Derived from warehouse-docs contexts/inbound-receiving: use-cases (event-driven
# use cases), domain-events (Consumed: version-guarded appointment copy, pack
# levels, VisitCheckedIn earliest arrival, ContainerStowed earliest stow),
# eventstorming (policies P6-P8) and the Receipt canvas.
# The scenario clock starts at 2026-10-09T07:00:00Z.

Feature: Local copies and dock-to-stock rules from the documentation
  The appointments, pack levels, arrivals and stows this context copies are
  guarded by the event version or by the earliest time, claimed once per
  CloudEvents id, and never publish an event of their own.

  @bdd
  Scenario Outline: A follow-up announcement changes the appointment copy only when its version is newer
    Given the ASN "ASN-1" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And the yard reassigns the appointment to door "DOOR-2"
    When dock-scheduling sends a <kind> of the appointment at version <version>
    Then the consumer reports "<outcome>"
    When I GET "/appointments/{appointment}"
    Then the response field "doorCode" is "<door>"
    And the response field "version" is "<copyVersion>"
    And the outbox event types are "asn.ASNRegistered"

    Examples:
      | kind         | version | outcome | door     | copyVersion |
      | reassignment | 1       | ignored | DOOR-2   | 2           |
      | reassignment | 2       | ignored | DOOR-2   | 2           |
      | reassignment | 3       | applied | DOOR-NEW | 3           |
      | reschedule   | 1       | ignored | DOOR-2   | 2           |
      | reschedule   | 2       | ignored | DOOR-2   | 2           |
      | reschedule   | 3       | applied | DOOR-NEW | 3           |

  @bdd
  Scenario Outline: A redelivered follow-up announcement is a duplicate and changes nothing
    Given the ASN "ASN-1" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And dock-scheduling sends a <kind> of the appointment at version 2
    When the last follow-up announcement is redelivered
    Then the consumer reports "duplicate"
    When I GET "/appointments/{appointment}"
    Then the response field "doorCode" is "DOOR-NEW"
    And the response field "version" is "2"

    Examples:
      | kind         |
      | reassignment |
      | reschedule   |

  @bdd
  Scenario: A cancellation that is not newer than the copy is ignored
    Given the ASN "ASN-1" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And the yard reassigns the appointment to door "DOOR-2"
    When dock-scheduling cancels the appointment at version 2
    Then the consumer reports "ignored"
    When I GET "/appointments/{appointment}"
    Then the response field "state" is "Booked"
    When dock-scheduling cancels the appointment at version 3
    Then the consumer reports "applied"
    When I GET "/appointments/{appointment}"
    Then the response field "state" is "Cancelled"
    And the response field "version" is "3"

  @bdd
  Scenario: A cancellation of an appointment this service never copied is ignored
    When dock-scheduling cancels an appointment this service never copied
    Then the consumer reports "ignored"

  @bdd
  Scenario Outline: A booking this context would reject is refused before anything is claimed
    When dock-scheduling announces an appointment with direction "<direction>" at door "<door>" from "<from>" to "<to>" for the ASNs "<asns>" at version <version>
    Then the consumer refuses the event as invalid

    Examples:
      | direction | door   | from                 | to                   | asns  | version |
      | SIDEWAYS  | DOOR-1 | 2026-10-09T08:00:00Z | 2026-10-09T10:00:00Z | ASN-1 | 1       |
      | INBOUND   | DOOR-1 | 2026-10-09T08:00:00Z | 2026-10-09T10:00:00Z | ASN-1 | 0       |
      | INBOUND   | DOOR-1 | 2026-10-09T08:00:00Z | 2026-10-09T10:00:00Z |       | 1       |
      | INBOUND   | DOOR 1 | 2026-10-09T08:00:00Z | 2026-10-09T10:00:00Z | ASN-1 | 1       |
      | INBOUND   | DOOR-1 | 2026-10-09T10:00:00Z | 2026-10-09T08:00:00Z | ASN-1 | 1       |

  @bdd
  Scenario: A pack-level declaration that is not newer than the view's is ignored
    Given the ASN "ASN-1" is registered
    And the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    When product-master declares the case GTIN "20001234567851" of SKU "SKU-1" as 12 eaches of "10001234567892" at version 1
    Then the consumer reports "ignored"
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"20001234567851"}'
    Then the response field "lines.0.receivedGood" is "20"

  @bdd
  Scenario: A newer pack-level declaration changes what a case holds from then on
    Given the ASN "ASN-1" is registered
    And the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    When product-master declares the case GTIN "20001234567851" of SKU "SKU-1" as 12 eaches of "10001234567892" at version 2
    Then the consumer reports "applied"
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I POST "/receipts/{receipt}/scans" with body '{"kind":"CASE_GTIN","id":"20001234567851","qty":2}'
    Then the response field "lines.0.receivedGood" is "24"

  @bdd
  Scenario: A redelivered pack-level declaration is a duplicate
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    When product-master declares the case GTIN "20001234567851" of SKU "SKU-1" as 12 eaches of "10001234567892" at version 2
    And the last follow-up announcement is redelivered
    Then the consumer reports "duplicate"

  @bdd
  Scenario Outline: A pack-level declaration this context would reject is refused
    When product-master declares the case GTIN "<gtin>" of SKU "SKU-1" as <eaches> eaches of "<each>" at version <version>
    Then the consumer refuses the event as invalid

    Examples:
      | gtin           | eaches | each           | version |
      | 12345          | 12     | 10001234567892 | 1       |
      | 20001234567851 | 0      | 10001234567892 | 1       |
      | 20001234567851 | -5     | 10001234567892 | 1       |
      | 20001234567851 | 12     | 10001234567892 | 0       |
      | 20001234567851 | 12     | not-a-gtin     | 1       |

  @bdd
  Scenario: Retiring a pack level follows the view: unseen is ignored, seen is applied, redelivery is a duplicate
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    When product-master retires the pack level "6291041500213" at version 1
    Then the consumer reports "ignored"
    When product-master retires the pack level "20001234567851" at version 1
    Then the consumer reports "applied"
    When the last follow-up announcement is redelivered
    Then the consumer reports "duplicate"

  @bdd
  Scenario: A retirement older than the declared version is ignored, and an unusable GTIN is refused
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And product-master declares the case GTIN "20001234567851" of SKU "SKU-1" as 12 eaches of "10001234567892" at version 3
    When product-master retires the pack level "20001234567851" at version 2
    Then the consumer reports "ignored"
    When product-master retires the pack level "not-a-gtin" at version 4
    Then the consumer refuses the event as invalid

  # Dock-to-stock (ADR 0021): the fact needs a Good carton, the earliest gate arrival and the
  # stow of every Good carton. ASN-C holds two cartons, 003456789012345670 and ...687.

  @bdd
  Scenario: A Damaged carton is not expected to be stowed, so the Good carton alone completes the fact
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I scanned the carton "003456789012345670"
    And I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687","condition":"Damaged"}'
    And the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 3600 seconds over 1 carton

  @bdd
  Scenario: A receipt whose cartons are all Damaged never completes a dock-to-stock fact
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345670","condition":"Damaged"}'
    And I POST "/receipts/{receipt}/scans" with body '{"kind":"SSCC","id":"003456789012345687","condition":"Damaged"}'
    And the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events

  @bdd
  Scenario: A manifest carton that never arrived does not hold the fact back
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I scanned the carton "003456789012345670"
    And the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:30:00Z"
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 5400 seconds over 1 carton

  @bdd
  Scenario Outline: The earliest gate arrival of an appointment is the one that counts, whatever the order
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I scanned the carton "003456789012345670"
    And I scanned the carton "003456789012345687"
    And inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    When the yard checks the appointment's trailer in at "<first>"
    And the yard checks the appointment's trailer in at "<second>"
    Then the consumer reports "<outcome>"
    When the receipt is closed
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is <seconds> seconds over 2 cartons

    Examples:
      | first                | second               | outcome | seconds |
      | 2026-10-09T06:00:00Z | 2026-10-09T05:00:00Z | applied | 7200    |
      | 2026-10-09T05:00:00Z | 2026-10-09T06:00:00Z | ignored | 7200    |

  @bdd
  Scenario: A stow recorded before the gate arrival reads as zero seconds, never negative
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I scanned the carton "003456789012345670"
    And I scanned the carton "003456789012345687"
    And the receipt is closed
    And inventory stows the carton "003456789012345670" at "2026-10-09T07:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:30:00Z"
    When the yard checks the appointment's trailer in at "2026-10-09T08:00:00Z"
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 0 seconds over 2 cartons

  @bdd
  Scenario: A walk-in receipt has no appointment to join, so it never completes the fact
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And a walk-in receipt is open for the ASN "ASN-C"
    And I scanned the carton "003456789012345670"
    And I scanned the carton "003456789012345687"
    And the receipt is closed
    When the yard checks a walk-in trailer in at "2026-10-09T05:00:00Z"
    And inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events
