---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Customer Payments

**Customer Payments** (`customer-payments`) is a bounded context of the
**Storefront** area of the warehouse-systems fleet. Its subdomain class is
**Generic** (see [Subdomain classification](/docs/ddd/subdomain-classification)).
It moves money with the shipment facts: it authorizes the quote total at
checkout, splits the hold into one allocation per shipment group, **captures**
each group when order-sourcing reports it shipped, **releases** it when the
group is cancelled or unsourceable, **refunds** it when shipping-and-tracking
reports the package lost or returned, and **re-authorizes** uncaptured
allocations before the provider's authorization validity runs out. It talks to
the payment provider only through an Anti-Corruption Layer port (Authorize,
Capture, Void, Refund, Reauthorize, Reduce) and never sees a card number, only
PSP tokens.

## What it owns

| Concept | Where | Notes |
| --- | --- | --- |
| `PaymentAuthorization` aggregate | `internal/domain/paymentauthorization` | One per checkout session (declined attempts are kept as separate `DECLINED` records). States `AUTHORIZED`, `PARTIALLY_CAPTURED`, `CAPTURED`, `RELEASED`, `VOIDED`, `EXPIRED`, `DECLINED`; allocations per shipment group in `PENDING`, `CAPTURED`, `RELEASED`; invariants over-capture, capture-after-expiry, allocation sum equals total. |
| `Refund` aggregate | `internal/domain/refund` | `REQUESTED`, then `SUCCEEDED` or `FAILED`; reasons `LOST`, `RETURNED_TO_SENDER`, `MANUAL`; refunds of an order never exceed what was captured; at most 5 provider attempts. |
| Parked fulfilment facts | `parked_facts` table, `internal/application/parked` | Shipment, cancellation, lost and returned facts that arrive before `CheckoutCompleted` binds the order are kept, applied on the bind, escalated after 24 h and purged after 30 days. |
| The provider port | `internal/application/ports/provider.go`, `internal/adapters/outbound/paymentprovider` | The Anti-Corruption Layer; deterministic provider idempotency keys per (operation, authorization, group). |
| The event stream `retailnetwork.customer-payments.events` | `apis/asyncapi.yaml` | Eight CloudEvents types ([Events](/docs/api-reference/events)). |
| REST, MCP and A2A read surfaces | `apis/openapi.yaml`, `cmd/mcp`, `cmd/a2a` | See [API reference](/docs/api-reference). |

## What it does NOT own

| Not owned | Owner |
| --- | --- |
| The checkout session, the purchase saga, the decision to authorize or to void after a rejected place-order step | `checkout` (it calls this context's REST API and publishes `CheckoutCompleted`) |
| Prices, the binding quote total | `pricing`, carried into the authorization by `checkout` |
| Whether a group ships, is cancelled or is unsourceable | `order-sourcing` (network) |
| Whether a package is lost or returned | `shipping-and-tracking` (network) |
| The order record, ship-to address and shopper PII | the network's order contexts; this context stores references only |
| Card capture and card data | the external payment provider (the `payment-provider-simulator` stands in for it in development) |
| Notifying the shopper, vendor payouts and settlement, identity | other or future contexts |

## Main capabilities

- **Authorize** a checkout session's total with allocations per shipment group
  (`POST /payment-authorizations`, idempotent, decline recorded as a result).
- **Void** an uncaptured authorization, on checkout's request or by the
  24-hour backstop for authorizations that no `CheckoutCompleted` ever bound.
- **Capture per shipped group** and **release per cancelled group**, driven by
  Kafka events, never by REST writes; partial release shrinks the provider hold.
- **Refund** lost or returned packages automatically, or on request
  (`POST /refunds`), with retry of failed refunds
  (`POST /refunds/{refundId}:retry`) and an alert when the fifth attempt fails.
- **Re-authorize** before expiry; a provider decline expires the authorization
  and raises `PaymentDeclined` with reason `REAUTH_DECLINED`.
- **Publish** every decision through a transactional outbox as CloudEvents 1.0.
- **Answer questions** about where a payment or refund stands, over REST, MCP
  tools and an A2A agent (read-only, unauthenticated).

## Where to go next

| You want to | Read |
| --- | --- |
| Run it locally and make the first calls | [Quickstart](/docs/overview/quickstart) |
| See how the code is laid out and which binary does what | [Architecture](/docs/overview/architecture) |
| Configure, deploy or operate it | [Configuration](/docs/operations/configuration), [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability), [Troubleshooting](/docs/operations/troubleshooting) |
| Test or change it | [Testing](/docs/development/testing) |
| Know every command and policy | [Use cases](/docs/ddd/use-cases) |
| See the neighbours and contracts | [Integration](/docs/ecosystem/integration), [API reference](/docs/api-reference), [Events](/docs/api-reference/events), [MCP tools](/docs/mcp/tools) |
| Read the domain model | [Ubiquitous language](/docs/ddd/ubiquitous-language), [DDD artifacts](/docs/ddd/ddd-artifacts) |
| Understand the decisions | [ADR index](/docs/adr) |

## Boundary rule

This repository is part of the Storefront domain. It consumes the retail
network's published `retailnetwork.*` events and calls no route of any sibling
context: its only synchronous outbound call is to the payment provider. See
[Integration](/docs/ecosystem/integration).
