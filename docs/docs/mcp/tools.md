---
id: tools
title: MCP tools
sidebar_label: MCP tools
sidebar_position: 2
---

# MCP tools

`cmd/mcp` is a separate, read-only deployable ([Architecture](/docs/overview/architecture)).
It serves the Model Context Protocol over **Streamable HTTP only** (no stdio, no
SSE) at `/mcp` on `MCP_ADDR` (default `:8090`); `/` is mounted too, and
`GET /healthz` backs the probes. Through Kong the path is
`/api/mcp/customer-payments` (rewritten to `/mcp`) when the chart value
`mcp.route.enabled` is on; the fleet's `warehouse-infra` values leave the MCP
component off. There is **no authentication**: access control is the in-cluster
ClusterIP boundary.

The tools run the same read use cases as the REST `GET` routes. No write use case
is wired into the binary, so it can never insert into the outbox, and it never
dials Kafka or the payment provider (`TestMCPAdapterDependencyRule`, see
[Testing](/docs/development/testing)). The list below is the output of a live
`tools/list` against the binary built from this branch. All four tools carry the
annotations `readOnlyHint: true`, `idempotentHint: true`, `openWorldHint: false`.

| Tool | Input | Annotation | Returns |
| --- | --- | --- | --- |
| `get_payment_authorization` | `authorizationId` (string, **required**, e.g. `pay-3f9c1a2b4d5e`) | read-only | one authorization: `authorizationId`, `checkoutSessionId`, `orderId`, `state`, `currency`, `totalMinor`, `allocations[]` (`groupNo`, `amountMinor`, `state`, `trackingNumbers`), `expiresAt`, `declineReason`. An unknown id fails with `not-found`. |
| `list_payment_authorizations` | `orderId` (string, optional), `checkoutSessionId` (string, optional); give **exactly one** | read-only | `{authorizations: [...]}` of the same shape, oldest first; a session lists declined attempts too; an order or session with none yields an empty list. The tool follows every page of the REST list for you, up to 1000 rows |
| `get_refund` | `refundId` (string, **required**, e.g. `ref-3f9c1a2b4d5e`) | read-only | one refund: `refundId`, `authorizationId`, `orderId`, `groupNo`, `amountMinor`, `reason` (`LOST`, `RETURNED_TO_SENDER`, `MANUAL`), `state` (`REQUESTED`, `SUCCEEDED`, `FAILED`), `attempts`, `failureCode`. An unknown id fails with `not-found`. |
| `list_refunds` | `orderId` (string, **required**) | read-only | `{refunds: [...]}`, oldest first; the tool follows every page of the REST list for you, up to 1000 rows |

Authorization states: `AUTHORIZED`, `PARTIALLY_CAPTURED`, `CAPTURED`, `RELEASED`,
`VOIDED`, `EXPIRED`, `DECLINED`; allocation states: `PENDING`, `CAPTURED`,
`RELEASED`. All inputs have `additionalProperties: false`. The registry is pinned
by `internal/adapters/inbound/mcp/testdata/tool_registry.golden.json`; the
tool definitions are in `internal/adapters/inbound/mcp/tools.go`.

## Calling it

```sh
# 1. initialize: the response carries the session id in the Mcp-Session-Id header
curl -s -i -X POST localhost:8090/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"doc","version":"0"}}}'

# 2. notifications/initialized, then tools/list or tools/call with the same header
curl -s -X POST localhost:8090/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'Mcp-Session-Id: <session id>' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

The server replies as a server-sent `message` event whose `data:` line is the
JSON-RPC response. The `initialize` result states its purpose: customer payments
of the digital storefront, read-only, authorization and refund lookups. A
`tools/call` looks like:

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_payment_authorizations","arguments":{"checkoutSessionId":"cs-demo-2"}}}
```

When an `orderId` or `checkoutSessionId` has no authorization, `list_*` return an
empty array, not an error; ids are matched exactly (a checkout session id is not
an order id; the order is bound by the `CheckoutCompleted` event). Failures come back as an `isError` tool result whose text is `<slug>: <detail>`,
with the REST slugs `not-found` and `validation-failed` (the "exactly one of"
rule); anything else is logged and reported as `internal-error` without
infrastructure details.

For delegation instead of tool calls, the same questions are answered by the
[A2A agent](/docs/api-reference) (`get_payment_status`, `get_refund_status`).
