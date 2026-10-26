Feature: Refunding captured payments
  A lost or returned package refunds its group's captured amount; operators can
  also request a manual refund. Refunds never exceed what was captured.

  Background:
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"

  Scenario: A lost package refunds its group
    When package "TRK1" of order "ord-1" is lost
    Then a refund of 7000 for reason "LOST" is "SUCCEEDED"

  Scenario: A returned package refunds its group
    When package "TRK1" of order "ord-1" is returned to sender
    Then a refund of 7000 for reason "RETURNED_TO_SENDER" is "SUCCEEDED"

  Scenario: A manual refund is issued for a captured order
    When a manual refund of 1000 is requested for order "ord-1"
    Then the response status is 201

  Scenario: Refunding more than was captured is rejected
    When a manual refund of 7001 is requested for order "ord-1"
    Then the response status is 409
    And the problem type is "over-refund"
