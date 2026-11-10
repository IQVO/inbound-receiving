Feature: Dock-to-stock KPI (ADR 0021)
  Dock-to-stock is the time from the carrier's physical arrival at the gate
  (yard-management's VisitCheckedIn) to the stow of the last Good carton
  (inventory-storage's ContainerStowed, joined to the receipt's cartons by SSCC
  = LPN). This context keeps a local copy of each fact and raises
  ReceiptStowCompleted exactly once, for a closed receipt opened from an
  appointment, when every Good carton is stowed and the arrival is known,
  whatever order the facts and the close arrive in.
  The scenario clock starts at 2026-10-09T07:00:00Z.

  Background:
    Given the packaging view knows SKU "SKU-1" with each GTIN "10001234567892" and case GTIN "20001234567851" of 20
    And the carton ASN "ASN-C" is registered
    And an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-C"
    And a receipt is open for the ASN "ASN-C" on the appointment
    And I scanned the carton "003456789012345670"
    And I scanned the carton "003456789012345687"

  Scenario: The fact is raised when the last Good carton is stowed after the close
    Given the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    And the receipt is closed
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events
    When inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    Then the consumer reports "applied"
    And the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 7200 seconds over 2 cartons

  Scenario: The last stow of a Good carton sets the end, not the first
    Given the receipt is closed
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:30:00Z"
    And inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    When the yard checks the appointment's trailer in at "2026-10-09T05:30:00Z"
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 7200 seconds over 2 cartons

  Scenario: The stows may come before the close and the arrival
    Given inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events
    When the receipt is closed
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 7200 seconds over 2 cartons

  Scenario: A carton that is not stowed yet holds the fact back
    Given the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events

  Scenario: Without the gate arrival there is no fact
    Given the receipt is closed
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events

  Scenario: A receipt that is still open has no fact yet
    Given the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    When inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    Then the outbox holds 0 ReceiptStowCompleted events

  Scenario: A redelivered fact raises nothing twice
    Given the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    And inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    When inventory redelivers the last stow
    Then the consumer reports "duplicate"
    When the yard redelivers the last arrival
    Then the consumer reports "duplicate"
    And the outbox holds 1 ReceiptStowCompleted event

  Scenario: A later move of a carton is not a new stow
    Given the receipt is closed
    And the yard checks the appointment's trailer in at "2026-10-09T05:00:00Z"
    And inventory stows the carton "003456789012345670" at "2026-10-09T06:00:00Z"
    And inventory stows the carton "003456789012345687" at "2026-10-09T07:00:00Z"
    When inventory stows the carton "003456789012345687" at "2026-10-09T09:00:00Z"
    Then the outbox holds 1 ReceiptStowCompleted event
    And the dock-to-stock fact is 7200 seconds over 2 cartons

  Scenario: A walk-in visit has no appointment to join and is ignored
    Given the yard checks a walk-in trailer in at "2026-10-09T05:00:00Z"
    Then the consumer reports "ignored"
    And the outbox holds 0 ReceiptStowCompleted events
