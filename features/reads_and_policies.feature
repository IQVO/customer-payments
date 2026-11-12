# reads_and_policies.feature — wave 2: the read side (list/get authorizations
# and refunds, keyset paging rules), the refund retry command, the H2
# reauthorization and unbound-void maintenance policies, the idempotency
# replay path and the platform endpoints.
Feature: Reads, retries and the maintenance policies

  Scenario: Authorizations are listed by order and by session
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And checkout session "cs-2" is authorized for 5000 BRL as 5000 for group 1
    When a GET request is sent to "/payment-authorizations?orderId=ord-1"
    Then the response status is 200
    And the response list "items" has 1 items
    And the response field "items.0.checkoutSessionId" is cs-1
    And the response field "items.0.orderId" is ord-1
    When a GET request is sent to "/payment-authorizations?checkoutSessionId=cs-2"
    Then the response status is 200
    And the response list "items" has 1 items
    And the response field "items.0.state" is AUTHORIZED

  Scenario: The list query wants exactly one of orderId and checkoutSessionId
    When a GET request is sent to "/payment-authorizations"
    Then the response status is 422
    And the problem type is "validation-failed"
    When a GET request is sent to "/payment-authorizations?orderId=ord-1&checkoutSessionId=cs-1"
    Then the response status is 422
    And the problem type is "validation-failed"
    When a GET request is sent to "/payment-authorizations?orderId=ord-1&limit=0"
    Then the response status is 400
    And the problem type is "invalid-limit"
    When a GET request is sent to "/payment-authorizations?orderId=ord-1&cursor=not-a-cursor"
    Then the response status is 400
    And the problem type is "invalid-cursor"
    When a GET request is sent to "/payment-authorizations?orderId=ord-1&bogus=1"
    Then the response status is 400
    And the problem type is "unknown-query-parameter"

  Scenario: A refund is read back and listed
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And a manual refund of 1000 is requested for order "ord-1"
    When a GET request is sent to "/refunds/ref-1"
    Then the response status is 200
    And the response field "orderId" is ord-1
    And the response field "amountMinor" is 1000
    And the response field "reason" is MANUAL
    And the response field "state" is SUCCEEDED
    And the response field "attempts" is 1
    When a GET request is sent to "/refunds?orderId=ord-1"
    Then the response status is 200
    And the response list "items" has 1 items
    When a GET request is sent to "/refunds/ref-99"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A declined refund fails and its retry succeeds
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And the provider "refund" is failing with DECLINED code ISSUER_REFUSED
    When a manual refund of 1000 is requested for order "ord-1"
    Then the response status is 201
    And the response field "state" is FAILED
    And the response field "failureCode" is ISSUER_REFUSED
    And the response field "attempts" is 1
    Given the provider recovers
    When a POST request is sent to "/refunds/ref-1/retries"
    Then the response status is 200
    And the response field "state" is SUCCEEDED
    And the response field "attempts" is 2

  Scenario: A refund that is not failed cannot be retried
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    And a manual refund of 1000 is requested for order "ord-1"
    When a POST request is sent to "/refunds/ref-1/retries"
    Then the response status is 409
    And the problem type is "illegal-transition"

  Scenario: A live hold near expiry is reauthorized
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And time advances 145 hours
    When the reauthorization policy runs
    Then 1 authorization was handled
    And the provider "reauthorize" ran 1 times
    And the authorization state is "AUTHORIZED"
    And the response field "expiresAt" is 2026-11-06T10:00:00Z

  Scenario: A declined reauthorization expires the hold
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And time advances 145 hours
    And the provider "reauthorize" is failing with DECLINED code CARD_EXPIRED
    When the reauthorization policy runs
    Then 1 authorization was handled
    And the authorization state is "EXPIRED"

  Scenario: An already-lapsed hold expires without asking the provider
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And time advances 192 hours
    And the provider "reauthorize" is failing with UNAVAILABLE
    When the reauthorization policy runs
    Then 1 authorization was handled
    And the authorization state is "EXPIRED"
    And the provider "reauthorize" ran 0 times

  Scenario: An unbound authorization is voided after the lead
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And time advances 25 hours
    When the unbound-void policy runs
    Then 1 authorization was handled
    And the authorization state is "VOIDED"

  Scenario: A bound authorization survives the unbound-void sweep
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And checkout session "cs-2" is authorized for 5000 BRL as 5000 for group 1
    And time advances 25 hours
    When the unbound-void policy runs
    Then 1 authorization was handled
    When a GET request is sent to "/payment-authorizations?orderId=ord-1"
    Then the response status is 200
    And the response field "items.0.state" is AUTHORIZED
    When a GET request is sent to "/payment-authorizations?checkoutSessionId=cs-2"
    Then the response status is 200
    And the response field "items.0.state" is VOIDED

  Scenario: An idempotent replay answers from the stored record
    When a POST request is sent to "/payment-authorizations" with key "k-replay" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":1000,"allocations":[{"groupNo":1,"amountMinor":1000}]}
    Then the response status is 201
    When a POST request is sent to "/payment-authorizations" with key "k-replay" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":1000,"allocations":[{"groupNo":1,"amountMinor":1000}]}
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    And the provider authorized exactly 1 hold
    When a POST request is sent to "/payment-authorizations" with key "k-replay" and the JSON body {"checkoutSessionId":"cs-9","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":2000,"allocations":[{"groupNo":1,"amountMinor":2000}]}
    Then the response status is 409
    And the problem type is "idempotency-key-reused"

  Scenario: The platform endpoints answer
    When a GET request is sent to "/healthz"
    Then the response status is 200
    When a GET request is sent to "/readyz"
    Then the response status is 200
    When a GET request is sent to "/metrics"
    Then the response status is 200
