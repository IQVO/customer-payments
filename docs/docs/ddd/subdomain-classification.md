---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 3
description: "Why Customer Payments is a Generic subdomain of the Storefront area, and the classification of its neighbours."
---

# Subdomain classification

**Customer Payments is Generic**, in the Storefront area (tier `storefront`).
Business model: revenue collection ("buy a PSP"); evolution: commodity.

## Why Generic

- **Low differentiation.** A shopper does not choose this storefront because of
  how it authorizes a card. Collecting revenue is a necessity, and the right
  answer is to buy that capability from a payment provider and keep the domain
  code thin. The code reflects it: the provider sits behind an
  Anti-Corruption Layer port (`internal/application/ports/provider.go`,
  `internal/adapters/outbound/paymentprovider`) and the
  `payment-provider-simulator` is its stand-in; swapping it for a real provider
  changes only `PAYMENT_PROVIDER_URL` and that adapter.
- **High complexity for a Generic context.** The honest surprise
  ([Core domain chart](/docs/ddd/core-domain-chart)): the provider's clock (hold
  expiry, 24 h re-authorization lead), cross-topic event order (a shipment fact
  can precede `CheckoutCompleted`, so facts are parked), money invariants across
  two aggregates (over-capture, over-refund, allocation sum) and provider calls
  that must be idempotent by construction.
- **What is not generic here** is the pairing of the money lifecycle with the
  network's shipment groups (capture what ships, release what is cancelled).
  That is a rule, not a competitive edge.

What would move it: nothing in the current plan. A future
`customer-notifications` context would consume `PaymentCaptured`,
`PaymentReleased` and `RefundIssued`, turning today's analytics-only events into
integration events, but would not change the classification.

## The storefront classification (neighbours)

From warehouse-docs `storefront/docs/strategic-design/subdomain-classification.md`
on `main`
([storefront subdomain classification](https://iqvo.github.io/warehouse-docs/storefront/strategic-design/subdomain-classification)):

| Bounded context | Classification | Business model | Evolution | Relation to Customer Payments |
| --- | --- | --- | --- | --- |
| `pricing` | Core | Revenue (the price of speed per tier) | Custom-built | none directly; totals reach this context inside checkout's authorize call |
| `product-content` | Generic | Engagement enabler (buy a PIM) | Commodity | none |
| `product-discovery` | Supporting | Conversion enabler | Custom-built | none |
| `shopping-cart` | Supporting | Conversion | Custom-built | also consumes `CheckoutCompleted`, shares nothing else |
| `checkout` | Supporting | Conversion (the purchase transaction) | Custom-built | **caller** of the REST API and publisher of `CheckoutCompleted` |
| `customer-payments` | **Generic** | **Revenue collection (buy a PSP)** | **Commodity** | this context |

Count in the storefront: 1 Core, 3 Supporting, 2 Generic. The network
contexts this one conforms to are classified in the network documentation:
`order-sourcing` (Core) and `shipping-and-tracking` (Generic).

Deferred or later subdomains named by the storefront model: customer identity
(Generic, deferred), notifications, returns, promotions, reviews and
recommendations. The experience layer (`storefront-bff`, `storefront-web`) and the
`payment-provider-simulator` carry no domain model and are not bounded contexts.
