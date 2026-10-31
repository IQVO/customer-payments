# ADR 0005: A2A inbound adapter

## Status

Accepted (2026-10-30). Follows [ADR 0003](./0003-cloudevents-envelope-and-type-catalogue.md) (CloudEvents on every
Kafka message) and [ADR 0004](./0004-fleet-observability-standard.md) (the telemetry contract every binary shares). It is
modelled on the read-only MCP adapter of this context (`internal/adapters/inbound/mcp`, charter in its server
instructions: "Nothing here changes data"; the additive-adapter and no-auth rules in
`.claude/rules/fleet/no-auth-and-mcp.md`).

## Context

The fleet is adopting the Agent2Agent (A2A) protocol so that one agent can **delegate** a goal to another context's
agent. customer-payments already exposes its reads over REST and MCP. Three transports now carry three different things
and must not overlap:

| Transport | Carries |
| --- | --- |
| Kafka + CloudEvents | **facts** between contexts (ADR 0003) |
| MCP | an agent's **tools** |
| A2A | **delegation** of a goal to another context's agent, never data sync |

This context moves money. The MCP surface is entirely read-only: four tools (`get_payment_authorization`,
`list_payment_authorizations`, `get_refund`, `list_refunds`) and no write. Authorizing, voiding and refunding stay on REST
and on the event consumers. An A2A skill may change state only if the context's MCP surface already offers that write,
and it offers none, so the A2A agent is read-only and never widens what the context exposes.

## Decision

- **A third driving adapter, a separate binary.** `internal/adapters/inbound/a2a` is built on the official
  `github.com/a2aproject/a2a-go/v2` SDK (pinned at v2.7.0) and served by `cmd/a2a`: JSON-RPC 2.0 over HTTP at `/` and
  `/a2a`, the Agent Card at `/.well-known/agent-card.json`, an open `/healthz`, port **8093** (8080 is the api, 8090 the
  MCP server). The binary is a clone of `cmd/mcp`'s composition: the same Postgres repositories and the same embedded boot
  migrations.
- **Two curated, goal-shaped skills, both reads**, folding the four MCP tools into two questions:
  - `get_payment_status` (`authorizationId` | `orderId` | `checkoutSessionId`, exactly one): where a payment stands, with
    state, total, allocations per shipment group, expiry and decline reason;
  - `get_refund_status` (`refundId` | `orderId`, exactly one): how a refund went, with state, reason, group, attempts and
    failure code.

  Unknown order or session ids answer an empty list (as REST and MCP do); an unknown authorization or refund id answers a
  `404` problem.
- **Money and card data never cross the boundary.** The answer DTOs are the REST and MCP fields and none more: no provider
  authorization id, no PSP token, no payment instrument. A test marshals real answers and asserts none of those appear.
- **Message-only, answered in-band.** Both skills are request/response reads, so the agent answers one message with one
  message (no Task lifecycle). Malformed requests, unknown skills, bad arguments and domain rejections are answered as
  RFC 7807-style `problem`s inside the reply envelope, never raised as JSON-RPC errors, with the REST adapter's slugs and
  status classes for the errors a read can raise: `404` not-found (including an id longer than 128 characters, exactly like
  `GET /payment-authorizations/{id}`), `400` malformed-request (an unknown or trailing JSON field, an over-long listing key,
  a missing skill), `400` unknown-skill, `422` validation-failed (zero or several selectors, the REST list operations' answer
  for the same mistake) and `500` anything unexpected. The detail is adapter-owned text; the cause of an unexpected failure
  stays in the log. The REST `409` and `503` classes belong to writes and to the payment provider, which this agent does not
  hold.
- **Strict input.** The request decodes with `DisallowUnknownFields` and rejects trailing data, so a misspelled argument
  fails loudly instead of being ignored.
- **Read-only is structural.** The adapter's `Deps` holds the four read use cases and nothing that can write; `NewDeps`
  takes the two read repositories only, so `cmd/a2a` never names the shared `usecases.Deps` bundle's write seams (unit of
  work, outbox, payment provider). `cmd/a2a` has no `KAFKA_BROKERS`, no `EVENT_PUBLISHER`, no consumer groups, never starts
  the outbox relay, the three consumers or the policy loop, never calls the payment provider and calls no sibling context. It
  contains **no outbound A2A client**. `internal/architecture/a2a_fitness_test.go` pins the dependency rule (application and
  domain only, nothing depends on the adapter), the absence of write use cases and write-capable seams (outbox, provider,
  Kafka, REST, memory doubles) and of outbound clients, and that the SDK is imported by the adapter and its command only.
- **Observability.** One OTel span per skill (`a2a.skill <id>`, attribute `a2a.skill`), and `cmd/a2a` exports through the
  ADR 0004 contract (`OTEL_SERVICE_NAME` defaults to `customer-payments-a2a`).
- **Unauthenticated, like REST and MCP.** The card declares no security schemes: this surface is unauthenticated by the same
  fleet-wide decision. Access control is the in-cluster ClusterIP boundary. Schemes are added to the card, not a redesign,
  if that decision is ever reversed.
- **Boot.** `DATABASE_URL` and `A2A_AGENT_URL` are mandatory; the latter must be an absolute http(s) URL, advertised on the
  card, and the process refuses to boot otherwise, naming the variable, before telemetry, any connection and the listener.
  `A2A_ADDR` (default `:8093`), `MIGRATIONS_DATABASE_URL`, `LOG_LEVEL`, `SERVICE_VERSION` and `ENVIRONMENT` complete the
  contract.
- **Chart.** The `a2a.*` block (default **off**) renders `<release>-a2a`: a Deployment (`command: ["/app/a2a"]`), a ClusterIP
  Service on 8093 and an optional gateway HTTPRoute (`a2a.route.enabled`, `/api/a2a/customer-payments` rewritten to `/`).
  `A2A_AGENT_URL` defaults to `http://<release>-a2a.<ns>.svc.cluster.local:8093/` and is overridden through `a2a.agentUrl`.
  The render test asserts the mandatory-env scan, the absence of Kafka env and the route rewrite; the selector test asserts
  each Service selects exactly one Deployment.

## Consequences

- An agent can delegate "where does this payment stand" and "how did this refund go" without a bespoke REST client, over
  the same use cases as REST and MCP.
- No state changes through A2A here, so there is nothing to make idempotent and no outbox row can leave this binary; facts
  still leave from `cmd/api` as CloudEvents.
- A write skill (authorize, void, refund) would be an amendment of this ADR and would first need an MCP write; it would go
  through the same use case and transactional outbox as REST, and a money-moving write needs an explicit decision about
  idempotency and confirmation first, since the endpoints are unauthenticated.
- A fourth deployable (`/app/a2a`) in the same image; the Dockerfile builds every `cmd/*` directory and exposes 8093.
