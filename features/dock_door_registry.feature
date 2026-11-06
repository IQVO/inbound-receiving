# The dock-door registry fed by facility-layout events.
#
# Derived from:
#   - docs/docs/adr/0003-*: the service keeps a local copy of the inbound dock
#     doors from facility-layout's LocationSlotRegistered and
#     LocationSlotDecommissioned events; only role Dock with a dockFlow of
#     Inbound or Both is kept; the CloudEvents id is claimed in the same unit of
#     work as the change, so a redelivery changes nothing; an event that can
#     never be applied is refused rather than retried
#   - GET /docks lists the known inbound doors (appointments are booked in
#     dock-scheduling since ADR 0019, so this copy no longer gates a booking)
#   - the product-master feed (ProductRegistered) fills the known-SKU copy the
#     same way and is exercised here for its duplicate and invalid outcomes
# The events arrive over Kafka, so they are driven through the same consumer
# use cases the adapter calls; the effects are asserted through REST.

Feature: Dock-door registry fed by facility-layout
  As the receiving service
  I want my copy of the inbound dock doors to follow facility-layout
  So that the docks list follows the facility layout

  Background:
    Given the dock doors are enforced and know "DOCK-SEED"

  @bdd
  Scenario Outline: An inbound or two-way dock door is added to the registry
    When facility-layout announces slot "<door>" with role "Dock" and dock flow "<flow>"
    Then the consumer reports "applied"
    When I GET "/docks"
    Then the listed "doorCode" values are "<door>, DOCK-SEED"
    And the listed "dockFlow" values are "<flow>, Inbound"

    Examples:
      | door     | flow    |
      | DOCK-A   | Inbound |
      | DOCK-A   | Both    |

  @bdd
  Scenario Outline: A slot that is not an inbound dock door is ignored
    When facility-layout announces slot "SLOT-X" with role "<role>" and dock flow "<flow>"
    Then the consumer reports "ignored"
    When I GET "/docks"
    Then the listed "doorCode" values are "DOCK-SEED"

    Examples:
      | role     | flow     |
      | Dock     | Outbound |
      | Dock     |          |
      | Storage  |          |
      | Storage  | Inbound  |
      | Yard     | Both     |

  @bdd
  Scenario: Redelivering the same event changes nothing
    Given facility-layout announces slot "DOCK-A" with role "Dock" and dock flow "Inbound"
    When facility-layout redelivers the last slot announcement
    Then the consumer reports "duplicate"
    When I GET "/docks"
    Then the listed "doorCode" values are "DOCK-A, DOCK-SEED"

  @bdd
  Scenario: Announcing the same door again with a new event id keeps one entry
    Given facility-layout announces slot "DOCK-A" with role "Dock" and dock flow "Inbound"
    When facility-layout announces slot "DOCK-A" with role "Dock" and dock flow "Both"
    Then the consumer reports "applied"
    When I GET "/docks"
    Then the listed "doorCode" values are "DOCK-A, DOCK-SEED"
    And the listed "dockFlow" values are "Both, Inbound"

  @bdd
  Scenario: A decommissioned door leaves the registry
    Given facility-layout announces slot "DOCK-A" with role "Dock" and dock flow "Inbound"
    When facility-layout announces slot "DOCK-A" decommissioned
    Then the consumer reports "applied"
    When I GET "/docks"
    Then the listed "doorCode" values are "DOCK-SEED"

  @bdd
  Scenario: Decommissioning a door that was never known is harmless
    When facility-layout announces slot "DOCK-NEVER" decommissioned
    Then the consumer reports "applied"
    When I GET "/docks"
    Then the listed "doorCode" values are "DOCK-SEED"

  @bdd
  Scenario: A dock announcement with an empty door code is refused
    When facility-layout announces slot "" with role "Dock" and dock flow "Inbound"
    Then the consumer refuses the event as invalid

  @bdd
  Scenario: A decommission with an empty door code is refused
    When facility-layout announces slot "" decommissioned
    Then the consumer refuses the event as invalid

  @bdd
  Scenario: A SKU announced by product-master is accepted once, and a redelivery changes nothing
    Given the product registry is enforced and knows "SKU-SEED"
    When product-master announces event "evt-1" registering SKU "SKU-NEW"
    Then the consumer reports "applied"
    When product-master announces event "evt-1" registering SKU "SKU-NEW"
    Then the consumer reports "duplicate"

  @bdd
  Scenario Outline: A product announcement with an unusable SKU is refused
    When product-master announces event "evt-bad" registering SKU "<sku>"
    Then the consumer refuses the event as invalid

    Examples:
      | sku       |
      |           |
      | has space |
      | has/slash |
