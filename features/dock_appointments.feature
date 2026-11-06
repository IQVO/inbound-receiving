Feature: Dock appointments moved to dock-scheduling
  The DockAppointment moved to dock-scheduling (ADR 0019, plan YD-0002). Booking,
  check-in and cancellation are answered 410 here; the appointments this service
  still shows (GET /appointments, deprecated) are a local copy fed by
  dock-scheduling's DockAppointmentBooked and DockAppointmentCancelled events,
  version-guarded, and applying them raises no event of this service.
  The scenario clock starts at 2026-10-09T07:00:00Z.

  Background:
    Given the ASN "ASN-1" is registered
    And the ASN "ASN-2" is registered

  Scenario Outline: A former appointment write answers 410 and names dock-scheduling
    When I POST "<path>" with body '<body>'
    Then the response status is 410
    And the problem type is "appointment-moved"
    And the response field "detail" is "<detail>"
    And the outbox event types are "asn.ASNRegistered, asn.ASNRegistered"

    Examples:
      | path                                                       | body                                                                                                                                      | detail                                                                                                                                                                          |
      | /appointments                                              | {"doorCode":"DOOR-1","carrier":"C","windowStart":"2026-10-09T08:00:00Z","windowEnd":"2026-10-09T10:00:00Z","asnNumbers":["ASN-1"]}        | Appointments are booked in dock-scheduling: POST /appointments on that service.                                                                                                |
      | /appointments/appt-00000000-0000-4000-8000-0000000000ff/check-ins | {}                                                                                                                                        | A carrier's arrival is recorded at the gate by yard-management (POST /visits naming the appointmentId); dock-scheduling derives the appointment's check-in from it. There is no check-in on appointments any more. |
      | /appointments/appt-00000000-0000-4000-8000-0000000000ff/cancellations | {"reason":"x"}                                                                                                                            | Appointments are cancelled in dock-scheduling: POST /appointments/{appointmentId}/cancellations on that service.                                                                |

  Scenario: A moved write is answered 410 without an Idempotency-Key
    When I POST "/appointments" without an idempotency key and body ''
    Then the response status is 410
    And the problem type is "appointment-moved"

  Scenario: A booking announced by dock-scheduling shows up in the copy
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When I GET "/appointments/{appointment}"
    Then the response status is 200
    And the response field "doorCode" is "DOOR-1"
    And the response field "state" is "Booked"
    And the response field "asnNumbers" is "ASN-1"
    And the response field "version" is "1"
    And the ETag is "1"
    And the outbox event types are "asn.ASNRegistered, asn.ASNRegistered"

  Scenario: Reading an unknown appointment
    When I GET "/appointments/appt-00000000-0000-4000-8000-0000000000ff"
    Then the response status is 404
    And the problem type is "appointment-not-found"

  Scenario: A cancellation announced by dock-scheduling cancels the copy
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When dock-scheduling cancels the appointment
    Then the consumer reports "applied"
    When I GET "/appointments/{appointment}"
    Then the response field "state" is "Cancelled"
    And the response field "version" is "2"
    And the outbox event types are "asn.ASNRegistered, asn.ASNRegistered"

  Scenario: Only a newer version changes the copy
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When dock-scheduling announces the appointment again at door "DOOR-2" with version 3
    Then the consumer reports "applied"
    When dock-scheduling announces the appointment again at door "DOOR-OLD" with version 2
    Then the consumer reports "ignored"
    When I GET "/appointments/{appointment}"
    Then the response field "doorCode" is "DOOR-2"
    And the response field "version" is "3"

  Scenario: A redelivered announcement is a duplicate
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    When dock-scheduling redelivers the last appointment announcement
    Then the consumer reports "duplicate"

  Scenario: A later booking announcement never revives a cancelled appointment
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And dock-scheduling cancels the appointment
    When dock-scheduling announces the appointment again at door "DOOR-1" with version 5
    Then the consumer reports "applied"
    When I GET "/appointments/{appointment}"
    Then the response field "state" is "Cancelled"
    And the response field "version" is "5"

  Scenario: An outbound appointment is not copied
    When dock-scheduling announces an outbound appointment at door "DOOR-9"
    Then the consumer reports "ignored"
    When I GET "/appointments/{appointment}"
    Then the response status is 404
    And the problem type is "appointment-not-found"

  Scenario: Listing appointments of a door
    Given an appointment at door "DOOR-1" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-1"
    And an appointment at door "DOOR-2" from "2026-10-09T08:00:00Z" to "2026-10-09T10:00:00Z" covers the ASN "ASN-2"
    When I list "/appointments" with query "door=DOOR-2&state=Booked"
    Then the listed "doorCode" values are "DOOR-2"
