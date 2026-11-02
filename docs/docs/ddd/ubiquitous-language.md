---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
sidebar_position: 1
description: "The terms Customer Payments speaks, each mapped to its code identifier on develop, the neighbour terms it uses and the terms most often confused with them."
---

# Ubiquitous language

The terms this context speaks, mapped to the identifier in the code. Source:
`internal/domain/paymentauthorization`, `internal/domain/refund`,
`internal/domain/shared`, `internal/application/{parked,provider,usecases}`.
The storefront-wide glossary is on
[warehouse-docs](https://iqvo.github.io/warehouse-docs/storefront/strategic-design/ubiquitous-language).

## Own terms

| Term | Definition | Code identifier | Not to be confused with |
| --- | --- | --- | --- |
| **Payment Authorization** | A hold on the shopper's payment method for the quote total, split into allocations per shipment group. One live (non-declined) record per checkout session. | `paymentauthorization.PaymentAuthorization` | A charge: an authorization moves no money until an allocation is captured. |
| **Allocation** | The share of an authorization that belongs to one shipment group: `groupNo`, `amountMinor`, `captured_minor`, tracking numbers, state `PENDING`, `CAPTURED` or `RELEASED`. | `paymentauthorization.Allocation`, `AllocState`, `AllocationInput` | The checkout session's per-group amount, the same number before it reaches this context. |
| **Capture** | Taking an allocation's money when its shipment group ships. | `PaymentAuthorization.Capture`, use case `Consumer.ApplyFact` (kind `Shipped`) | **Authorize** (the hold at checkout). |
| **Release** | Letting an uncaptured allocation go when its group is cancelled or unsourceable. | `PaymentAuthorization.Release`, `ReasonCancelled`, `ReasonUnsourceable` | **Void**, which cancels the whole authorization. |
| **Void** | Cancelling the whole authorization before any capture. | `PaymentAuthorization.Void`, `usecases.VoidAuthorization`, `ReasonVoided` | **Release** (one allocation) and **Refund** (money already captured). |
| **Refund** | Returning captured money for a lost or returned package, or manually. | `refund.Refund`, `refund.Reason` (`LOST`, `RETURNED_TO_SENDER`, `MANUAL`) | **Void** and **Release**, which return a hold, not money. |
| **Reauthorization** | A fresh provider hold replacing one about to expire, for allocations not yet captured. | `PaymentAuthorization.Reauthorize`, `usecases.ReauthorizeBeforeExpiry`, event `PaymentReauthorized` | The first authorize at checkout (`AuthorizePayment`). |
| **Expiry** | The moment (`expiresAt`) after which the provider no longer honours the hold; no capture after it. | `PaymentAuthorization.Expire`, `ErrCaptureAfterExpiry`, state `EXPIRED` | Void (a choice) and Release (one group). |
| **Declined** | The provider refused the authorize attempt (or a re-authorization). The record is kept as a terminal `DECLINED` authorization and the checkout session stays open for another token. | state `DECLINED`, `PaymentDeclined`, `ReasonReauthDeclined` | A decline of the shopper's order: the order is not placed yet. |
| **Payment Method Token** | The PSP token standing in for the shopper's card; the storefront never sees a card number. | `AuthorizePaymentInput.PaymentMethodToken`, `provider.AuthorizeRequest.PaymentMethodToken` | A card number or any part of one. |
| **Bind** | Attaching `orderId` to the session's authorization when `CheckoutCompleted` arrives. | `PaymentAuthorization.BindOrder`, `Consumer.CheckoutCompleted` | Authorize: the authorization exists before the order does. |
| **Parked Fulfilment Fact** | A shipment, cancellation, unsourceable, lost or returned fact kept until its order is bound (or its tracking number is known); escalated after 24 h, purged after 30 days. | `parked.Fact`, `parked.Kind`, table `parked_facts` | A retry queue: parked facts wait for a bind, not for a failure. |
| **Provider Idempotency Key** | The key sent with every provider call, deterministic per (operation, `authorizationId`, `groupNo`, qualifier). | `usecases.Key` | The REST `Idempotency-Key` header, which dedupes the caller's request. |
| **Provider Hold** | The provider's side of an authorization: its id and validity. | `provider.Hold`, `providerAuthorizationId` | `authorizationId`, this context's own id. |
| **Refund states** | `REQUESTED`, `SUCCEEDED`, `FAILED`. A failed refund can be retried, at most 5 attempts in all. | `refund.State`, `refund.MaxAttempts` | The authorization states. |
| **Authorization states** | `AUTHORIZED`, `PARTIALLY_CAPTURED`, `CAPTURED`, `RELEASED`, `VOIDED`, `EXPIRED`, `DECLINED`. The live state is derived from the allocations. | `paymentauthorization.State` | `DECLINED` is the record of a failed attempt, not a state an authorization moves into. |
| **Outbox event** | A domain event already encoded as CloudEvents and stored in the same transaction as the aggregate. | `outbox.Message`, table `outbox_events` | The Kafka message itself, which exists only after the relay publishes it. |
| **Inbox claim** | A consumed CloudEvents `id` recorded in the same transaction as its effect. | `ports.Inbox.Claim`, table `processed_events` | The consumer group offset. |

## Neighbour terms this context uses

| Term | Owner | Where it appears here |
| --- | --- | --- |
| **Checkout Session** | `checkout` | `checkoutSessionId` on the authorization and on `CheckoutCompleted`. |
| **Place-Order Step** | `checkout` | `VoidAuthorization` is called only after a definitive rejection of it. |
| **Fulfillment Assignment**, **Shipment Group** | `order-sourcing` | `groupNo` joins an allocation to the network's group. |
| **Tracking Number** | `shipping-and-tracking` | stored on the allocation at capture; `PackageLost` / `PackageReturned` name it. |
| **Customer Order** | the network's order contexts | `orderId`, a reference only. |

## Fleet translation

None. Customer Payments never produces or consumes a `warehouse.*` topic: its
topics are `retailnetwork.*` ([Integration](/docs/ecosystem/integration)).
