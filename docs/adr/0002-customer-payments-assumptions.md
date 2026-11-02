# ADR 0002: customer-payments assumptions

## Status

Accepted (2026-10-25). Where the frozen model and `model-gap-decisions.md` (H1-H12, S4) differ,
the decisions win; everything else the model leaves open is recorded here and listed as a model gap.

## Assumptions

- **A1** One authorization per checkout session; a replayed authorize returns the existing one
  (ErrDuplicateAuthorization is mapped to the stored authorization, never to an error).
- **A2** States are derived from the allocations (H3); a stored state field is never trusted over them.
- **A3** Provider idempotency keys are deterministic per (operation, authorizationId, groupNo).
- **A4** Fulfilment facts for an unbound order are parked (S4), purged after 30 days (H10) and
  dead-lettered with an alert when still unbound after 24 hours; they are never dropped silently.
- **A5** VoidUnboundAuthorizations voids an authorization that has no CheckoutCompleted 24 hours
  after creation (backstop after checkout's 22 hour give-up); released allocations carry reason VOIDED.
- **A6** An amount in minor units is at most 1,000,000,000 so every sum of allocations fits an int64.
- **A7** Bounded integers stay below the Postgres integer ceiling (2147483647); one authorization
  holds at most 50 allocations (shipment groups).
- **A8** Instants are UTC, truncated to the microsecond Postgres stores, so a round trip never
  changes a timestamp.
- **A9** ReauthorizeBeforeExpiry runs for an authorization with live (pending or captured-unreleased
  groups still pending) allocations at expiresAt minus 24 hours; a decline expires the authorization and
  raises PaymentDeclined with reason REAUTH_DECLINED (H2).
- **A10** RetryRefund moves a FAILED refund back to REQUESTED, at most 5 attempts in total (H8); the
  fifth failure leaves it FAILED and raises RefundFailed with `final: true` for the operator alert.
- **A11** ErrOverRefund counts REQUESTED and issued refunds, never FAILED ones (H8).
- **A12** Payment provider contract. The payment-provider-simulator is a later deliverable, so the
  HTTP adapter (`PAYMENT_PROVIDER_URL`) and the integration tests use a local httptest fake
  (`internal/testing/providerfake`) with this contract. Every POST carries an `Idempotency-Key`
  header; the same key returns the same answer. Bodies are JSON, amounts are integer minor units,
  only PSP tokens travel (never a card number).
  - `POST /authorizations` `{amountMinor, currency, paymentMethodToken, reference}` returns a hold id and its expiry; a declined token answers with a decline code.
  - `POST /authorizations/{id}/voids`, `/reauthorizations` `{amountMinor}`, `/reductions` `{amountMinor}` (partial release, H4), `/captures` `{amountMinor}` and `/refunds` `{amountMinor}` (the noun routes of the fleet REST guideline, [ADR 0006](./0006-adopt-the-fleet-rest-api-guideline.md); the verb spellings `/void`, `/reauthorize` and `/reduce` are retired).
  - A 4xx decline is final; 5xx and transport errors are transient and retried by the caller.
- **A13** The tracking number of a captured group is stored at capture (H6); a refund for a lost or
  returned package finds its group by tracking number.
- **A14** A Refund carries groupNo and authorizationId (H12); a MANUAL refund may omit groupNo.
- **A15** PaymentReleased and PaymentAuthorizationExpired omit orderId while the authorization is unbound.
- **A16** All eight published events are analytics-only for their consumers by design (H9); they are
  still written to the integration topic through the outbox.
- **A17** The system clock reads UTC truncated to the microsecond (see A8).
- **A18** Cancelled and unsourceable fulfilment facts release the group's allocation with reasons
  CANCELLED and UNSOURCEABLE; releasing a released or captured group is a no-op.

## Consequences

The model-fix PR applies H1-H12 to the docs; any other divergence found by a consumer is a new
assumption here.
