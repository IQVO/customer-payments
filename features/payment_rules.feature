# payment_rules.feature — scenarios derived from the context documentation
# (warehouse-docs storefront/docs/contexts/customer-payments: business-context,
# use-cases, aggregate-design-canvas). Each scenario names the rule it pins.
Feature: The payment rules of the customer-payments documentation

  # --- AuthorizePayment -----------------------------------------------------

  Scenario: A declined authorization is kept as a terminal DECLINED record
    Given the provider "authorize" is failing with DECLINED code INSUFFICIENT_FUNDS
    When the shopper's checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    Then the response status is 201
    And the response field "state" is DECLINED
    And the provider "capture" ran 0 times

  Scenario: The checkout session stays open for another token after a decline
    Given the provider "authorize" is failing with DECLINED code INSUFFICIENT_FUNDS
    And the shopper's checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And the provider recovers
    When the shopper's checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    Then the response status is 201
    And the authorization state is "AUTHORIZED"

  Scenario: A provider outage answers 503 and a retry with the same key moves money once
    Given the provider "authorize" is failing with UNAVAILABLE
    When a POST request is sent to "/payment-authorizations" with key "cs-1:auth" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":4000}]}
    Then the response status is 503
    And the problem type is "payment-provider-unavailable"
    Given the provider recovers
    When a POST request is sent to "/payment-authorizations" with key "cs-1:auth" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":4000}]}
    Then the response status is 201
    And the response field "state" is AUTHORIZED
    And the provider authorized exactly 1 hold

  Scenario: A second authorize for the same session under a different key never opens a second hold
    When a POST request is sent to "/payment-authorizations" with key "cs-1:auth" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":4000}]}
    Then the response status is 201
    When a POST request is sent to "/payment-authorizations" with key "cs-1:auth-again" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_mastercard","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":4000}]}
    Then the provider authorized exactly 1 hold

  Scenario: Allocations must name distinct shipment groups
    When a POST request is sent to "/payment-authorizations" with key "cs-1:auth" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":2000},{"groupNo":1,"amountMinor":2000}]}
    Then the response status is 422
    And the problem type is "allocation-mismatch"
    And the provider authorized exactly 0 holds

  Scenario: A write without an Idempotency-Key is refused
    When a POST request is sent to "/payment-authorizations" without a key and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":4000,"allocations":[{"groupNo":1,"amountMinor":4000}]}
    Then the response status is 400
    And the problem type is "idempotency-key-required"
    And the provider authorized exactly 0 holds

  # --- VoidAuthorization ----------------------------------------------------

  Scenario: Only an AUTHORIZED hold can be voided
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    When the authorization is voided
    Then the response status is 409
    And the problem type is "illegal-transition"
    And the authorization state is "PARTIALLY_CAPTURED"

  Scenario: A second void of a voided hold under a new key is an illegal transition
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And the authorization is voided
    When the authorization is voided
    Then the response status is 409
    And the problem type is "illegal-transition"
    And the provider "void" ran 1 times

  Scenario: Replaying a void with its own key answers the stored response
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    When the authorization is voided with key "cs-1:void"
    And the authorization is voided with key "cs-1:void"
    Then the response status is 200
    And the response header "Idempotency-Replayed" is "true"
    And the provider "void" ran 1 times

  Scenario: Voiding an authorization that does not exist is a 404
    When a POST request is sent to "/payment-authorizations/auth-404/voids"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A provider outage during void leaves the hold untouched
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And the provider "void" is failing with UNAVAILABLE
    When the authorization is voided
    Then the response status is 503
    And the problem type is "payment-provider-unavailable"
    And the authorization state is "AUTHORIZED"

  # --- CaptureAllocation ----------------------------------------------------

  Scenario: Capture stores the tracking number against the group's allocation
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    When group 2 of order "ord-1" ships with tracking number "TRK-G2"
    Then the authorization is read back
    And the response field "allocations.1.trackingNumber" is TRK-G2
    And the response field "allocations.0.trackingNumber" is null

  Scenario: A redelivered shipment event captures once
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    When group 1 of order "ord-1" ships with tracking number "TRK1" in event "ev-ship-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1" in event "ev-ship-1"
    Then the provider "capture" ran 1 times
    And the authorization state is "PARTIALLY_CAPTURED"

  Scenario: A second shipment fact for a captured group never captures it again
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    When group 1 of order "ord-1" ships with tracking number "TRK1" in event "ev-ship-again"
    Then the provider "capture" ran 1 times
    And the authorization state is "PARTIALLY_CAPTURED"

  Scenario: A fact naming a group the authorization does not hold is an anomaly and changes nothing
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    When group 3 of order "ord-1" ships with tracking number "TRK3" but the fact is refused
    Then the provider "capture" ran 0 times
    And the authorization state is "AUTHORIZED"

  Scenario: No capture after the authorization expired
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And checkout completed for session "cs-1" as order "ord-1"
    And time advances 169 hours
    When group 1 of order "ord-1" ships with tracking number "TRK1" but the fact is refused
    Then the provider "capture" ran 0 times
    And allocation of group 1 is "PENDING"

  Scenario: A shipment fact for a voided authorization captures nothing
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And checkout completed for session "cs-1" as order "ord-1"
    And the authorization is voided
    When group 1 of order "ord-1" ships with tracking number "TRK1" but the fact is refused
    Then the provider "capture" ran 0 times
    And the authorization state is "VOIDED"

  Scenario: A provider outage during capture leaves the allocation pending
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And checkout completed for session "cs-1" as order "ord-1"
    And the provider "capture" is failing with UNAVAILABLE
    When group 1 of order "ord-1" ships with tracking number "TRK1" but the fact is refused
    Then allocation of group 1 is "PENDING"

  # --- ReleaseAllocation ----------------------------------------------------

  Scenario: A partial release reduces the hold at the provider
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    When group 2 of order "ord-1" is cancelled
    Then the provider "reduce" ran 1 times
    And allocation of group 2 is "RELEASED"
    And the authorization state is "AUTHORIZED"

  Scenario: An unsourceable group releases only its own allocation
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    When group 1 of order "ord-1" is unsourceable
    Then allocation of group 1 is "RELEASED"
    And allocation of group 2 is "PENDING"
    And the authorization state is "AUTHORIZED"

  Scenario: A captured group cannot be released afterwards
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    When group 1 of order "ord-1" is cancelled but the fact is refused
    Then allocation of group 1 is "CAPTURED"

  # --- BindOrder ------------------------------------------------------------

  Scenario: Binding the same order twice is a no-op
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And checkout completed for session "cs-1" as order "ord-1"
    When checkout completed for session "cs-1" as order "ord-1"
    Then the authorization state is "AUTHORIZED"
    And the response field "orderId" is ord-1

  Scenario: Parked facts are applied in arrival order on bind
    When group 1 of order "ord-7" ships with tracking number "TRK7"
    And package "TRK7" of order "ord-7" is lost
    And checkout session "cs-7" is authorized for 5000 BRL as 5000 for group 1
    And checkout completed for session "cs-7" as order "ord-7"
    Then the authorization of order "ord-7" is "CAPTURED"
    And a refund of 5000 for reason "LOST" is "SUCCEEDED"

  # --- Refund ---------------------------------------------------------------

  Scenario: A failed refund does not count against the captured total
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And the provider "refund" is failing with DECLINED code ISSUER_REFUSED
    And a manual refund of 7000 is requested for order "ord-1"
    And the provider recovers
    When a manual refund of 7000 is requested for order "ord-1"
    Then the response status is 201
    And the response field "state" is SUCCEEDED

  Scenario: A requested refund counts against the captured total even while it is not settled
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And a manual refund of 5000 is requested for order "ord-1"
    When a manual refund of 2001 is requested for order "ord-1"
    Then the response status is 409
    And the problem type is "over-refund"

  Scenario: Retrying a failed refund is capped at five attempts
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And the provider "refund" is failing with DECLINED code ISSUER_REFUSED
    And a manual refund of 1000 is requested for order "ord-1"
    When a POST request is sent to "/refunds/ref-1/retries"
    And a POST request is sent to "/refunds/ref-1/retries"
    And a POST request is sent to "/refunds/ref-1/retries"
    And a POST request is sent to "/refunds/ref-1/retries"
    Then the response field "attempts" is 5
    And the response field "state" is FAILED
    When a POST request is sent to "/refunds/ref-1/retries"
    Then the response status is 409
    And the problem type is "illegal-transition"

  Scenario: A refund for a lost package is not issued twice when the event is redelivered
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    When package "TRK1" of order "ord-1" is lost in event "ev-lost-1"
    And package "TRK1" of order "ord-1" is lost in event "ev-lost-1"
    Then the provider "refund" ran 1 times
    And a refund of 7000 for reason "LOST" is "SUCCEEDED"

  Scenario: A refund needs an order bound to an authorization
    When a manual refund of 1000 is requested for order "ord-unknown"
    Then the response status is 404
    And the problem type is "not-found"

  # --- ReauthorizeBeforeExpiry ---------------------------------------------

  Scenario: A fully captured authorization is not reauthorized
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And time advances 145 hours
    When the reauthorization policy runs
    Then 0 authorizations were handled
    And the provider "reauthorize" ran 0 times

  Scenario: A partially captured hold that cannot be reauthorized expires
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And time advances 145 hours
    And the provider "reauthorize" is failing with DECLINED code CARD_EXPIRED
    When the reauthorization policy runs
    Then 1 authorization was handled
    And the authorization state is "EXPIRED"
    And allocation of group 1 is "CAPTURED"

  Scenario: A hold that is not near expiry is left alone
    Given checkout session "cs-1" is authorized for 10000 BRL as 10000 for group 1
    And time advances 100 hours
    When the reauthorization policy runs
    Then 0 authorizations were handled
    And the provider "reauthorize" ran 0 times
