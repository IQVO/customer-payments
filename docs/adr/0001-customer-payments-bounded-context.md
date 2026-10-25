# 1. Customer Payments is a bounded context of the storefront domain

Status: Accepted (bootstrap). Design source: warehouse-docs `storefront/model/model.yaml`, context `customer-payments`.

## Context

The storefront is the e-commerce domain of the retail-network organisation: six bounded contexts, each its own
service and database. Moves money with the shipment facts: authorize at checkout, capture each shipment group's allocation when it ships, release on cancellation, refund on lost or returned packages, re-authorize uncaptured allocations before the provider's authorization validity expires [9]. Speaks to the payment provider through an Anti-Corruption Layer port (Authorize, Capture, Void, Refund, Reauthorize); the payment-provider-simulator is the stand-in adapter and swapping it for a real provider changes nothing else. Never sees a card number, only PSP tokens [11][12].

## Decision

- This repo owns the aggregates PaymentAuthorization, Refund; the invariants and error names are those of the model.
- Business decisions of the context:
- One authorization per checkout session; a replayed authorize returns the existing one.
- Capture is per allocation (shipment group): zero captures without FulfillmentShipped, zero expired authorizations on shipped groups (section 8 correctness targets).
- Fulfilment facts for a not-yet-bound order are parked and applied on BindOrderOnCheckoutCompleted (cross-topic order, gap S4).
- Provider idempotency keys are deterministic per (operation, authorizationId, groupNo).
- Events use the storefront CloudEvents namespace `com.retailnetwork.storefront.customer-payments.<entity>.<Event>` on `retailnetwork.customer-payments.events`
  (analytics-only events on `.analytics`). Published: PaymentAuthorized, PaymentCaptured, PaymentReleased, PaymentAuthorizationExpired, RefundIssued, RefundFailed, PaymentDeclined. Consumed: CheckoutCompleted, FulfillmentCancelled, FulfillmentShipped, PackageLost, PackageReturned. No `warehouse.*` topic is consumed or produced.
- The storefront calls only the network's Open Host Service routes (model inbound rows for `shopper / storefront`),
  never an in-org or restricted route; sibling data used in a decision is a local event-fed read model, except the
  calls the model lists under `sync_calls`.
- No authentication on REST, consistent with the fleet and the network. This context stores no PII and no card data.

## Consequences

Relationships and the full consumed-event list are in the model and in the AsyncAPI contract added with the first feature PR.
