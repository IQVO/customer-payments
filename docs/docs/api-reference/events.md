---
id: events
title: Events
sidebar_label: Events
sidebar_position: 3
---

# Events

The event catalogue of Customer Payments, per `apis/asyncapi.yaml` and the code.
Every message is a **CloudEvents 1.0** event in structured content mode
([ADR 0003](/docs/adr/0003-cloudevents-envelope-and-type-catalogue)).

## Envelope

| Attribute | Value |
| --- | --- |
| Kafka header `content-type` | `application/cloudevents+json; charset=UTF-8` |
| `specversion` | `1.0` |
| `id` | UUID v4, minted once when the event is encoded and stored with the outbox row, so a relay retry republishes the same `id` |
| `source` | `/retailnetwork/customer-payments` |
| `type` | `com.retailnetwork.storefront.customer-payments.<entity>.<EventName>`; `<entity>` is `paymentauthorization` or `refund` |
| `subject` | the `authorizationId` (payment events) or the `refundId` (refund events); it is also the Kafka message key, so one aggregate's events stay ordered on one partition |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:customer-payments:events:<EventName>:v1` |
| `time` | the domain occurrence time, UTC |
| `data` | camelCase JSON; instants RFC 3339 UTC; amounts integer minor units; optional fields omitted when absent, never `null`; arrays always present |

A breaking payload change is a new `.v2` type, never a mutation. Source:
`internal/adapters/kafka/cloudevents/cloudevents.go`,
`internal/adapters/outbound/kafka/encoder.go`.

Example (the golden file `testdata/PaymentAuthorized.golden.json`):

```json
{"specversion":"1.0","id":"7a2c9e41-5f0b-4c3d-9e6a-1b8d4f0c2a55","source":"/retailnetwork/customer-payments","type":"com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized","subject":"pay-1","datacontenttype":"application/json","dataschema":"urn:retailnetwork:customer-payments:events:PaymentAuthorized:v1","time":"2026-10-24T09:00:00.123456Z","data":{"authorizationId":"pay-1","checkoutSessionId":"cs-1","currency":"BRL","totalMinor":10000,"allocations":[{"groupNo":1,"amountMinor":7000},{"groupNo":2,"amountMinor":3000}],"expiresAt":"2026-10-31T09:00:00.123456Z"}}
```

Besides the CloudEvents attributes, the relay publishes the W3C `traceparent` /
`tracestate` headers stored with the outbox row (see
[Observability](/docs/operations/observability#traces)).

## Published: `retailnetwork.customer-payments.events`

Written through the transactional outbox (state and encoded events in one
database transaction, then an always-on relay, at-least-once). The asyncapi
document states that every published event is analytics-only for its consumers:
no sibling context reacts to them.

| Event | Raised when | `data` fields | Raised by |
| --- | --- | --- | --- |
| `PaymentAuthorized` | the provider approved the hold | `authorizationId`, `checkoutSessionId`, `currency`, `totalMinor`, `allocations[]` (`groupNo`, `amountMinor`), `expiresAt` | `AuthorizePayment` |
| `PaymentDeclined` | the provider declined an authorization, or a re-authorization (`reason` = `REAUTH_DECLINED`) | `authorizationId`, `checkoutSessionId`, `reason` (the provider's decline code, e.g. `CARD_DECLINED`), `at` | `AuthorizePayment`, `ReauthorizeBeforeExpiry` |
| `PaymentCaptured` | a group's allocation was captured | `authorizationId`, `orderId` (omitted if absent), `groupNo`, `amountMinor` | `Consumer.ApplyFact` (`FulfillmentShipped`) |
| `PaymentReleased` | an allocation was let go; `reason` is `CANCELLED`, `UNSOURCEABLE`, `VOIDED` or `EXPIRED` | `authorizationId`, `orderId` (omitted), `groupNo`, `amountMinor`, `reason` | `Consumer.ApplyFact` (`FulfillmentCancelled`, `FulfillmentUnsourceable`), `VoidAuthorization` (one event per allocation) |
| `PaymentAuthorizationExpired` | the hold lapsed with money still uncaptured | `authorizationId`, `orderId` (omitted), `expiredAt` | `ReauthorizeBeforeExpiry` |
| `PaymentReauthorized` | the hold was renewed before expiry | `authorizationId`, `orderId` (omitted), `expiresAt`, `providerAuthorizationId` | `ReauthorizeBeforeExpiry` |
| `RefundIssued` | the provider returned the money | `refundId`, `orderId`, `authorizationId`, `groupNo` (omitted for an order-level refund), `amountMinor`, `reason` (`LOST`, `RETURNED_TO_SENDER`, `MANUAL`) | `RequestRefund`, `RetryRefund`, `ProcessRequestedRefunds`, the lost / returned policies |
| `RefundFailed` | the provider refused the refund | `refundId`, `orderId`, `authorizationId`, `groupNo` (omitted), `code`, `attempts`, `final` (true on the fifth failed attempt) | same as `RefundIssued` |

The Go types are in `internal/domain/paymentauthorization/events.go` and
`internal/domain/refund/events.go`; the wire payloads in
`internal/adapters/outbound/kafka/payloads.go`. The golden files cover the eight
types plus the variants `PaymentReleasedUnbound` (no `orderId`) and
`RefundIssuedManual`.

A second, non-CloudEvents topic exists for operators:
`retailnetwork.customer-payments.unbound-facts.dlq` carries a plain JSON copy of
a fulfilment fact whose order stayed unbound for 24 h (see the
[Runbook](/docs/operations/runbook#kafka-topics-and-consumer-groups)); it is not
part of the asyncapi contract.

## Consumed

Dispatch is on the **full** `type`; unknown types are logged and skipped;
duplicates are dropped by the inbox on the CloudEvents `id`. Only the fields the
service reads are listed; unknown fields are ignored.

| Topic | Event `type` | Fields read | Effect |
| --- | --- | --- | --- |
| `retailnetwork.checkout.events` | `com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted` | `sessionId`, `orderId`, `authorizationId` | binds the order to the authorization, then applies the parked facts of that order |
| `retailnetwork.order-sourcing.events` | `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped` | `orderId`, `groupNo`, `trackingNumbers`, `shippedAt` | capture the group's allocation, store the tracking numbers |
| `retailnetwork.order-sourcing.events` | `...assignment.FulfillmentCancelled` | `orderId`, `groupNo` | release the allocation (`CANCELLED`) |
| `retailnetwork.order-sourcing.events` | `...assignment.FulfillmentUnsourceable` | `orderId`, `groupNo` | release the allocation (`UNSOURCEABLE`) |
| `retailnetwork.shipping-and-tracking.events` | `com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost` | `orderId`, `trackingNumber` | refund the captured amount of the group that carried the tracking number (`LOST`) |
| `retailnetwork.shipping-and-tracking.events` | `...journey.PackageReturned` | `orderId`, `trackingNumber` | the same, reason `RETURNED_TO_SENDER` |

The type strings are constants in
`internal/adapters/inbound/kafka/handlers.go`. Failure handling (retry, five
attempts, `<topic>.dlq`) is described in the
[Runbook](/docs/operations/runbook#dead-letters). Per-event integration notes:
[Integration](/docs/ecosystem/integration).
