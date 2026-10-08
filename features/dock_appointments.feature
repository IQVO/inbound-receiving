Feature: Book and manage dock appointments
  A carrier gets a door window covering registered ASNs. A door may not be
  booked twice in overlapping windows, and check-in is only allowed around the
  start of the window. The scenario clock starts at 2026-10-09T07:00:00Z.

  Background:
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered

  Scenario: Booking a door window
    When I POST "/appointments" with body '{"doorCode":"DOOR-1","carrier":"ACME Freight","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T10:00:00Z","asnNumbers":["ASN-1"]}'
    Then the response status is 201
    And the response field "state" is "Booked"
    And the response field "version" is "1"
    And the response field "asnNumbers" is "ASN-1"
    And the ETag is "1"
    And the outbox event types are "asn.ASNRegistered, asn.ASNRegistered, dockappointment.DockAppointmentBooked"

  Scenario: Reading a booked appointment
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I GET "/appointments/{appointment}"
    Then the response status is 200
    And the response field "doorCode" is "DOOR-1"

  Scenario: Reading an unknown appointment
    When I GET "/appointments/appt-00000000-0000-4000-8000-0000000000ff"
    Then the response status is 404
    And the problem type is "appointment-not-found"

  Scenario: An overlapping window on the same door is refused
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I POST "/appointments" with body '{"doorCode":"DOOR-1","carrier":"Other","windowStart":"2026-10-09T09:00:00Z","windowEnd":"2026-10-09T11:00:00Z","asnNumbers":["ASN-2"]}'
    Then the response status is 409
    And the problem type is "door-window-overlap"

  Scenario: A back-to-back window and another door are both fine
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I POST "/appointments" with body '{"doorCode":"DOOR-1","carrier":"Other","windowStart":"2026-10-09T10:00:00Z","windowEnd":"2026-10-09T12:00:00Z","asnNumbers":["ASN-2"]}'
    Then the response status is 201
    When I POST "/appointments" with body '{"doorCode":"DOOR-2","carrier":"Other","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T10:00:00Z","asnNumbers":["ASN-2"]}'
    Then the response status is 201

  Scenario: A cancelled appointment frees its window
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I POST "/appointments/{appointment}/cancel" with body '{"reason":"Carrier delayed"}'
    Then the response status is 200
    And the response field "state" is "Cancelled"
    When I POST "/appointments" with body '{"doorCode":"DOOR-1","carrier":"Other","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T10:00:00Z","asnNumbers":["ASN-2"]}'
    Then the response status is 201

  Scenario: A booked appointment cannot be cancelled twice
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I POST "/appointments/{appointment}/cancel" with body ''
    And I POST "/appointments/{appointment}/cancel" with body ''
    Then the response status is 409
    And the problem type is "appointment-not-booked"

  Scenario Outline: Invalid bookings are rejected
    When I POST "/appointments" with body '<body>'
    Then the response status is <status>
    And the problem type is "<slug>"

    Examples:
      | body                                                                                                                              | status | slug                      |
      | {"doorCode":"a b","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-1"]}   | 400    | invalid-door-code         |
      | {"doorCode":"D","carrier":" ","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-1"]}     | 400    | invalid-carrier           |
      | {"doorCode":"D","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T08:00:00Z","asnNumbers":["ASN-1"]}     | 400    | invalid-window            |
      | {"doorCode":"D","carrier":"C","windowStart":"2026-10-09T05:00:00Z","windowEnd":"2026-10-09T06:00:00Z","asnNumbers":["ASN-1"]}     | 400    | window-in-past            |
      | {"doorCode":"D","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":[]}            | 400    | appointment-requires-asns |
      | {"doorCode":"D","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-1","ASN-1"]} | 400 | duplicate-asn-number      |
      | {"doorCode":"D","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-404"]}   | 422    | unknown-asn               |

  Scenario: A cancelled ASN cannot be booked
    Given the ASN "ASN-3" is registered
    And the ASN "ASN-3" is cancelled
    When I POST "/appointments" with body '{"doorCode":"DOOR-1","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-3"]}'
    Then the response status is 409
    And the problem type is "asn-not-receivable"

  Scenario: An unknown dock door is refused when the door registry is enforced
    Given the dock doors are enforced and know "WH1-DOCK-IN-01"
    When I POST "/appointments" with body '{"doorCode":"WH1-STOR-01","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-1"]}'
    Then the response status is 422
    And the problem type is "unknown-dock-door"
    When I POST "/appointments" with body '{"doorCode":"WH1-DOCK-IN-01","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T09:00:00Z","asnNumbers":["ASN-1"]}'
    Then the response status is 201

  Scenario: Check-in before the window opens is refused
    Given an appointment at door "DOOR-1" from "2026-10-09T10:00:00Z" to "2026-10-09T12:00:00Z" covers the ASN "ASN-1"
    When I POST "/appointments/{appointment}/check-in" with body ''
    Then the response status is 409
    And the problem type is "outside-check-in-window"

  Scenario: Check-in at the start of the window
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And the clock is at "2026-10-09T08:00:00Z"
    When I POST "/appointments/{appointment}/check-in" with If-Match "1" and body ''
    Then the response status is 200
    And the response field "state" is "CheckedIn"
    And the ETag is "2"

  Scenario: Listing appointments of a door
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-2"
    When I list "/appointments" with query "door=DOOR-2&state=Booked"
    Then the listed "doorCode" values are "DOOR-2"
