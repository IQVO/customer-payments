---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 2
description: "Every Customer Payments use case: trigger, inputs, invariants and the events it raises, from internal/application/usecases."
---

# Use cases

Every use case is a struct in `internal/application/usecases` that embeds `Deps`
and exposes `Execute` (or, for the Kafka-driven ones, methods on `Consumer`).
Commands share one shape inside a single `UnitOfWork`: load the aggregate with
its `version`, let the domain decide, call the payment provider with a
deterministic key, save conditionally on the version, append the raised events to
the outbox, return a view. A version conflict (`ports.ErrConcurrentUpdate`)
reruns the attempt (2 attempts for commands, 5 for consumed events) before it
surfaces as `409 concurrent-update`. Errors pass through unchanged and the HTTP
adapter maps them to problem slugs ([API overview](/docs/api-reference)).

## At a glance

| Use case | Trigger | Inbound adapter | Events raised |
| --- | --- | --- | --- |
| `AuthorizePayment` | checkout | `POST /payment-authorizations` | `PaymentAuthorized` or `PaymentDeclined` |
| `VoidAuthorization` | checkout; policy `VoidUnboundAuthorizations` | `POST /payment-authorizations/{authorizationId}/voids`; policy loop | `PaymentReleased` (reason `VOIDED`, one per allocation) |
| `GetAuthorization`, `ListAuthorizations` | checkout, ops, MCP, A2A | `GET /payment-authorizations...`, MCP, A2A | none |
| `RequestRefund` | storefront ops | `POST /refunds` | `RefundIssued` or `RefundFailed` |
| `RetryRefund` | storefront ops | `POST /refunds/{refundId}:retry` | `RefundIssued` or `RefundFailed` |
| `GetRefund`, `ListRefunds` | ops, MCP, A2A | `GET /refunds...`, MCP, A2A | none |
| `Consumer.CheckoutCompleted` (bind) | `CheckoutCompleted` | Kafka `retailnetwork.checkout.events` | none itself; applies parked facts |
| `Consumer.ApplyFact`: capture | `FulfillmentShipped` | Kafka `retailnetwork.order-sourcing.events` | `PaymentCaptured` |
| `Consumer.ApplyFact`: release | `FulfillmentCancelled`, `FulfillmentUnsourceable` | Kafka `retailnetwork.order-sourcing.events` | `PaymentReleased` (`CANCELLED` / `UNSOURCEABLE`) |
| `Consumer.ApplyFact`: refund package | `PackageLost`, `PackageReturned` | Kafka `retailnetwork.shipping-and-tracking.events` | `RefundIssued` or `RefundFailed` |
| `ReauthorizeBeforeExpiry` | clock | policy loop | `PaymentReauthorized`, or `PaymentDeclined` (`REAUTH_DECLINED`) |
| `ProcessRequestedRefunds` | clock | policy loop | `RefundIssued` or `RefundFailed` |
| `MaintainParkedFacts` | clock | policy loop | none (publishes to the unbound-facts DLQ topic) |

## AuthorizePayment

- **Trigger / inputs:** checkout. `checkoutSessionId`, `paymentMethodToken` (a PSP
  token, never a card number), `currency` (`^[A-Z]{3}$`), `totalMinor`,
  `allocations[]` (`groupNo`, `amountMinor`). Header `Idempotency-Key` required.
- **Flow:** if the session already holds a live (non-declined) authorization, it
  is returned unchanged (`200`) and the provider is not called. Otherwise the
  draft is checked, the provider `Authorize` is called with key
  `authorize:<sessionId>:0:<n>` (n = attempts so far + 1), and the authorization
  is created `AUTHORIZED` (with `expiresAt` and the provider hold id) or
  `DECLINED` (with the decline code). Saved with its outbox row in one
  transaction; `201`.
- **Invariants:** allocations sum to `totalMinor` and name distinct groups
  (`allocation-mismatch`); amounts are not negative; valid ids and currency
  (`validation-failed`); one live authorization per session and per order (unique
  partial indexes, `duplicate-authorization` on a race).
- **Failure:** a provider outage is `payment-provider-unavailable` (503) and
  nothing is stored; the metric `payments.authorizations.decided` records
  `authorized`, `declined` or `failed` with a reason.
- **Events:** `PaymentAuthorized` (with `allocations` and `expiresAt`) or
  `PaymentDeclined`.

## VoidAuthorization

- **Trigger / inputs:** checkout after a definitive rejection of its place-order
  step, or the `void-unbound-authorizations` policy. `authorizationId`; header
  `Idempotency-Key` required on REST.
- **Invariant:** only an `AUTHORIZED` authorization (no capture, not expired,
  not voided) can be voided, else `illegal-transition` (409). The domain verdict
  is checked on a copy **before** the provider is called.
- **Flow:** provider `Void`, then every pending allocation is released with
  reason `VOIDED` and the state becomes `VOIDED`.
- **Events:** one `PaymentReleased` (reason `VOIDED`) per allocation.

## RequestRefund and RetryRefund

- **Trigger / inputs:** `POST /refunds` with `orderId`, `amountMinor`, `reason`
  and optional `groupNo` (0 = whole order); the lost / returned policies call the
  same internal path.
- **Invariants:** `orderId` must be bound to an authorization (`not-found`
  otherwise); `amountMinor >= 1`; reason is `LOST`, `RETURNED_TO_SENDER` or
  `MANUAL`; refunds of the order (and of the group when `groupNo > 0`) never
  exceed what was captured, counting `REQUESTED` and `SUCCEEDED` refunds and
  never `FAILED` ones (`over-refund`, 409).
- **Flow:** create the refund `REQUESTED`, save it, call the provider `Refund`
  with key `refund:<authorizationId>:<groupNo>:<refundId>:<attempt>`. Success
  records `SUCCEEDED`; a provider decline records `FAILED` (with the code); an
  unreachable provider leaves it `REQUESTED` (WARN) for the
  `process-requested-refunds` policy.
- **RetryRefund:** `FAILED` to `REQUESTED` and one more provider attempt;
  at most `refund.MaxAttempts = 5` attempts in all. When the fifth fails the
  refund stays `FAILED` and an ERROR `ALERT refund failed for good: operator
  action needed` is logged. A refund that is not `FAILED` is `illegal-transition`.
- **Events:** `RefundIssued` or `RefundFailed` (`final: true` on the last attempt).

## Queries

`GetAuthorization`, `ListAuthorizations` (exactly one of `orderId` and
`checkoutSessionId`, else `validation-failed`; a session lists declined attempts,
an order lists its one bound authorization or nothing), `GetRefund`, `ListRefunds`
(`orderId` required, oldest first). The two lists return one keyset page
(`Execute(..., after, limit)`: `limit` rows, ordered by `(createdAt, id)`, and the
keyset to resume after). The same four use cases back the MCP tools and the A2A
skills, which keep their own tool schemas and follow every page through
`All` (up to 1000 rows) so they still return complete data.

## Consumed events

Each consumer method claims the CloudEvents `id` in `processed_events` and applies
its effect in the same unit of work, so a redelivery converges.

**CheckoutCompleted (bind).** Inputs `sessionId`, `orderId`, `authorizationId`.
Invalid ids, or a `sessionId` that does not match the authorization, are
`ErrInvalidEvent` (dead-lettered at once). `BindOrder` is idempotent for the same
order and `order-already-bound` for another one. After the bind, every parked fact
of the order is applied in arrival order.

**ApplyFact.** Valid facts: group facts (shipped, cancelled, unsourceable) need
`groupNo >= 1`; package facts (lost, returned) need exactly one tracking number.
If the order has no authorization yet, the fact is **parked**.

- *Capture* (`FulfillmentShipped`): the group's allocation is captured at the
  provider (key `capture:<authorizationId>:<groupNo>`), tracking numbers are stored,
  `PaymentCaptured` raised. A captured group is left alone. Unknown group,
  over-capture and capture after expiry are domain refusals (dead-lettered).
  A shipment also re-applies facts that were waiting for its tracking number.
- *Release* (`FulfillmentCancelled`, `FulfillmentUnsourceable`): the pending
  allocation is released and the provider hold is reduced to what is still pending
  (`ReduceAuthorization`), or voided when nothing is pending; `PaymentReleased` with
  reason `CANCELLED` or `UNSOURCEABLE`. A released group is left alone; a captured
  group cannot be released.
- *Refund package* (`PackageLost`, `PackageReturned`): the group that carried the
  tracking number is found (if no capture carries it yet the fact waits, parked);
  the amount is the group's captured amount less refunds already requested or made;
  nothing left is a no-op; an over-refund is skipped with a WARN.

## Policies (clock-driven, every `POLICY_INTERVAL`)

- **ReauthorizeBeforeExpiry** (`reauthorize-before-expiry`): live authorizations
  with `expires_at` within `ReauthLead` (24 h). The provider is asked to
  reauthorize the pending amount (key `reauthorize:<id>:0:<n>`); success raises
  `PaymentReauthorized` (state stays `AUTHORIZED`/`PARTIALLY_CAPTURED`, new
  `expiresAt`); a provider decline expires the authorization
  (`PaymentAuthorizationExpired`) and raises `PaymentDeclined` with reason
  `REAUTH_DECLINED`; a hold that already lapsed expires without calling the
  provider. Provider outages, concurrent updates and illegal transitions are
  skipped with a WARN and retried at the next tick.
- **VoidUnboundAuthorizations** (`void-unbound-authorizations`): `AUTHORIZED`
  authorizations with no `orderId` older than `UnboundVoidAfter` (24 h) are voided.
- **ProcessRequestedRefunds** (`process-requested-refunds`): refunds `REQUESTED`
  for more than 30 s are settled against the provider.
- **MaintainParkedFacts** (`maintain-parked-facts`): facts parked longer than 24 h
  are published to `retailnetwork.customer-payments.unbound-facts.dlq` with an
  `ALERT fact for an unbound order dead-lettered` ERROR; facts older than 30 days are
  purged.

Each pass handles at most 200 rows per tick.
