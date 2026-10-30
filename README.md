# customer-payments

Storefront (e-commerce) domain of the retail-network organisation, bounded context **Customer Payments** (Generic, storefront tier).

Moves money with the shipment facts: authorize at checkout, capture each shipment group's allocation when it ships, release on cancellation, refund on lost or returned packages, re-authorize uncaptured allocations before the provider's authorization validity expires [9]. Speaks to the payment provider through an Anti-Corruption Layer port (Authorize, Capture, Void, Refund, Reauthorize); the payment-provider-simulator is the stand-in adapter and swapping it for a real provider changes nothing else. Never sees a card number, only PSP tokens [11][12].

Status: under construction. The design lives in [warehouse-docs](https://iqvo.github.io/warehouse-docs/storefront/) `storefront/model/model.yaml` (single source of truth).
This repository is part of the storefront domain: it consumes the retail network's Open Host Service routes and its
published `retailnetwork.*` events; it never calls an in-org or restricted network route.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message
(`com.retailnetwork.storefront.customer-payments.<entity>.<Event>`), transactional outbox.

## Observability

Logs, metrics and traces follow the fleet standard ([ADR 0004](docs/adr/0004-fleet-observability-standard.md)): JSON logs on stdout carry `trace_id`/`span_id`
and a `request_id`; `cmd/api` and `cmd/mcp` export OTLP/gRPC to the Collector (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `SERVICE_VERSION`, `ENVIRONMENT`;
export never blocks, so no Collector costs telemetry, nothing else) with HTTP request duration, Go runtime metrics and the business counter
`payments.authorizations.decided` (`outcome`, `reason`); the trace continues across Kafka in the `traceparent` header of each published event and is picked up by the
consumers.

## Agent interfaces (MCP and A2A)

Both are optional, read-only, unauthenticated (fleet-wide decision) and off by default in the chart; neither can move money.

- `cmd/mcp` (`internal/adapters/inbound/mcp`): the four read tools `get_payment_authorization`, `list_payment_authorizations`,
  `get_refund`, `list_refunds` over Streamable HTTP on `/mcp` (`MCP_ADDR`, default `:8090`).
- `cmd/a2a` ([ADR 0005](docs/adr/0005-a2a-inbound-adapter.md), `internal/adapters/inbound/a2a`): an Agent2Agent agent that
  accepts a **delegated goal** as a message and answers in-band. Skills: `get_payment_status` (by `authorizationId`,
  `orderId` or `checkoutSessionId`) and `get_refund_status` (by `refundId` or `orderId`). JSON-RPC at `/` and `/a2a`, Agent
  Card at `/.well-known/agent-card.json`, `A2A_ADDR` default `:8093`, `A2A_AGENT_URL` (absolute http(s) URL advertised on the
  card) is mandatory. Problems (404/400/422/500) travel in the reply envelope, never as JSON-RPC errors.

Facts still travel only as CloudEvents on Kafka; MCP carries tools, A2A carries delegation, never data sync.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

See `docs/adr/` for decisions and `docs/before-first-release.md` for the packaging checklist.
