---
id: integration
title: Integration
sidebar_label: Integration
sidebar_position: 1
description: "Every upstream and downstream of Customer Payments: protocol, contract, failure behaviour, and the edges that do not exist."
---

# Integration

Customer Payments is part of the Storefront domain. It consumes the retail
network's published `retailnetwork.*` events and calls **no route of any sibling
context**; its only synchronous call out is to the payment provider. It never
produces or consumes a `warehouse.*` topic. Kafka is the fleet's single broker.
The context map is on [Context map](/docs/ddd/context-map).

## Summary

| Neighbour | Direction | Protocol | Contract | Failure behaviour |
| --- | --- | --- | --- | --- |
| `checkout` | calls us | REST (through Kong `/api/customer-payments` or in-cluster Service port 80) | `POST /payment-authorizations`, `POST /payment-authorizations/{authorizationId}:void`, `GET /payment-authorizations/{authorizationId}` in `apis/openapi.yaml` | `503 payment-provider-unavailable`: retry with the same `Idempotency-Key`; replays return the stored answer |
| `checkout` | we consume | Kafka `retailnetwork.checkout.events` | `CheckoutCompleted` | transient failure: retry then `<topic>.dlq`; invalid event: dead-letter at once |
| `order-sourcing` | we consume | Kafka `retailnetwork.order-sourcing.events` | `FulfillmentShipped`, `FulfillmentCancelled`, `FulfillmentUnsourceable` | as above; unbound order: parked |
| `shipping-and-tracking` | we consume | Kafka `retailnetwork.shipping-and-tracking.events` | `PackageLost`, `PackageReturned` | as above; unknown tracking number: parked |
| payment provider | we call | HTTP/JSON at `PAYMENT_PROVIDER_URL` | `POST /authorizations`, then `POST /authorizations/{id}/void`, `/captures`, `/refunds`, `/reauthorize`, `/reduce` (ADR 0002, assumption A12) | 10 s timeout; 402 = decline, 429 and 5xx = unavailable; deterministic idempotency key on every call |
| Storefront ops (humans, agents) | call us | REST, MCP, A2A | [API overview](/docs/api-reference) | n/a |
| analytics / other consumers | read our events | Kafka `retailnetwork.customer-payments.events` | [Events](/docs/api-reference/events) | at-least-once, dedupe on the CloudEvents `id` |

## Upstream: who we depend on

### checkout (events)

`retailnetwork.checkout.events`, type
`com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted`
(`sessionId`, `orderId`, `authorizationId`). It binds the order to the
authorization the checkout created earlier through our REST API, and releases any
parked fulfilment facts of that order. Without it an authorization stays unbound:
after 24 h the `void-unbound-authorizations` policy voids it, and shipment facts
for the order are parked and eventually escalated. Consumer group:
`CHECKOUT_CONSUMER_GROUP`.

### order-sourcing

`retailnetwork.order-sourcing.events`:
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped` (capture
the group, store tracking numbers), `...FulfillmentCancelled` and
`...FulfillmentUnsourceable` (release the group). Conformist: the payloads are
taken as published; unknown fields are ignored. Consumer group:
`ORDER_SOURCING_CONSUMER_GROUP`.

### shipping-and-tracking

`retailnetwork.shipping-and-tracking.events`:
`com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost` and
`...PackageReturned` (`orderId`, `trackingNumber`) trigger a refund of the group
that carried the tracking number. Conformist. Consumer group:
`SHIPPING_AND_TRACKING_CONSUMER_GROUP`.

### Consumer behaviour (all three)

- Structured CloudEvents 1.0 only; anything else is logged at WARN and committed
  past. Dispatch is on the full `type`; unknown types are skipped.
- At-least-once with an atomic effect: the CloudEvents `id` is claimed in
  `processed_events` and the effect is applied in one transaction; the offset is
  committed only afterwards.
- A transient failure is retried with backoff (200 ms doubling to 5 s) up to 5
  attempts, then published to `<topic>.dlq` with `x-dlq-*` headers. Facts that can
  never be applied (invalid event, unknown group, capture after expiry, illegal
  transition, order bound elsewhere) are dead-lettered at once.
- Facts for an order not yet bound are **parked**, not failed: they wait in
  `parked_facts`, are applied on `CheckoutCompleted`, escalated to
  `retailnetwork.customer-payments.unbound-facts.dlq` after 24 h and purged after 30
  days.
- A new consumer group id starts at the first offset and replays the whole topic.
- The service creates no topic; the broker auto-creates on first write and
  `/readyz` waits for no topic. A topic whose producer has not published yet is
  simply silent.

### The payment provider

`internal/adapters/outbound/paymentprovider` is the Anti-Corruption Layer. Only the
PSP token crosses it, never a card number. Every call carries
`Idempotency-Key: <operation>:<authorizationId>:<groupNo>[:<qualifier>]`, so a retried
authorize, capture, void, refund or reauthorize cannot move money twice. Mapping:
2xx decoded, **402 = definitive decline** (code from the body, default `DECLINED`),
timeout / connection failure / 429 / 5xx = `provider.ErrUnavailable`, any other status
is a contract error. The response body is limited to 1 MiB. In development and in the
kind cluster the provider is the `payment-provider-simulator`
(`http://payment-provider-simulator.warehouse-systems.svc.cluster.local:80`); its test
tokens (`tok_approve`, `tok_decline`, `tok_timeout`, `tok_expire-early`) give
deterministic outcomes. Swapping in a real PSP changes `PAYMENT_PROVIDER_URL` and this
package only. Outage behaviour: authorization fails with 503 and stores nothing; a
refund stays `REQUESTED` and is retried by the policy loop; a capture or release
fact is retried by the consumer (transient) and finally dead-lettered.

## Downstream: who depends on us

### checkout (REST)

Checkout authorizes the quote total before it places the order and voids only after
a definitive rejection of the place-order step. It sends `Idempotency-Key` values
that are stable per logical request; a decline is a `201` result, not an error.
The caller is `checkout/internal/adapters/outbound/customerpayments` (checked in
the local checkout of that repository).

### Events: no consumers today

`retailnetwork.customer-payments.events` carries eight CloudEvents types. The asyncapi
contract states they are analytics-only by design; in the repositories checked
(checkout, storefront-bff, customer-ordering, order-sourcing, shipping-and-tracking,
warehouse-ops-agent) nothing consumes the topic. A future `customer-notifications`
context would be the first consumer of `PaymentCaptured`, `PaymentReleased` and
`RefundIssued`.

### MCP and A2A

`cmd/mcp` (four read tools) and `cmd/a2a` (two read skills) answer questions about
payments and refunds for agents. Neither can move money, neither dials Kafka or the
provider, and neither is authenticated (fleet-wide). The fleet's `warehouse-infra`
values enable the A2A agent (route `/api/a2a/customer-payments`) and leave the MCP
component off. See [MCP tools](/docs/mcp/tools).

## Edges that do not exist

- No call to `customer-ordering`, `order-sourcing`, `shipping-and-tracking`,
  `pricing` or any other context; the order record, ship-to and shopper data are
  never read (references only).
- No restricted or in-org network route is called; only published events are
  consumed.
- No outbound A2A or MCP client (`TestA2AServerIsReadOnlyAndHasNoOutboundClient`).
- No Kafka in `cmd/mcp` and `cmd/a2a`.
- No consumer of our own events inside the repository.
- No authentication on any inbound edge.
