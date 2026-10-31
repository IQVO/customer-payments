---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Symptom, cause, check, fix, for the failure modes the code can produce. Log
messages are quoted exactly as the binaries write them. For the deployed shape
and routine procedures see the [Runbook](/docs/operations/runbook); for every
variable see [Configuration](/docs/operations/configuration).

## The process does not start

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| `cmd/api` exits 1 at once: `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode` | mandatory variable missing | the error names the variable; they are checked in the order `DATABASE_URL`, `KAFKA_BROKERS`, `PAYMENT_PROVIDER_URL`, `CHECKOUT_CONSUMER_GROUP`, `ORDER_SOURCING_CONSUMER_GROUP`, `SHIPPING_AND_TRACKING_CONSUMER_GROUP` and only the first missing one is reported | set it (chart: `database.*`, `kafka.brokers`, `config.paymentProviderUrl`, `config.*ConsumerGroup`) |
| `cmd/a2a` exits 1: `A2A_AGENT_URL is required: the Agent Card must advertise the absolute URL of this agent's JSON-RPC endpoint` | no `A2A_AGENT_URL`, or a relative / schemeless / non-http(s) / host-less value (`A2A_AGENT_URL="/a2a" is not an absolute http(s) URL`) | the error is joined with the `DATABASE_URL` one when both are missing | set an absolute URL such as `http://<release>-a2a.<ns>.svc.cluster.local:8093/`; the chart defaults it |
| `cmd/mcp` / `cmd/a2a` exits 1 with `DATABASE_URL is required: this context runs on real Postgres` | no DSN | | set `DATABASE_URL`; neither binary needs Kafka variables |
| Pod `CrashLoopBackOff`, log ends with a migration or ping error after 5 attempts | Postgres unreachable, wrong DSN, or credentials rejected; first-dial reset of an injected pod outlasted the budget (1+2+4+8 s) | `kubectl logs --previous`; the lines `run migrations` / `ping postgres` name the attempt | fix the DSN or Postgres; the startup probe allows about 60 s |
| Migrations hang or fail on a pooled DSN | `DATABASE_URL` points at PgBouncer in transaction mode, which cannot hold golang-migrate's session advisory lock | | set `MIGRATIONS_DATABASE_URL` to a direct DSN (chart: Secret key `MIGRATIONS_DATABASE_URL`) |
| `helm template` / `helm upgrade` fails | the chart refuses to render without `database.*`, `kafka.brokers`, `config.paymentProviderUrl` and the three group ids, refuses `*_MODE` / `*_CONSUMER_GROUP` in `extraEnv`, and a `terminationGracePeriodSeconds` below the worst-case shutdown | the failure message names the value | set it, or raise the grace period |
| Process exits after running: `<name> stopped unexpectedly: exiting so the orchestrator restarts the service` | a worker returned on its own: `outbox-relay`, `policies`, `checkout`, `order-sourcing` or `shipping-and-tracking`. The usual cause is a broker still unreachable after 10 consecutive stalled fetches | the log line just before it (`... consumer stopped`, `outbox relay stopped`) | restore Kafka; the restart is by design |

## Readiness and traffic

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| `GET /readyz` is 503 `dependency-unavailable` naming `postgres` | the ping failed | `kubectl exec` and `psql "$DATABASE_URL" -c 'select 1'`; verdict is cached 3 s | restore Postgres / PgBouncer; no restart needed, `/healthz` stays 200 |
| `/readyz` is 503 naming `kafka` | no broker answered a topic-less metadata request within 2 s | `KAFKA_BROKERS` value, broker reachability from the pod | restore the broker; a **topic** that does not exist yet never makes `/readyz` fail |
| `/readyz` is 503 `{"status":"not_ready"}` for a while, then the pod goes | draining after SIGTERM | the log line `shutdown: readiness flipped to not-ready; waiting for traffic to drain` | expected on rollout |
| Pod killed (SIGKILL) during rollout, relay or consumers cut off | grace period shorter than drain delay + 10 s HTTP + 4 workers x 10 s + 10 s relay | `terminationGracePeriodSeconds` (chart default 70) | the chart refuses a shorter value for the configured `SHUTDOWN_DRAIN_DELAY`; keep it |
| Requests slow or 500 with `statement timeout` in the logs | a query ran past the pool's `statement_timeout = 5s` | `pg_stat_activity` | find the slow query; the timeout is not configurable |

## REST errors

All errors are RFC 9457 `application/problem+json` with `type` `/problems/<slug>`.

| Status / slug | Cause | Fix |
| --- | --- | --- |
| 400 `idempotency-key-required` | a `POST` without `Idempotency-Key` (1 to 128 chars) | send one key per logical request |
| 422 `idempotency-key-reused` | the same key with a different request body | mint a new key, or keep the body byte-stable per logical request; a correct replay returns the stored answer with `Idempotent-Replayed: true` |
| 400 `malformed-request` | bad JSON, unknown field, wrong type | fix the body (see `apis/openapi.yaml`) |
| 422 `validation-failed` | e.g. `exactly one of orderId and checkoutSessionId is required` on `GET /payment-authorizations`, or `orderId is required` on `GET /refunds` | send the missing / exclusive parameter |
| 422 `allocation-mismatch` | allocations do not sum to `totalMinor` or repeat a `groupNo` | fix the allocations |
| 422 `unknown-group` | a refund or fact names a `groupNo` the authorization does not have | check `GET /payment-authorizations/{authorizationId}` |
| 422 `payment-declined` | the provider declined an operation that has no stored-decline form | read `detail` for the code |
| 404 `not-found` | unknown authorization or refund id, or `POST /refunds` for an order with no bound authorization (`authorization of order ...: not found`) | the order is bound by the `CheckoutCompleted` event; see "Facts stay parked" below |
| 409 `duplicate-authorization` | two concurrent authorize requests for one checkout session raced past the live-session lookup (the database allows one non-`DECLINED` authorization per session); a sequential repeat returns the existing authorization with `200` instead | read it back with `GET /payment-authorizations?checkoutSessionId=` |
| 409 `order-already-bound` | the authorization is bound to a different order | check the checkout event that bound it |
| 409 `capture-after-expiry` | a capture fact arrived after the hold expired | the fact is dead-lettered; investigate the shipping delay and refund / re-authorize out of band |
| 409 `over-refund` | refunds (REQUESTED plus SUCCEEDED) would exceed the captured total of the order or group | list `GET /refunds?orderId=` and compare with the captured amounts |
| 409 `illegal-transition` | e.g. voiding an authorization that already has a captured allocation, or retrying a refund that is not FAILED | read the current state |
| 409 `concurrent-update` | another writer changed the aggregate between load and save, after the 2 automatic attempts | retry |
| 503 `payment-provider-unavailable` | the provider timed out (10 s), refused the connection, or answered 429 / 5xx | the counter `payments.authorizations.decided{outcome="failed",reason="payment-provider-unavailable"}`; check `PAYMENT_PROVIDER_URL`. Nothing is stored for an authorization; retry **with the same** `Idempotency-Key` (the provider key is deterministic) |
| 500 `internal-error` | an unexpected error | the access-log line (`status` 500, `request_id`) and the surrounding ERROR lines; the trace by `trace_id` |

A decline is **not** an error: `POST /payment-authorizations` answers `201` with
`state: DECLINED` and a `declineReason`, and a later request for the same
checkout session creates a fresh attempt (declined attempts do not block the
session).

## Events are not leaving (outbox)

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| Downstream contexts do not see `PaymentAuthorized` etc. | the relay cannot publish, or a row is capped | `SELECT id, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL ORDER BY id;` and the log line `outbox relay pass failed` | restore Kafka; the relay drains in `id` order by itself |
| One row stays unpublished, attempts equal `OUTBOX_MAX_ATTEMPTS`, ERROR `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | the row failed 20 times (default); later rows overtake it | `last_error` of the row | fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;` |
| `outbox relay` log shows `invalid OUTBOX_MAX_ATTEMPTS; using the default` | a negative or non-numeric value | the `value` field | fix the variable; `0` means unlimited |

## Consumers, dead letters and parked facts

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| ERROR `<consumer> handling failed; retrying the same message` repeating, then `... failed after the maximum attempts; dead-lettering the message` | a transient failure that outlasted 5 attempts (provider outage during a capture, Postgres trouble) | `partition`, `offset`, `error` fields | fix the dependency; inspect and replay `<topic>.dlq` (see [Runbook](/docs/operations/runbook#dead-letters)) |
| ERROR `... message can never be applied; dead-lettering it without retry` | a poison payload: invalid event, unknown group, capture after expiry, illegal transition, order already bound to another authorization | the `x-dlq-error` header on the `.dlq` message | the producer sent a fact that contradicts the stored state; correct it at the source; do not replay unchanged |
| WARN `skipping a message that is not a valid CloudEvent` | a non-CloudEvents message on a consumed topic | `topic`, `error` | none needed: committed past; fix the publisher |
| WARN `<consumer> broker connection stalled or lost; fetching again after the backoff` | idle TCP connection dropped (conntrack, sidecar idle timeout) | `attempt` counter | none for isolated lines; after 10 consecutive failures the worker returns and the process restarts |
| A consumer is silent | the producing sibling has not published yet, or the group id changed | `kafka-consumer-groups` lag for the group; the log line `consumer running` shows `group_id` and `topic` | check the producer first; a **new** group id replays the whole topic |
| `customer_payments_parked_facts` grows; after 24 h ERROR `ALERT fact for an unbound order dead-lettered` | shipped / cancelled / lost / returned facts arrived for an order that no authorization is bound to: `CheckoutCompleted` has not been consumed (or never carried the `authorizationId`) | `SELECT order_id, kind, parked_at FROM parked_facts ORDER BY parked_at;`; the checkout consumer's lag; `retailnetwork.checkout.events` | fix checkout's publication; parked facts are applied once the order is bound; unbound ones go to `retailnetwork.customer-payments.unbound-facts.dlq` after 24 h and are purged after 30 days |
| Brand-new database or group: `customer_payments_parked_facts` is high right after first start | a group with no committed offset starts at the first offset and replays the whole shared topic | `parked_facts` content | expected; they escalate and are purged by the policy loop |

## Authorizations that do not behave

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| An authorization went `EXPIRED` | the provider hold lapsed without a successful re-authorization (the policy runs within 24 h of `expires_at`), or the re-authorization was declined (`PaymentDeclined` with reason `REAUTH_DECLINED`) | `payment_authorizations.expires_at`, `reauthorizations`; log lines `re-authorization skipped` | a skipped pass retries at every `POLICY_INTERVAL`; a provider outage longer than the lead time expires the hold |
| An `AUTHORIZED` authorization with no order was voided by itself | `void-unbound-authorizations`: no `CheckoutCompleted` bound it within 24 h | `PaymentReleased` events with reason `VOIDED` | expected backstop for abandoned checkouts |
| Allocations stay `PENDING` after shipping | the `FulfillmentShipped` fact has not been applied: order unbound (see parked facts) or the order-sourcing consumer is behind | `parked_facts`, consumer lag | as above |

## Refunds

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| Refund stays `REQUESTED` | the provider was unreachable during the request (WARN `refund left REQUESTED: payment provider unavailable`) | `refunds.state`, `created_at` | nothing: `process-requested-refunds` retries every `POLICY_INTERVAL` once the refund is 30 s old |
| Refund `FAILED` | the provider declined it (`failureCode`) | `GET /refunds/{refundId}` (`attempts`, `failureCode`) | fix the cause, then `POST /refunds/{refundId}:retry` with a new `Idempotency-Key`; at most 5 provider attempts in all |
| ERROR `ALERT refund failed for good: operator action needed` | the fifth attempt failed; the `RefundFailed` event has `final: true` | `refund_id`, `order_id`, `code`, `attempts` | the refund stays `FAILED`; settle with the provider manually, then request a new `MANUAL` refund |

## MCP and A2A

| Symptom | Cause | Fix |
| --- | --- | --- |
| MCP `tools/call` or `tools/list` answers an error about the session | Streamable HTTP needs `initialize` first and the returned `Mcp-Session-Id` header on later calls, plus `Accept: application/json, text/event-stream` | follow the order in [MCP tools](/docs/mcp/tools) |
| A2A answers `-32009 VERSION_NOT_SUPPORTED` (`requested "0.3", served "1.0"`) | the client omitted the `A2A-Version: 1.0` header | send it, or use an A2A 1.0 SDK |
| A2A answers `-32601 method not found` | the method name is wrong; the server speaks `SendMessage` | use `SendMessage` |
| A2A / MCP answers an empty list for an order | no authorization is bound to that order in **this** database (a session id is not an order id) | query by `checkoutSessionId`, or check that the agent points at the same `DATABASE_URL` as `cmd/api` |
| The Agent Card advertises the wrong URL | `A2A_AGENT_URL` | set it to the URL clients dial (the gateway URL for off-cluster clients) |
| Routes `/api/mcp/...` return 404 | the MCP component is disabled in the fleet's values (`helm-values/customer-payments.yaml`) | enable `mcp.enabled` and `mcp.route.enabled` |
