# Cross-service integration events (Kafka)

This service PUBLISHES eight events to `retailnetwork.customer-payments.events` and CONSUMES
six events from checkout, order-sourcing and shipping-and-tracking. The full catalogue and
envelope are in `docs/adr/0003-cloudevents-envelope-and-type-catalogue.md`; the contract is
`apis/asyncapi.yaml`. See `fleet/cloudevents.md` for the envelope rule (CloudEvents 1.0
structured JSON, built and decoded only through `internal/adapters/kafka/cloudevents`).

## Publishing

- Events are written to the transactional outbox in the same transaction as the aggregate
  (`internal/adapters/outbound/postgres/outbox.go`); the always-on relay drains them through
  `internal/adapters/outbound/kafka/sink.go`. The CloudEvents `id` is minted once and stored.
- One golden JSON file per published type lives in `internal/adapters/outbound/kafka/testdata`.
- A breaking payload change is a new `.v2` type and dataschema, never a mutation.
  `TestEventCatalogueMatchesContract` keeps the ADR and `apis/asyncapi.yaml` in step.

## Consuming

- Consumers live in `internal/adapters/inbound/kafka`; each dedupes on the CloudEvents `id`
  through the inbox, dispatches on the FULL type string, dead-letters poison messages.
- Consumer group ids come from mandatory env vars (never a literal at the call site).
- Fulfilment and package facts for an unbound order are parked, then applied on bind.

### Published types

| `type` | `subject` | `dataschema` |
| --- | --- | --- |
| `com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized` | authorizationId | `urn:retailnetwork:customer-payments:events:PaymentAuthorized:v1` |
| `...paymentauthorization.PaymentCaptured` | authorizationId | `...:events:PaymentCaptured:v1` |
| `...paymentauthorization.PaymentReleased` | authorizationId | `...:events:PaymentReleased:v1` |
| `...paymentauthorization.PaymentAuthorizationExpired` | authorizationId | `...:events:PaymentAuthorizationExpired:v1` |
| `...paymentauthorization.PaymentDeclined` | authorizationId | `...:events:PaymentDeclined:v1` |
| `...paymentauthorization.PaymentReauthorized` | authorizationId | `...:events:PaymentReauthorized:v1` |
| `...refund.RefundIssued` | refundId | `...:events:RefundIssued:v1` |
| `...refund.RefundFailed` | refundId | `...:events:RefundFailed:v1` |

### Consumed types

| `type` | topic | producer |
| --- | --- | --- |
| `com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted` | `retailnetwork.checkout.events` | checkout |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped` | `retailnetwork.order-sourcing.events` | order-sourcing |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled` | `retailnetwork.order-sourcing.events` | order-sourcing |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable` | `retailnetwork.order-sourcing.events` | order-sourcing |
| `com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost` | `retailnetwork.shipping-and-tracking.events` | shipping-and-tracking |
| `com.retailnetwork.logistics.shipping-and-tracking.journey.PackageReturned` | `retailnetwork.shipping-and-tracking.events` | shipping-and-tracking |
