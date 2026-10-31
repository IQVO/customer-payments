---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 5
description: "Six Customer Payments scenarios as ddd-crew domain message flows: authorize and void, capture on shipment, parked facts applied on bind, release, refund, and re-authorization."
---

# Domain message flow

Following ddd-crew
[Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling).
Participants are contexts, actors and the provider; Kafka is one participant. Arrows are
prefixed `cmd:`, `qry:` or `evt:`. Event names are written `entity.EventName`; the full
type is `com.retailnetwork.storefront.customer-payments.<entity>.<EventName>`
([Events](/docs/api-reference/events)). Each diagram's `Source:` line names the code.

## 1. Checkout authorizes the payment, or voids it

```mermaid
sequenceDiagram
    autonumber
    participant CK as checkout
    participant CP as customer-payments
    participant PSP as payment provider
    participant K as Kafka events topic
    CK->>CP: cmd: AuthorizePayment<br/>POST /payment-authorizations
    CP->>PSP: cmd: Authorize<br/>PSP token, total, key
    alt the provider approves
        PSP-->>CP: reply hold id, expiresAt
        CP->>K: evt: paymentauthorization.PaymentAuthorized
        CP-->>CK: reply 201 AUTHORIZED<br/>allocations per group
        Note over CK: place the order (pivot)
    else the provider declines
        PSP-->>CP: reply declined (402)
        CP->>K: evt: paymentauthorization.PaymentDeclined
        CP-->>CK: reply 201 DECLINED<br/>session stays open
    end
    opt the place-order step is definitively rejected
        CK->>CP: cmd: VoidAuthorization<br/>POST ...:void
        CP->>PSP: cmd: Void
        CP->>K: evt: paymentauthorization.PaymentReleased VOIDED
        CP-->>CK: reply 200 VOIDED
    end
```

Source: `internal/application/usecases/authorization.go` (`AuthorizePayment`,
`VoidAuthorization`), `internal/adapters/inbound/http/handlers.go`,
`internal/adapters/outbound/paymentprovider/client.go`. Omitted: the outbox write and
relay (see [Sequence diagrams](/docs/ddd/sequence-diagrams)); a provider outage is a 503
to retry under the same `Idempotency-Key`.

## 2. A shipped group is captured

```mermaid
sequenceDiagram
    autonumber
    participant OS as order-sourcing
    participant K as Kafka
    participant CP as customer-payments
    participant PSP as payment provider
    OS->>K: evt: assignment.FulfillmentShipped
    K->>CP: evt: assignment.FulfillmentShipped
    Note over CP: ApplyFact: capture the group,<br/>ErrOverCapture, ErrCaptureAfterExpiry,<br/>ErrUnknownGroup
    CP->>PSP: cmd: Capture<br/>deterministic key
    PSP-->>CP: reply capture id
    Note over CP: allocation CAPTURED,<br/>tracking numbers stored
    CP->>K: evt: paymentauthorization.PaymentCaptured
```

Source: `internal/adapters/inbound/kafka/handlers.go` (`orderSourcingHandlers`),
`internal/application/usecases/consume.go` (`ApplyFact`), `facts.go` (`captureGroup`).

## 3. A fulfilment fact arrives before the order is bound

```mermaid
sequenceDiagram
    autonumber
    participant OS as order-sourcing
    participant CK as checkout
    participant K as Kafka
    participant CP as customer-payments
    participant PSP as payment provider
    OS->>K: evt: assignment.FulfillmentShipped
    K->>CP: evt: assignment.FulfillmentShipped
    Note over CP: no authorization bound to this orderId:<br/>park the fact
    CK->>K: evt: checkoutsession.CheckoutCompleted
    K->>CP: evt: checkoutsession.CheckoutCompleted
    Note over CP: BindOrder, then replay the parked facts
    CP->>PSP: cmd: Capture for the parked group
    PSP-->>CP: reply capture id
    CP->>K: evt: paymentauthorization.PaymentCaptured
```

Source: `internal/application/usecases/consume.go` (`CheckoutCompleted`, `ApplyFact`),
`facts.go` (`replayParked`), `internal/application/parked`. The same parking applies to
cancelled, unsourceable, lost and returned facts. A fact that stays unbound for 24 h is
published to `retailnetwork.customer-payments.unbound-facts.dlq`.

## 4. A cancelled group is released

```mermaid
sequenceDiagram
    autonumber
    participant OS as order-sourcing
    participant K as Kafka
    participant CP as customer-payments
    participant PSP as payment provider
    OS->>K: evt: assignment.FulfillmentCancelled<br/>orderId, groupNo
    K->>CP: evt: assignment.FulfillmentCancelled
    Note over CP: Release: allocation RELEASED
    CP->>PSP: cmd: ReduceAuthorization<br/>(Void when nothing is pending)
    CP->>K: evt: paymentauthorization.PaymentReleased<br/>reason CANCELLED
```

Source: `internal/application/usecases/facts.go` (`releaseGroup`, `shrinkHold`),
`internal/domain/paymentauthorization/commands.go` (`Release`). `FulfillmentUnsourceable`
runs the same flow with reason `UNSOURCEABLE`.

## 5. A lost or returned package is refunded

```mermaid
sequenceDiagram
    autonumber
    participant ST as shipping-and-tracking
    participant K as Kafka
    participant CP as customer-payments
    participant PSP as payment provider
    ST->>K: evt: journey.PackageLost or PackageReturned
    K->>CP: evt: journey.PackageLost or PackageReturned
    Note over CP: find the group by tracking number,<br/>RequestRefund: ErrOverRefund.<br/>Refund REQUESTED
    CP->>PSP: cmd: Refund<br/>deterministic key
    alt the provider refunds
        PSP-->>CP: reply refunded
        CP->>K: evt: refund.RefundIssued
    else the provider declines
        PSP-->>CP: reply failure code
        CP->>K: evt: refund.RefundFailed
    end
```

Source: `internal/application/usecases/facts.go` (`refundPackage`), `refund.go`
(`openRefund`, `settleRefund`), `internal/domain/refund`. A provider outage leaves the
refund `REQUESTED` and the `process-requested-refunds` policy retries it.

## 6. An authorization is re-authorized before it expires

```mermaid
sequenceDiagram
    autonumber
    participant CP as customer-payments
    participant PSP as payment provider
    participant K as Kafka
    Note over CP: policy loop: expires_at within 24 h,<br/>allocations still PENDING
    CP->>PSP: cmd: Reauthorize<br/>pending amount, key
    alt the provider renews
        PSP-->>CP: reply new hold id and expiry
        CP->>K: evt: paymentauthorization.PaymentReauthorized
    else the provider declines
        PSP-->>CP: reply declined
        CP->>K: evt: paymentauthorization.PaymentDeclined<br/>reason REAUTH_DECLINED
        CP->>K: evt: paymentauthorization.PaymentAuthorizationExpired
    end
```

Source: `internal/application/usecases/policies.go` (`ReauthorizeBeforeExpiry.renew`),
`cmd/api/policies.go`. Also on the loop: `void-unbound-authorizations` (no
`CheckoutCompleted` after 24 h: void).
