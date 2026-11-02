---
id: runbook
title: Runbook
sidebar_label: Runbook
sidebar_position: 2
---

# Customer Payments runbook

Customer Payments is the storefront bounded context that moves money with
the shipment facts: it authorizes at checkout, captures per shipped allocation,
releases on cancellation, refunds lost/returned packages and re-authorizes
before the provider's validity expires ([README](https://github.com/IQVO/customer-payments/blob/develop/README.md), [ADR 0001](/docs/adr/0001-customer-payments-bounded-context),
[ADR 0002](/docs/adr/0002-customer-payments-assumptions)). It publishes its decisions on
`retailnetwork.customer-payments.events`. This runbook covers the deployed
processes and what to do when they misbehave. Every variable is documented in
[Configuration](/docs/operations/configuration); symptoms and fixes by cause
are in [Troubleshooting](/docs/operations/troubleshooting).

## Deployment

The Helm chart `charts/customer-payments` (Chart.yaml in the repository) renders
three components, each its own Deployment and ClusterIP Service:

| Component | Binary | Service port | Rendered when | Probes |
| --- | --- | --- | --- | --- |
| `api` | `cmd/api` | 80 → container 8080 | always | startup and liveness `/healthz`, readiness `/readyz` |
| `mcp` | `cmd/mcp` | 8090 | `mcp.enabled=true` | `/healthz` |
| `a2a` | `cmd/a2a` | 8093 | `a2a.enabled=true` | `/healthz` |

The chart refuses to render without the mandatory values (the failure names
them): `database.url` or `database.existingSecret`, `kafka.brokers`,
`config.paymentProviderUrl` and the three `config.*ConsumerGroup` ids. It also
refuses any `*_MODE` or `*_CONSUMER_GROUP` in `extraEnv`, any variable the chart
renders itself, and a `terminationGracePeriodSeconds` that does not exceed the
worst-case shutdown (see below). The image is built from the repository
Dockerfile (uid 1000, read-only root filesystem), published as
`warehouse/customer-payments`, tag `v<version>`.

```sh
helm upgrade --install customer-payments ./charts/customer-payments \
  --set database.existingSecret=customer-payments-db \
  --set kafka.brokers="<broker>:9092" \
  --set config.paymentProviderUrl="http://payment-provider-simulator:80" \
  --set config.checkoutConsumerGroup=customer-payments-checkout \
  --set config.orderSourcingConsumerGroup=customer-payments-order-sourcing \
  --set config.shippingAndTrackingConsumerGroup=customer-payments-shipping-and-tracking
```

In the fleet's kind cluster the release is driven by `warehouse-infra`
(`helm-values/customer-payments.yaml`): namespace `warehouse-systems`, Secret
`customer-payments-db` (keys `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`), the
provider at `http://payment-provider-simulator.warehouse-systems.svc.cluster.local:80`,
the gateway route `/api/customer-payments` (Kong, `stripPath`), the A2A agent
enabled with `/api/a2a/customer-payments`, and the MCP server left off. The
image tag comes from Terraform. Packaging prerequisites are in
[Before the first release](/docs/operations/before-first-release).

Startup budget: the first Postgres dial of an injected pod can be reset (Istio
native sidecars), so migrations and the first ping retry 5 times with backoff
1, 2, 4 and 8 s (`internal/bootretry`); the chart's startup probe allows about
60 s (`periodSeconds: 2`, `failureThreshold: 30`).

## Migrations

One embedded migration, `0001_customer_payments_schema` (golang-migrate, in
`internal/adapters/outbound/postgres/migrations/`), creates `payment_authorizations`,
`allocations`, `refunds`, `parked_facts`, `processed_events`, `idempotency_keys`
and `outbox_events`. It runs at boot of **each** of `cmd/api`, `cmd/mcp` and
`cmd/a2a`; golang-migrate's advisory lock makes concurrent starts safe. Because
that lock is session-scoped it does not survive PgBouncer transaction pooling,
so set `MIGRATIONS_DATABASE_URL` (chart: Secret key
`database.migrationsExistingSecretKey`, optional) to a **direct** DSN when
`DATABASE_URL` points at PgBouncer. A failed migration refuses boot after the
retry budget. The migration also ships a `.down.sql`, but no runbook step runs
it: roll forward.

## Kafka: topics and consumer groups

| Topic | Direction | Group variable | Handled types |
| --- | --- | --- | --- |
| `retailnetwork.customer-payments.events` | produced by the outbox relay (key = aggregate id, acks=all) | n/a | the eight event types in [Events](/docs/api-reference/events) |
| `retailnetwork.checkout.events` | consumed | `CHECKOUT_CONSUMER_GROUP` (suggested `customer-payments-checkout`) | `CheckoutCompleted` |
| `retailnetwork.order-sourcing.events` | consumed | `ORDER_SOURCING_CONSUMER_GROUP` (`customer-payments-order-sourcing`) | `FulfillmentShipped`, `FulfillmentCancelled`, `FulfillmentUnsourceable` |
| `retailnetwork.shipping-and-tracking.events` | consumed | `SHIPPING_AND_TRACKING_CONSUMER_GROUP` (`customer-payments-shipping-and-tracking`) | `PackageLost`, `PackageReturned` |
| `<consumed topic>.dlq` | produced on dead-lettering | n/a | original key, value and headers plus `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`, `x-dlq-error`, `x-dlq-failed-at` |
| `retailnetwork.customer-payments.unbound-facts.dlq` | produced by the policy loop | n/a | JSON of a fulfilment fact whose order stayed unbound for 24 h (`eventId`, `kind`, `orderId`, `groupNo`, `trackingNumbers`, `occurredAt`, `parkedAt`, `reason`) |

The service never creates topics through an admin API; the broker auto-creates
them on first write (`AllowAutoTopicCreation`). Group ids must be stable: a
rescheduled pod resumes from its committed offsets, while a **new** group id
starts from the first offset and replays the whole topic (harmless, because
every message is claimed in `processed_events` by CloudEvents id, but it parks
facts for orders this database does not know).

## Dead letters

A consumed message is handled in one database transaction (inbox claim on the
CloudEvents id plus the effect) and its offset is committed only afterwards.
A transient failure is retried with backoff 200 ms doubling to 5 s; after 5
attempts the message is published to `<topic>.dlq`. A payload that can never be
applied (invalid event, unknown group, capture after expiry, illegal
transition, order already bound to another authorization) is dead-lettered at
once. Not-a-CloudEvent messages and unknown types are logged at WARN and
committed past. To inspect a dead letter, read the `.dlq` topic and look at the
`x-dlq-error` header; to replay, republish the value to the source topic once
the cause is fixed (the inbox makes a duplicate harmless).

## Policy loop (the sweepers)

`cmd/api` runs one loop every `POLICY_INTERVAL` (5 s) over four passes, each
bounded to 200 rows and independent (a failing pass is logged and retried at
the next tick):

| Pass | Acts on | Effect |
| --- | --- | --- |
| `reauthorize-before-expiry` | live authorizations within 24 h of `expires_at` | new provider hold (`PaymentReauthorized`); a decline expires it and raises `PaymentDeclined` with reason `REAUTH_DECLINED`; an already-lapsed hold expires without asking the provider |
| `void-unbound-authorizations` | `AUTHORIZED` authorizations with no `order_id` after 24 h | voided (the backstop for a checkout that never completed) |
| `process-requested-refunds` | refunds still `REQUESTED` after 30 s (a provider outage during the request) | settles them against the provider |
| `maintain-parked-facts` | facts parked more than 24 h | published to `retailnetwork.customer-payments.unbound-facts.dlq` with an `ALERT fact for an unbound order dead-lettered` ERROR line; facts older than the retention (30 days) are purged |

## Runtime shape (api Deployment: REST, outbox relay, consumers, policy loop)

One `api` Deployment (`charts/customer-payments/templates/deployment.yaml`)
runs a single process, `cmd/api` — the composition root that wires, in one
binary and one pod:

- **REST API** on `HTTP_ADDR` (default `:8080`): `POST /payment-authorizations`,
  `GET /payment-authorizations`, `GET /payment-authorizations/{authorizationId}`,
  `POST /payment-authorizations/{authorizationId}/voids`, `POST /refunds`,
  `GET /refunds`, `GET /refunds/{refundId}`,
  `POST /refunds/{refundId}:retry` (`internal/adapters/inbound/http/server.go`).
- **Transactional outbox relay**: polls the `outbox_events` table every
  `OUTBOX_RELAY_INTERVAL` and publishes pending rows to Kafka
  (`cmd/api/main.go` `startOutboxRelay`, `internal/adapters/outbound/outbox/relay.go`).
- **Three Kafka consumers**: checkout, order-sourcing and shipping-and-tracking
  (`cmd/api/main.go` `consumerSpecs`, `internal/adapters/inbound/kafka/handlers.go`).
- **Clock-driven policy loop**: re-authorization of uncaptured allocations
  before the provider's validity expires, every `POLICY_INTERVAL`
  (`cmd/api/policies.go`).

There is no in-memory mode and no worker that can be switched off. If the relay,
the policy loop or any consumer returns on its own, the process exits non-zero
so the orchestrator replaces the pod (`cmd/api/main.go`
`serveUntilSignal`/`watchWorkers`).

Graceful shutdown order (`cmd/api/main.go` `serveUntilSignal`): `/readyz` flips
to 503 → wait `SHUTDOWN_DRAIN_DELAY` (default 5s) → drain HTTP (10s bound) →
stop the consumers (10s each) → stop the policy loop (10s) → stop the relay
LAST (10s) → close the pool. Worst case is drain + 60s; the chart refuses a
`terminationGracePeriodSeconds` that does not exceed it and defaults to 70
(`charts/customer-payments/values.yaml`).

## The optional MCP server

`cmd/mcp` is a second, read-only deployable over the same read use cases the
REST adapter serves: Streamable HTTP on `/mcp` at `MCP_ADDR` (default `:8090`),
plus `GET /healthz` for the probes. It runs the same embedded migrations at
boot, never dials Kafka, runs no consumer, no relay and no policy loop, never
calls the payment provider, and has NO auth: access control is the in-cluster
ClusterIP boundary (`cmd/mcp/main.go`). The chart renders it only with
`mcp.enabled=true` (`charts/customer-payments/templates/mcp-deployment.yaml`).

External exposure, when `mcp.route.enabled=true`, is the Gateway HTTPRoute
**`/api/mcp/customer-payments`** → Service :8090, prefix rewritten to `/mcp`
(the SDK's canonical endpoint); `/healthz` deliberately stays off the host —
it is a probe concern, not a public endpoint
(`charts/customer-payments/templates/mcp-route.yaml`).

## The optional A2A agent

`cmd/a2a` is a third, read-only deployable ([ADR 0005](/docs/adr/0005-a2a-inbound-adapter)) over the same read use cases:
Agent2Agent JSON-RPC on `/` and `/a2a` at `A2A_ADDR` (default `:8093`), the Agent Card at
`/.well-known/agent-card.json` and `GET /healthz` for the probes. It runs the same embedded migrations at boot, never dials
Kafka, runs no consumer, no relay and no policy loop, never calls the payment provider, and has NO auth. `A2A_AGENT_URL`
(absolute http(s) URL advertised on the card) is mandatory: the process refuses to boot without it. The chart renders it only
with `a2a.enabled=true` (`charts/customer-payments/templates/a2a-deployment.yaml`); `A2A_AGENT_URL` defaults to
`http://<release>-a2a.<ns>.svc.cluster.local:8093/` and is overridden by `a2a.agentUrl`.

External exposure, when `a2a.route.enabled=true`, is the Gateway HTTPRoute **`/api/a2a/customer-payments`** → Service :8093,
prefix rewritten to `/` (the card stays reachable under the same prefix); `/healthz` stays off the host
(`charts/customer-payments/templates/a2a-route.yaml`).

## Dependencies

| Dependency | Used for | Source |
| --- | --- | --- |
| Postgres (via PgBouncer) | every write and read; `DATABASE_URL` is the pooled DSN | Secret (`database.existingSecret`), key `DATABASE_URL` |
| Postgres (direct DSN) | boot migrations only; `MIGRATIONS_DATABASE_URL` | same Secret, key `MIGRATIONS_DATABASE_URL`, optional — falls back to `DATABASE_URL` (`cmd/api/main.go`) |
| Kafka (`KAFKA_BROKERS`) | outbox relay sink, the three consumers, `/readyz` broker check | chart value `kafka.brokers` |
| Payment provider (REST) | the one synchronous dependency: authorize, capture, void, refund, reauthorize | `config.paymentProviderUrl` (`PAYMENT_PROVIDER_URL`) |

Notes:

- Migrations are embedded (golang-migrate) and run at boot inside a bounded
  retry (`internal/bootretry`) that tolerates a first-dial reset; exhaustion
  refuses boot.
- Produced topic: `retailnetwork.customer-payments.events`
  (`internal/adapters/outbound/kafka/payloads.go` `TopicEvents`). Event types
  are `com.retailnetwork.storefront.customer-payments.<entity>.<EventName>`:
  `PaymentAuthorized`, `PaymentCaptured`, `PaymentReleased`, `PaymentDeclined`,
  `PaymentReauthorized`, `PaymentAuthorizationExpired`, `RefundIssued`,
  `RefundFailed` (`internal/domain/paymentauthorization/events.go`,
  `internal/domain/refund/events.go`).
- Consumed topics: `retailnetwork.checkout.events`,
  `retailnetwork.order-sourcing.events` and
  `retailnetwork.shipping-and-tracking.events`; dead letters go to
  `<topic>.dlq` (`internal/adapters/inbound/kafka/handlers.go`).

## Health

- `GET /healthz` — liveness only. 200 while the process is up; never flipped by
  dependencies or by shutdown (`internal/adapters/inbound/http/server.go`). The
  chart's startup and liveness probes use it.
- `GET /readyz` — readiness. 200 `{"status":"ready"}` only while (a) the pod is
  not draining AND (b) a Postgres ping AND a Kafka broker metadata request both
  answer. A verdict is cached 3s and one check round is bounded 2s
  (`internal/adapters/inbound/http/readiness.go` `DefaultReadinessTTL`/
  `DefaultReadinessTimeout`). Otherwise: 503 `{"status":"not_ready"}` once
  draining, or 503 problem+json `dependency-unavailable` naming the failing
  dependencies. `/healthz` stays 200 either way, so probes never restart a pod
  whose dependencies are down.
- `GET /metrics` — Prometheus scrape endpoint (`internal/adapters/inbound/http/server.go`).

## Configuration and chart values

Env vars `cmd/api` reads (the full per-binary tables are in
[Configuration](/docs/operations/configuration); contract: `cmd/api/main.go`
header comment, validated in `cmd/api/config.go`; rendered in
`charts/customer-payments/templates/deployment.yaml`; the mcp and a2a components read
the subset marked •):

| Env var | Chart value | Default | Notes |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `config.httpAddr` | `:8080` | port must equal `service.targetPort` |
| `LOG_LEVEL` • | `config.logLevel` | `info` | `debug`, `info`, `warn`/`warning` or `error`; JSON logs on stdout with trace context |
| `DATABASE_URL` • | Secret key `database.existingSecretKey` | required | pooled DSN (PgBouncer); never in the ConfigMap |
| `MIGRATIONS_DATABASE_URL` • | Secret key `database.migrationsExistingSecretKey` | `DATABASE_URL` | direct (session-mode) DSN, boot migrations only |
| `KAFKA_BROKERS` | `kafka.brokers` | required | comma-separated broker list |
| `PAYMENT_PROVIDER_URL` | `config.paymentProviderUrl` | required | base URL of the payment provider |
| `CHECKOUT_CONSUMER_GROUP` | `config.checkoutConsumerGroup` | required | e.g. `customer-payments-checkout` |
| `ORDER_SOURCING_CONSUMER_GROUP` | `config.orderSourcingConsumerGroup` | required | e.g. `customer-payments-order-sourcing` |
| `SHIPPING_AND_TRACKING_CONSUMER_GROUP` | `config.shippingAndTrackingConsumerGroup` | required | e.g. `customer-payments-shipping-and-tracking` |
| `OUTBOX_RELAY_INTERVAL` | `config.outboxRelayInterval` | `1s` | Go duration or plain seconds |
| `POLICY_INTERVAL` | `config.policyInterval` | `5s` | how often the re-authorization policy loop runs |
| `OUTBOX_MAX_ATTEMPTS` | `config.outboxMaxAttempts` | `20` | failures before the relay excludes a row; `0` = unlimited |
| `SHUTDOWN_DRAIN_DELAY` | `config.shutdownDrainDelay` | `5s` | wait after `/readyz` flips before the listener closes; `0` disables |
| `MCP_ADDR` | `mcp.httpAddr` | `:8090` | mcp only: listen address of the Streamable HTTP server |
| `A2A_ADDR` | `a2a.httpAddr` | `:8093` | a2a only: listen address of the A2A JSON-RPC server |
| `A2A_AGENT_URL` | `a2a.agentUrl` | `http://<release>-a2a.<ns>.svc.cluster.local:8093/` | a2a only: absolute http(s) URL advertised on the Agent Card; mandatory in the binary |
| `SERVICE_VERSION` • | image tag (`appVersion` when empty; `mcp.serviceVersion` overrides for mcp) | image tag | resource attribute and the boot log's build label |
| `ENVIRONMENT` • | `environment` (`mcp.environment` for mcp) | `production` | `deployment.environment.name` on every span, metric and log |
| `OTEL_SERVICE_NAME` • | `otel.serviceName` (mcp gets `<serviceName>-mcp`, a2a `<serviceName>-a2a`) | `customer-payments` | `service.name` of spans and metrics |
| `OTEL_EXPORTER_OTLP_ENDPOINT` • | `otel.endpoint` | Collector OTLP/gRPC | not rendered when `otel.enabled=false` (the binaries then fall back to `localhost:4317`) |

A missing required variable is a boot error that names the variable
(`cmd/api/config.go`). The consumer group ids must be stable and
service-prefixed — a rescheduled pod resumes from its committed offsets under
the same group (`charts/customer-payments/values.yaml`).

Behavior — Idempotency-Key middleware (`internal/adapters/inbound/http/idempotency.go`):

- Every POST route (`/payment-authorizations`, `.../voids`, `/refunds`,
  `:retry`) requires an `Idempotency-Key` header of 1 to 128 printable ASCII
  characters (absent: 400 `idempotency-key-required`, malformed: 400
  `idempotency-key-invalid`).
- The key is global. The first response below 500 (a 4xx too) is stored for 24
  hours in the same transaction as the aggregate change and its outbox rows; a
  repeat with the same request (method + path + body hash) replays it with
  header `Idempotency-Replayed: true`; the same key with a different request is
  409 problem `idempotency-key-reused`. Concurrent requests with one key are
  serialised by the unique index on `idempotency_keys`; the loser replays.
- 5xx responses and panics roll back and are never stored, so the key stays
  retryable.

## Alerts and dashboards

- Traces, metrics and logs follow the fleet observability standard
  (`docs/adr/0004`): pod --OTLP/gRPC--> Collector --> Jaeger (traces) and
  Prometheus (metrics); stdout JSON logs carry `trace_id`/`span_id` for Loki.
  Export never blocks: a Collector that is down costs telemetry and nothing
  else (`internal/adapters/outbound/telemetry/telemetry.go`).
- Business metric: the `payments.authorizations.decided` counter
  (`internal/adapters/outbound/telemetry/payment_metrics.go`), exported over
  OTLP like every other OTel metric; `GET /metrics` serves only the
  `customer_payments_parked_facts` gauge.
- Pod-restart and Kong 5xx alert rules exist at the fleet level, but
  `warehouse-alert-rules.json` carries no group for this context today; see
  [Observability](/docs/operations/observability#dashboards-and-alerts). Watch
  the Grafana dashboard `warehouse-customer-payments` and the log levels
  instead.

## Failure modes

### Pod crash-loop

- Symptom: restarts climb, the pod-restart alert fires.
- Diagnosis: `kubectl -n warehouse-systems logs deploy/customer-payments --previous`
  — the process logs the missing env var by name (`cmd/api/config.go`),
  migrations/ping exhaustion (`internal/bootretry`), or `<worker> stopped
  unexpectedly` (`cmd/api/main.go`).
- Action: fix the named chart value or Secret key, then roll again.

### Postgres unreachable

- Symptom: `/readyz` 503 `dependency-unavailable` naming `postgres`; REST
  writes fail (every write is a Postgres transaction); consumers retry; the
  relay logs `outbox relay pass failed`.
- Diagnosis: check the Secret's `DATABASE_URL` (pooled) and PgBouncer; the
  readiness verdict refreshes within 3s of recovery.
- Action: restore Postgres/PgBouncer — consumers and the relay resume on their
  own and nothing needs replaying; the pod is not killed (`/healthz` stays 200).

### Kafka unreachable

- Symptom: `/readyz` 503 naming `kafka`; `/healthz` 200.
- Diagnosis: `SELECT count(*) FROM outbox_events WHERE published_at IS NULL;`
  grows while brokers are down; the relay retries every
  `OUTBOX_RELAY_INTERVAL` (`internal/adapters/outbound/outbox/relay.go`).
- Action: restore the brokers; the relay drains in `id` order automatically. If
  a consumer's reader returns, the process exits non-zero by design and the pod
  restarts.

### Outbox relay stuck / row parked after max attempts

- Symptom: repeating relay-pass ERRORs, or the one-time ERROR naming the
  attempt cap (`internal/adapters/outbound/outbox/relay.go`
  `AttemptCapReachedMsg`).
- Diagnosis: parked rows keep their counters for inspection —
  `SELECT id, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL ORDER BY id;`.
  A row whose `attempts` reached `OUTBOX_MAX_ATTEMPTS` is excluded from claims
  and later rows overtake it.
- Action: fix the cause (usually Kafka), then re-arm the row:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`.

### Consumer lag or retry logs

- Symptom: repeated `handling failed; retrying the same message`, or silence on
  a topic.
- Diagnosis: transient handler failures retry with capped exponential backoff
  and then dead-letter to `<topic>.dlq`; unknown CloudEvents types are skipped,
  non-CloudEvents are WARN-logged, a poison payload is dead-lettered at once
  (`internal/adapters/inbound/kafka`). A topic whose producing sibling has not
  published yet has no messages — check the producer before assuming this
  consumer is broken.
- Action: inspect `<topic>.dlq` for poison messages and replay from there; a
  lagging-but-quiet topic is a producer-side matter.

### idempotency-key-reused storms

- Symptom: a burst of 409 `idempotency-key-reused` from the POST routes.
- Diagnosis: the middleware hashes the request, so a storm means callers reuse
  a key while regenerating the body (timestamps, ids). Correct replays come
  back with `Idempotency-Replayed: true` and are not a fault. The same key sent
  to two different routes is a reuse too (the key is global).
- Action: fix the caller to keep the body byte-stable per logical request or
  mint a fresh key.

## Operational actions

- Restart: `kubectl -n warehouse-systems rollout restart deploy/customer-payments`
- Logs: Grafana Loki (JSON slog on stdout with `trace_id`/`span_id`); raise
  `config.logLevel` to `debug` for a noisy pod. Traces: Jaeger, service
  `customer-payments` (mcp: `customer-payments-mcp`, a2a: `customer-payments-a2a`).
- In-cluster REST: Service port 80 → container 8080; the external path is the
  gateway route `/api/customer-payments`.
- MCP (when enabled): Streamable HTTP at `/api/mcp/customer-payments` through
  the gateway (rewritten to `/mcp`), or in-cluster on Service port 8090.
- A2A (when enabled): JSON-RPC at `/api/a2a/customer-payments` through the gateway (rewritten to `/`; the Agent Card at
  `/api/a2a/customer-payments/.well-known/agent-card.json`), or in-cluster on Service port 8093.
- Scale: HPA off by default (`autoscaling.api`); N>1 is safe for the relay (row
  claims), the version-guarded writes and the consumers (stable groups, inbox
  dedupe), but has not been load-tested (`charts/customer-payments/values.yaml`).
