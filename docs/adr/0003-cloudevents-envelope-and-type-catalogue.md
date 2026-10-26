# ADR 0003: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-25).

## Context

Every Kafka message in the retail network is a CloudEvents 1.0 event in structured mode
(repo rule `.claude/rules/fleet/cloudevents.md`). This ADR is the catalogue of every type
customer-payments publishes; `TestEventCatalogueMatchesContract` checks it against
`apis/asyncapi.yaml`. Consumed types are listed at the end for reference.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across relay retries;
  consumers dedupe on it); `source` `/retailnetwork/customer-payments`; `subject` the aggregate
  root id (`authorizationId`, or `refundId` for refund events); `time` the commit instant in UTC;
  `datacontenttype` `application/json`; `dataschema`
  `urn:retailnetwork:customer-payments:events:<EventName>:v1`.
- Kafka value = the JSON event format; header `content-type: application/cloudevents+json; charset=UTF-8`;
  Kafka key = the subject.
- `data` is camelCase; optional fields are omitted when unset; amounts are integer minor units.
- Built and decoded only through `internal/adapters/kafka/cloudevents`. Tier is `storefront`;
  `<entity>` is `paymentauthorization` or `refund`.

### Published types

All types go to `retailnetwork.customer-payments.events`. All are analytics-only for their
consumers by design (model gap H9): no sibling reacts to them.

```text
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized stream: events
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentCaptured stream: events
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentReleased stream: events
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorizationExpired stream: events
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentDeclined stream: events
type: com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentReauthorized stream: events
type: com.retailnetwork.storefront.customer-payments.refund.RefundIssued stream: events
type: com.retailnetwork.storefront.customer-payments.refund.RefundFailed stream: events
```

| Event | Raised when | `data` |
|---|---|---|
| `PaymentAuthorized` | provider authorization succeeds | `{authorizationId, checkoutSessionId, currency, totalMinor, allocations[{groupNo, amountMinor}], expiresAt}` |
| `PaymentCaptured` | a group's allocation is captured | `{authorizationId, orderId, groupNo, amountMinor}` |
| `PaymentReleased` | an allocation is released (CANCELLED, UNSOURCEABLE, VOIDED, EXPIRED) | `{authorizationId, orderId?, groupNo, amountMinor, reason}` |
| `PaymentAuthorizationExpired` | the authorization lapses or re-authorization is declined | `{authorizationId, orderId?, expiredAt}` |
| `PaymentDeclined` | authorize or re-authorize is declined (H2, H7) | `{authorizationId, checkoutSessionId?, reason, at}` |
| `PaymentReauthorized` | re-authorization before expiry (H1) | `{authorizationId, orderId, expiresAt, providerAuthorizationId}` |
| `RefundIssued` | the provider confirms a refund (H12) | `{refundId, orderId, authorizationId, groupNo?, amountMinor, reason}` |
| `RefundFailed` | a refund attempt fails; `final` after 5 attempts (H8) | `{refundId, orderId, authorizationId, groupNo?, code, attempts, final}` |

The encoders are pinned by golden JSON files under `internal/adapters/outbound/kafka/testdata`.

### Consumed types

```text
com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted
com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped
com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled
com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable
com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost
com.retailnetwork.logistics.shipping-and-tracking.journey.PackageReturned
```

## Consequences

A breaking payload change is a new `.v2` type and dataschema; this ADR, `apis/asyncapi.yaml`
and the consumers change together.
