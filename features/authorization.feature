Feature: Authorizing a checkout session's payment
  A payment authorization is created once per checkout session and splits the total
  into one allocation per shipment group.

  Scenario: A new authorization holds every allocation as pending
    When the shopper's checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    Then the response status is 201
    And the authorization state is "AUTHORIZED"
    And allocation of group 1 is "PENDING"
    And allocation of group 2 is "PENDING"

  Scenario: Replaying the authorization of a session returns the existing one
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    When the shopper's checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    Then the authorization id is the same as the first one
    And the provider authorized exactly 1 hold

  Scenario: Allocations that do not sum to the total are rejected
    When the shopper's checkout session "cs-2" is authorized for 10000 BRL as 7000 for group 1 and 2000 for group 2
    Then the response status is 422
    And the problem type is "allocation-mismatch"

  Scenario: Voiding an authorization releases every pending allocation
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    When the authorization is voided
    Then the response status is 200
    And the authorization state is "VOIDED"
