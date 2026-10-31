---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`customer-payments` ships **three binaries**, each configured only through
environment variables: `cmd/api` (REST, outbox relay, Kafka consumers, policy
loop), `cmd/mcp` (read-only MCP server) and `cmd/a2a` (read-only A2A agent).
There is no config file, no flag and no secret-manager integration in the
repository. REST, MCP and A2A are unauthenticated fleet-wide, so there is no
token or key to configure.

The service runs on real Postgres and real Kafka only: `cmd/api` has no
in-memory mode, no log sink and no consumer that can be switched off
(`TestRuntimeBinaryHasNoInMemoryAdapters` in `internal/architecture/`). A
missing mandatory variable is a boot error that names it, raised before
anything is opened or bound. Test-only variables are not listed.

The Helm chart maps these variables onto the pods (see
[Runbook](/docs/operations/runbook#configuration-and-chart-values)); the
"Chart value" there is the knob to turn in a cluster.

## `cmd/api`

| Variable | Default | Required? | Meaning | Source |
| --- | --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST server (also serves `/healthz`, `/readyz`, `/metrics`). | `cmd/api/main.go` (`run`) |
| `DATABASE_URL` | none | **yes** | Postgres DSN. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode`. The embedded migrations run at boot; the outbox, the `Idempotency-Key` store, the inbox and every repository live here. | `cmd/api/config.go` (`loadRuntime`) |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because golang-migrate's advisory lock does not survive PgBouncer transaction pooling. An empty value falls back to `DATABASE_URL`. | `cmd/api/main.go` (`buildAdapters`) |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated broker list (entries trimmed, blanks dropped). Missing = boot error `KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)`. Used by the consumers, the outbox relay, the escalation writer and the `/readyz` broker check. In the fleet's kind cluster the single broker is `localhost:9092` from the host. | `cmd/api/config.go` (`loadRuntime`, `splitBrokers`) |
| `PAYMENT_PROVIDER_URL` | none | **yes** | Base URL of the payment provider's REST API, the one synchronous dependency (authorize, capture, void, refund, reauthorize). Missing = boot error `PAYMENT_PROVIDER_URL is required`. | `cmd/api/config.go` (`loadRuntime`), `internal/adapters/outbound/paymentprovider/client.go` |
| `CHECKOUT_CONSUMER_GROUP` | none | **yes** | Stable Kafka consumer group id of the checkout consumer (topic `retailnetwork.checkout.events`). The code has no default; the deployment chooses it. Missing = boot error `CHECKOUT_CONSUMER_GROUP is required: the consumer always runs on real Kafka`. | `cmd/api/config.go` (`loadRuntime`), `cmd/api/main.go` (`consumerSpecs`) |
| `ORDER_SOURCING_CONSUMER_GROUP` | none | **yes** | Group id of the order-sourcing consumer (topic `retailnetwork.order-sourcing.events`). | `cmd/api/main.go` (`consumerSpecs`) |
| `SHIPPING_AND_TRACKING_CONSUMER_GROUP` | none | **yes** | Group id of the shipping-and-tracking consumer (topic `retailnetwork.shipping-and-tracking.events`). | `cmd/api/main.go` (`consumerSpecs`) |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Pause between relay passes that did not fill a batch. A Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Unparsable or non-positive values log a WARN and use the default. | `cmd/api/config.go` (`parseRelayInterval`) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures after which the relay stops claiming an outbox row. `0` = unlimited. Negative or unparsable values log a WARN and use `20`, so a typo never removes the cap. | `cmd/api/config.go` (`outboxMaxAttempts`) |
| `POLICY_INTERVAL` | `5s` | no | How often the clock-driven policy loop runs its four passes: `reauthorize-before-expiry`, `void-unbound-authorizations`, `process-requested-refunds` (refunds a provider outage left REQUESTED) and `maintain-parked-facts` (escalate after 24 h, purge after 30 days). Go duration or plain seconds; unparsable or non-positive values log a WARN and use the default. | `cmd/api/policies.go` (`DefaultPolicyInterval`), `cmd/api/config.go` (`parsePolicyInterval`) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | After SIGINT/SIGTERM `/readyz` flips to 503 and the process waits this long before closing the listener. `0` disables the wait. Negative or unparsable values log a WARN and use `5s`. Read when the signal arrives, not at boot. | `cmd/api/config.go` (`shutdownDrainDelay`) |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`, case-insensitive. Any other value silently falls back to `info`. JSON logs on stdout, with `trace_id`/`span_id` when the record carries a span context. | `cmd/api/config.go` (`newLogger`) |
| `OTEL_SERVICE_NAME` | `customer-payments` | no | `service.name` of every span and metric, and the name of the HTTP server spans. | `cmd/api/telemetry.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint, bare `host:port` (normalised to `http://host:port`) or a full URL. Export never blocks: an unreachable Collector costs telemetry, not availability. | `cmd/api/telemetry.go`, `internal/adapters/outbound/telemetry/telemetry.go` (`normalizeEndpoint`) |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute. | `cmd/api/telemetry.go` |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute (read by `telemetry.Setup`). The chart sets `production`. | `internal/adapters/outbound/telemetry/telemetry.go` (`environment`) |

The mandatory variables are validated before anything is opened; the first
missing one is reported (checked in the order `DATABASE_URL`, `KAFKA_BROKERS`,
`PAYMENT_PROVIDER_URL`, then the three group ids). With them set, the three consumers, the outbox relay and the policy
loop all start, and `/readyz` says whether Postgres and Kafka are usable.

## `cmd/mcp`

Read-only. Starts no relay, consumer or policy loop, never dials Kafka and
never calls the payment provider.

| Variable | Default | Required? | Meaning | Source |
| --- | --- | --- | --- | --- |
| `MCP_ADDR` | `:8090` | no | Listen address of the Streamable HTTP server (`/mcp`, `/healthz`). | `cmd/mcp/main.go` |
| `DATABASE_URL` | none | **yes** | Postgres DSN of the service database that `cmd/api` owns. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres`. | `cmd/mcp/main.go` (`buildDeps`) |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the idempotent boot migrations (this binary can boot against a fresh database before `cmd/api` has). | `cmd/mcp/main.go` |
| `LOG_LEVEL` | `info` | no | As `cmd/api`. | `cmd/mcp/main.go` (`newLogger`) |
| `OTEL_SERVICE_NAME` | `customer-payments-mcp` | no | `service.name` of spans and metrics. | `cmd/mcp/telemetry.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As `cmd/api`. | `cmd/mcp/telemetry.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version`; also printed in the `mcp server starting` boot line. | `cmd/mcp/main.go`, `cmd/mcp/telemetry.go` |
| `ENVIRONMENT` | `production` in the boot line, `local` in telemetry | no | `deployment.environment.name`. The two defaults differ: the boot log line falls back to `production`, the OTel resource to `local`. | `cmd/mcp/main.go`, `internal/adapters/outbound/telemetry/telemetry.go` |

## `cmd/a2a`

Read-only, like `cmd/mcp`. All problems with mandatory variables are reported
at once (`errors.Join`), before telemetry, any connection and the listener.

| Variable | Default | Required? | Meaning | Source |
| --- | --- | --- | --- | --- |
| `A2A_ADDR` | `:8093` | no | Listen address of the JSON-RPC server (`/`, `/a2a`, `/.well-known/agent-card.json`, `/healthz`). | `cmd/a2a/main.go` |
| `A2A_AGENT_URL` | none | **yes** | Absolute `http(s)` URL of this agent's JSON-RPC endpoint, advertised on the Agent Card. Missing, blank, relative, schemeless, non-http(s) or host-less values are a boot error naming the variable ([ADR 0005](/docs/adr/0005-a2a-inbound-adapter)). | `cmd/a2a/main.go` (`checkAgentURL`, `loadBootConfig`) |
| `DATABASE_URL` | none | **yes** | Postgres DSN, as `cmd/mcp`. | `cmd/a2a/main.go` (`loadBootConfig`) |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations. | `cmd/a2a/main.go` |
| `LOG_LEVEL` | `info` | no | As `cmd/api`. | `cmd/a2a/main.go` (`newLogger`) |
| `OTEL_SERVICE_NAME` | `customer-payments-a2a` | no | `service.name` of spans and metrics. | `cmd/a2a/telemetry.go` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As `cmd/api`. | `cmd/a2a/telemetry.go` |
| `SERVICE_VERSION` | `dev` | no | `service.version`; also on the Agent Card version and the boot line. | `cmd/a2a/main.go`, `cmd/a2a/telemetry.go` |
| `ENVIRONMENT` | `production` in the boot line, `local` in telemetry | no | `deployment.environment.name`; same split default as `cmd/mcp`. | `cmd/a2a/main.go`, `internal/adapters/outbound/telemetry/telemetry.go` |

## Readiness

`GET /readyz` on `cmd/api` is 200 only when the shutdown drain gate is open
**and** Postgres answers a ping **and** a Kafka broker answers a topic-less
metadata request. The verdict is cached for 3 s and one round of checks is
bounded by 2 s (`internal/adapters/inbound/http/readiness.go`). A failed
dependency is `503` `application/problem+json` (`dependency-unavailable`
naming the dependency). `/healthz` is liveness only. `cmd/mcp` and `cmd/a2a`
expose `/healthz` only. Details in the [Runbook](/docs/operations/runbook#health).

## Compiled-in settings (not configurable)

These are constants in the source; changing them needs a code change.

| Setting | Value | Source |
| --- | --- | --- |
| HTTP `ReadHeaderTimeout` | 5 s (all three binaries) | `cmd/*/main.go` |
| HTTP graceful shutdown timeout / per-worker drain timeout (`cmd/api`) | 10 s / 10 s | `cmd/api/main.go` (`shutdownTimeout`, `workerDrainTimeout`) |
| HTTP graceful shutdown timeout (`cmd/mcp`, `cmd/a2a`) | 10 s | `cmd/mcp/main.go`, `cmd/a2a/main.go` |
| Postgres pool size and statement timeout | 10 connections, `statement_timeout = 5s` | `internal/adapters/outbound/postgres/pool.go` |
| Boot retry (migrations, first ping) | 5 attempts, backoff 1 s, 2 s, 4 s, 8 s | `internal/bootretry/bootretry.go` |
| Outbox relay batch size | 100 rows per pass | `internal/adapters/outbound/outbox/relay.go` (`DefaultBatchSize`) |
| Kafka producer | acks=all, hash balancer on the message key, 10 ms batch timeout, `AllowAutoTopicCreation` | `internal/adapters/outbound/kafka/writer.go` |
| Consumer retry backoff | 200 ms doubling to a 5 s cap, at most 5 handling attempts, then `<topic>.dlq` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` (`domainMaxHandlerAttempts`) |
| Consumer fetch resilience | a stalled or reset broker connection is retried up to 10 consecutive times, then the worker (and the process) exits | `internal/adapters/inbound/kafka/kafka.go` (`maxTransientFetchFailures`) |
| DLQ topic readiness wait | 40 attempts, 250 ms apart | `internal/adapters/inbound/kafka/deadletter.go` |
| `/readyz` dependency cache and check timeout | 3 s verdict cache, 2 s per round | `internal/adapters/inbound/http/readiness.go` |
| `Idempotency-Key` length | 1 to 128 characters | `internal/adapters/inbound/http/idempotency.go` (`maxKeyLength`) |
| Topic produced | `retailnetwork.customer-payments.events` | `internal/adapters/outbound/kafka/payloads.go` (`TopicEvents`) |
| Topics consumed | `retailnetwork.checkout.events`, `retailnetwork.order-sourcing.events`, `retailnetwork.shipping-and-tracking.events` | `internal/adapters/inbound/kafka/handlers.go` |

## What is deliberately absent

- **No auth variables.** REST, MCP and A2A are unauthenticated fleet-wide;
  `TestNoAuthMiddlewareReintroduced` fails the build if a bearer or JWT check
  appears under `internal/adapters/inbound`.
- **No Kafka variable on `cmd/mcp` and `cmd/a2a`.** They never dial Kafka and
  cannot write the outbox.
- **No `*_MODE` variables.** There is no memory or permissive mode; the chart
  refuses any `*_MODE` in `extraEnv`.
- **No CORS, rate-limit or feature-flag variables.**
