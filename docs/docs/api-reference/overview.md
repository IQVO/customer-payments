---
id: overview
title: API overview
sidebar_label: Overview
slug: /api-reference
sidebar_position: 1
---

# API overview

:::info Implemented by cmd/api, cmd/mcp and cmd/a2a
`cmd/api` serves the REST routes below (`internal/adapters/inbound/http/server.go`)
on `HTTP_ADDR` (default `:8080`). The contracts are `apis/openapi.yaml` and
`apis/asyncapi.yaml`, pinned by [ADR 0001](/docs/adr/0001-customer-payments-bounded-context)
and [ADR 0003](/docs/adr/0003-cloudevents-envelope-and-type-catalogue). See the
[Quickstart](/docs/overview/quickstart) for real responses. Where the code and
the spec disagree, the code is documented here and the difference is listed
under *Spec versus code* below.
:::

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, spec version 0.1.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. Server named in the spec: `http://localhost:8080`; through Kong in the kind cluster the base path is `/api/customer-payments` (port 8000). |
| Events | `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0) | The [event catalogue](/docs/api-reference/events): eight published CloudEvents types, six consumed. |
| MCP | `internal/adapters/inbound/mcp`, `cmd/mcp` | Streamable HTTP on `/mcp` (port 8090; through Kong `/api/mcp/customer-payments`, rewritten to `/mcp`). Four read-only tools, no write tool: `get_payment_authorization`, `list_payment_authorizations`, `get_refund`, `list_refunds`. See [MCP tools](/docs/mcp/tools). No auth, by fleet decision. |
| A2A | [ADR 0005](/docs/adr/0005-a2a-inbound-adapter), `internal/adapters/inbound/a2a`, `cmd/a2a` | JSON-RPC 2.0 on `/` and `/a2a` (port 8093; through Kong `/api/a2a/customer-payments`, rewritten to `/`), Agent Card at `/.well-known/agent-card.json`. Two read-only skills: `get_payment_status` (exactly one of `authorizationId`, `orderId`, `checkoutSessionId`) and `get_refund_status` (exactly one of `refundId`, `orderId`). Method `SendMessage`, header `A2A-Version: 1.0`. Domain rejections travel in the reply envelope as problem documents, never as JSON-RPC errors. No auth, by fleet decision. |

## Endpoints (`cmd/api`)

| Tag | Endpoint | Operation id | Success | In the spec? |
| --- | --- | --- | --- | --- |
| Authorizations | `POST /payment-authorizations` | `authorizePayment` | `201` with the authorization this call created (also a stored `DECLINED` one); `200` when the checkout session already holds a live authorization, which is returned unchanged and the provider is not called | yes (only `201` is documented) |
| Authorizations | `GET /payment-authorizations` | `listPaymentAuthorizations` | `200 {authorizations: [...]}`; exactly one of `orderId` or `checkoutSessionId` is required (`422` otherwise); a session lists its declined attempts too | yes |
| Authorizations | `GET /payment-authorizations/{authorizationId}` | `getPaymentAuthorization` | `200`; `404 not-found` | yes |
| Authorizations | `POST /payment-authorizations/{authorizationId}:void` | `voidPaymentAuthorization` | `200`, state `VOIDED`, allocations `RELEASED`; `409 illegal-transition` after a capture | yes |
| Refunds | `POST /refunds` | `requestRefund` | `201`, the refund (possibly still `REQUESTED` after a provider outage); body `orderId`, `amountMinor`, `reason` (`LOST`, `RETURNED_TO_SENDER`, `MANUAL`), optional `groupNo` (default 0 = order level) | yes (without `groupNo`) |
| Refunds | `GET /refunds?orderId=` | `listRefunds` | `200 {refunds: [...]}`, oldest first; `orderId` required | yes |
| Refunds | `GET /refunds/{refundId}` | none | `200`; `404 not-found` | **no** |
| Refunds | `POST /refunds/{refundId}:retry` | none | `200`; moves a `FAILED` refund back to `REQUESTED` and tries the provider again (at most 5 attempts in all) | **no** |
| Health | `GET /healthz` | none | `200 {"status":"ok"}` | no |
| Health | `GET /readyz` | none | `200 {"status":"ready"}`, or `503` (`dependency-unavailable` naming `postgres` / `kafka`, or `{"status":"not_ready"}` while draining) | no |
| Metrics | `GET /metrics` | none | Prometheus text, one gauge `customer_payments_parked_facts` | no |

`cmd/mcp` and `cmd/a2a` each add `GET /healthz`; the Agent Card is served by `cmd/a2a`.

## Conventions in the contract

- **Idempotency-Key.** Required on every `POST` (the four write routes); 1 to
  128 characters. The first response below 500 is stored per `(route, key)`; a
  repeat of the same request replays it with `Idempotent-Replayed: true`; the
  same key with a different request is `422 idempotency-key-reused`; a missing
  or over-long key is `400 idempotency-key-required`. 5xx answers are never
  stored. Provider calls carry their own deterministic keys per
  (operation, authorization, group), so a retried request cannot move money twice.
- **Money** is integer minor units (`totalMinor`, `amountMinor`) with an ISO 4217
  `currency` (`^[A-Z]{3}$`). Card numbers never appear: `paymentMethodToken` is a
  PSP token.
- **Errors** are RFC 9457 `application/problem+json` with
  `type = /problems/<slug>`, `title`, `status`, `detail`, `instance`. Slugs:
  `malformed-request` (400), `idempotency-key-required` (400), `not-found` (404),
  `duplicate-authorization`, `over-capture`, `capture-after-expiry`,
  `order-already-bound`, `over-refund`, `concurrent-update`, `illegal-transition`
  (409), `idempotency-key-reused`, `payment-declined`, `allocation-mismatch`,
  `unknown-group`, `negative-amount`, `validation-failed` (422),
  `payment-provider-unavailable` (503), `internal-error` (500). Causes and
  remedies: [Troubleshooting](/docs/operations/troubleshooting#rest-errors).
- **A decline is a result, not an error.** `POST /payment-authorizations`
  answers `201` with `state: DECLINED` and a `declineReason`.
- **Request limits.** A NUL character (U+0000) in the path, query or body is a
  uniform client error, never a database error; unknown JSON fields are rejected
  as `malformed-request`.
- **Auth: none.** The fleet reverted REST, MCP and A2A auth on 2026-09-11, and
  `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns under
  `internal/adapters/inbound`. Access control is the network boundary
  (ClusterIP, the gateway).
- **Captures, releases and re-authorizations are not REST writes.** They arrive
  as Kafka events or run in the policy loop. Facts travel only as CloudEvents;
  MCP carries read tools and A2A carries delegation, never data sync.

## Spec versus code

Documented here, not fixed (the specs are out of scope for this documentation
change):

| Topic | `apis/openapi.yaml` | Code |
| --- | --- | --- |
| `POST /payment-authorizations` for a session with a live authorization | `201`, `409`, `422`, `503` | additionally `200` with the existing authorization (`internal/adapters/inbound/http/handlers.go`, `handleAuthorize`); a repeat with a *different* key returns the existing one rather than `409 duplicate-authorization` |
| Authorization `state` enum | `AUTHORIZED`, `PARTIALLY_CAPTURED`, `CAPTURED`, `VOIDED`, `EXPIRED`, `DECLINED` | also `RELEASED` (every allocation released by cancellation; the database `CHECK` and the domain allow it) |
| `GET /refunds/{refundId}`, `POST /refunds/{refundId}:retry` | absent | served by the router |
| Refund body `groupNo`; refund response `authorizationId`, `groupNo`, `attempts`, `failureCode`, `providerRefundId` | absent from `RefundRequest` / `Refund` | accepted and returned (`internal/adapters/inbound/http/dto.go`) |
| `/healthz`, `/readyz`, `/metrics` | absent | served by the router |
