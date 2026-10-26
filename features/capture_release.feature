Feature: Capturing and releasing allocations as fulfilment facts arrive
  Each shipment group is captured when it ships and released when it is cancelled
  or unsourceable. The authorization state is derived from its allocations.

  Background:
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"

  Scenario: Shipping one group partially captures the authorization
    When group 1 of order "ord-1" ships with tracking number "TRK1"
    Then the authorization state is "PARTIALLY_CAPTURED"
    And allocation of group 1 is "CAPTURED"
    And allocation of group 2 is "PENDING"

  Scenario: Shipping every group captures the authorization
    When group 1 of order "ord-1" ships with tracking number "TRK1"
    And group 2 of order "ord-1" ships with tracking number "TRK2"
    Then the authorization state is "CAPTURED"

  Scenario: Cancelling the only pending group after a capture still counts as captured
    When group 1 of order "ord-1" ships with tracking number "TRK1"
    And group 2 of order "ord-1" is cancelled
    Then the authorization state is "CAPTURED"
    And allocation of group 2 is "RELEASED"

  Scenario: Releasing every group releases the authorization
    When group 1 of order "ord-1" is unsourceable
    And group 2 of order "ord-1" is cancelled
    Then the authorization state is "RELEASED"

  Scenario: A fact for an order that is not bound yet is parked and applied on bind
    When group 1 of order "ord-9" ships with tracking number "TRK9"
    And checkout session "cs-9" is authorized for 5000 BRL as 5000 for group 1
    And checkout completed for session "cs-9" as order "ord-9"
    Then the authorization of order "ord-9" is "CAPTURED"
