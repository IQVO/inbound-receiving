# Listing receipts, appointments and inbound dock doors.
#
# Derived from:
#   - apis/openapi.yaml GET /receipts: ascending receipt-id order, a page at a
#     time (limit 1..500, default 100, opaque cursor), narrowed by asnNumber
#     and/or state; 400 for a malformed query
#   - apis/openapi.yaml GET /appointments: ascending windowStart order (ties by
#     id), narrowed by door and state by equality and by from/to to the
#     appointments whose window overlaps [from, to); 400 for a malformed query
#   - apis/openapi.yaml GET /docks and ADR 0003: the inbound dock doors known
#     from facility-layout events (role Dock, dockFlow Inbound or Both, minus
#     decommissioned) in ascending door-code order, with the current
#     DOCK_DOOR_MODE
# The scenario clock starts at 2026-10-09T07:00:00Z.

Feature: Listing receipts, appointments and dock doors
  As a dock supervisor
  I want to browse what is arriving and which doors exist
  So that I can plan the day without knowing every id

  Background:
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered
    And the ASN "ASN-3" is registered

  @bdd
  Scenario: Receipts are listed in ascending receipt-id order
    Given a walk-in receipt is open for the ASN "ASN-1"
    And a walk-in receipt is open for the ASN "ASN-2"
    And a walk-in receipt is open for the ASN "ASN-3"
    When I list "/receipts" with query "limit=500"
    Then the response status is 200
    And the list has 3 items
    And the listed "receiptId" values are in ascending order

  @bdd
  Scenario: Receipts can be narrowed to one ASN
    Given a walk-in receipt is open for the ASN "ASN-1"
    And a walk-in receipt is open for the ASN "ASN-2"
    When I list "/receipts" with query "asnNumber=ASN-2"
    Then the response status is 200
    And the listed "asnNumber" values are "ASN-2"

  @bdd
  Scenario: Receipts can be narrowed to one state
    Given a walk-in receipt is open for the ASN "ASN-1"
    And the receipt is closed
    And a walk-in receipt is open for the ASN "ASN-2"
    When I list "/receipts" with query "state=Closed"
    Then the listed "asnNumber" values are "ASN-1"
    When I list "/receipts" with query "state=Open"
    Then the listed "asnNumber" values are "ASN-2"

  @bdd
  Scenario: Receipts page by cursor without repeating or skipping any
    Given a walk-in receipt is open for the ASN "ASN-1"
    And a walk-in receipt is open for the ASN "ASN-2"
    And a walk-in receipt is open for the ASN "ASN-3"
    When I list "/receipts" with query "limit=2"
    Then the list has 2 items
    And the response field "nextCursor" is present
    When I list the next page
    Then the list has 1 item
    And the response field "nextCursor" is absent

  @bdd
  Scenario: A page that exactly fills the limit on the last page has no next cursor
    Given a walk-in receipt is open for the ASN "ASN-1"
    And a walk-in receipt is open for the ASN "ASN-2"
    When I list "/receipts" with query "limit=2"
    Then the list has 2 items
    And the response field "nextCursor" is absent

  @bdd
  Scenario: An ASN page that exactly fills the limit has no next cursor
    When I list "/asns" with query "limit=3"
    Then the list has 3 items
    And the response field "nextCursor" is absent

  @bdd
  Scenario: A receipt filter that matches nothing lists an empty page
    Given a walk-in receipt is open for the ASN "ASN-1"
    When I list "/receipts" with query "asnNumber=ASN-3"
    Then the response status is 200
    And the list has 0 items
    And the response field "nextCursor" is absent

  @bdd
  Scenario Outline: A malformed receipt query is a bad request
    When I list "/receipts" with query "<query>"
    Then the response status is 400

    Examples:
      | query           |
      | limit=0         |
      | limit=-1        |
      | limit=501       |
      | limit=many      |
      | state=Maybe     |
      | cursor=!!!      |

  @bdd
  Scenario: Appointments are listed by window start, earliest first
    Given an appointment at door "DOOR-1" from "2026-10-09T12:00:00Z" to "2026-10-09T13:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-2"
    And an appointment at door "DOOR-3" from "2026-10-09T10:00:00Z" to "2026-10-09T11:00:00Z" covers the ASN "ASN-3"
    When I list "/appointments" with query "limit=100"
    Then the response status is 200
    And the listed "doorCode" values are "DOOR-2, DOOR-3, DOOR-1"

  @bdd
  Scenario: Appointments overlapping a time range are listed, bounds half-open
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T10:00:00Z" to "2026-10-09T11:00:00Z" covers the ASN "ASN-2"
    And an appointment at door "DOOR-3" from "2026-10-09T12:00:00Z" to "2026-10-09T13:00:00Z" covers the ASN "ASN-3"
    When I list "/appointments" with query "from=2026-10-09T09:00:00Z&to=2026-10-09T12:00:00Z"
    Then the listed "doorCode" values are "DOOR-2"
    When I list "/appointments" with query "from=2026-10-09T08:30:00Z&to=2026-10-09T10:30:00Z"
    Then the listed "doorCode" values are "DOOR-1, DOOR-2"

  @bdd
  Scenario: Appointments can be narrowed by state
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T10:00:00Z" to "2026-10-09T11:00:00Z" covers the ASN "ASN-2"
    And I POST "/appointments/{appointment}/cancellations" with body ''
    When I list "/appointments" with query "state=Cancelled"
    Then the listed "doorCode" values are "DOOR-2"
    When I list "/appointments" with query "state=Booked"
    Then the listed "doorCode" values are "DOOR-1"

  @bdd
  Scenario: Appointments with the same window start are ordered by id and page without loss
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-2"
    And an appointment at door "DOOR-3" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-3"
    When I list "/appointments" with query "limit=2"
    Then the list has 2 items
    And the listed "appointmentId" values are in ascending order
    And the response field "nextCursor" is present
    When I list the next page
    Then the list has 1 item
    And the response field "nextCursor" is absent

  @bdd
  Scenario: An appointment page that exactly fills the limit has no next cursor
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T09:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T10:00:00Z" to "2026-10-09T11:00:00Z" covers the ASN "ASN-2"
    When I list "/appointments" with query "limit=2"
    Then the list has 2 items
    And the response field "nextCursor" is absent

  @bdd
  Scenario Outline: A malformed appointment query is a bad request
    When I list "/appointments" with query "<query>"
    Then the response status is 400

    Examples:
      | query                       |
      | limit=0                     |
      | limit=501                   |
      | state=Maybe                 |
      | from=yesterday              |
      | to=2026-10-09               |
      | cursor=!!!                  |
      | door=has%20space            |

  @bdd
  Scenario: In permissive mode the dock list is empty and informational
    When I GET "/docks"
    Then the response status is 200
    And the response field "mode" is "permissive"
    And the list has 0 items

  @bdd
  Scenario: In kafka mode the dock list is the known inbound doors in code order
    Given the dock doors are enforced and know "DOCK-B, DOCK-A, DOCK-C"
    When I GET "/docks"
    Then the response status is 200
    And the response field "mode" is "kafka"
    And the listed "doorCode" values are "DOCK-A, DOCK-B, DOCK-C"
    And the listed "dockFlow" values are "Inbound, Inbound, Inbound"

  @bdd
  Scenario: Opening a receipt against an appointment that does not exist is refused
    When I POST "/receipts" with key "k-unknown-appt" and body '{"asnNumber":"ASN-1","appointmentId":"appt-00000000-0000-4000-8000-0000000000ff"}'
    Then the response status is 422
    And the problem type is "unknown-appointment"
