Feature: A retried request with the same Idempotency-Key is answered from the record only when its first answer is final
  The first answer of a key is stored when it is final for the request: a success,
  or a refusal whose problem type is FINAL (ADR 0008). The same key and request then
  replays it byte for byte (marked Idempotency-Replayed) and nothing runs again. A
  refusal that depends on state that can change (RETRYABLE: a concurrent update, an
  order not bound yet, a state not reached) and a 5xx are not stored: the key is
  released and a retry runs the work again.

  Scenario: A refusal that is final for the request is stored and replayed
    When a POST request is sent to "/payment-authorizations" with key "k-final" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":2000,"allocations":[{"groupNo":1,"amountMinor":1000}]}
    Then the response status is 422
    And the problem type is "allocation-mismatch"
    And the response is not marked replayed
    When a POST request is sent to "/payment-authorizations" with key "k-final" and the JSON body {"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":2000,"allocations":[{"groupNo":1,"amountMinor":1000}]}
    Then the response status is 422
    And the problem type is "allocation-mismatch"
    And the response header "Idempotency-Replayed" is "true"

  Scenario: A refusal that depends on state that can change is not stored, so the key retries
    When a POST request is sent to "/refunds" with key "k-late" and the JSON body {"orderId":"ord-1","amountMinor":1000,"reason":"MANUAL"}
    Then the response status is 404
    And the problem type is "not-found"
    Given checkout session "cs-1" is authorized for 10000 BRL as 7000 for group 1 and 3000 for group 2
    And checkout completed for session "cs-1" as order "ord-1"
    And group 1 of order "ord-1" ships with tracking number "TRK1"
    When a POST request is sent to "/refunds" with key "k-late" and the JSON body {"orderId":"ord-1","amountMinor":1000,"reason":"MANUAL"}
    Then the response status is 201
    And the response is not marked replayed
    When a POST request is sent to "/refunds" with key "k-late" and the JSON body {"orderId":"ord-1","amountMinor":1000,"reason":"MANUAL"}
    Then the response status is 201
    And the response header "Idempotency-Replayed" is "true"
    And the provider "refund" ran 1 times

  Scenario: A different request under a released key is a new request, not a reuse
    When a POST request is sent to "/refunds" with key "k-free" and the JSON body {"orderId":"ord-9","amountMinor":1000,"reason":"MANUAL"}
    Then the response status is 404
    When a POST request is sent to "/refunds" with key "k-free" and the JSON body {"orderId":"ord-8","amountMinor":1000,"reason":"MANUAL"}
    Then the response status is 404
    And the problem type is "not-found"
