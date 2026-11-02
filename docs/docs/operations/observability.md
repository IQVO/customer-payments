---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

Logs, metrics and traces follow the fleet observability standard
([ADR 0004](/docs/adr/0004-fleet-observability-standard)): every pod pushes
OTLP/gRPC to the OpenTelemetry Collector, which feeds Jaeger (traces) and
Prometheus (metrics); stdout JSON logs are collected by Alloy into Loki. All
three binaries (`cmd/api`, `cmd/mcp`, `cmd/a2a`) share the same wiring in
`internal/adapters/outbound/telemetry`.

Export never blocks: the OTLP exporters connect lazily with no blocking dial
option, so a Collector that is down or absent costs telemetry, never
availability and never a failed boot. Exporter errors are logged at DEBUG only.
The final flush at shutdown is bounded to 5 s and a failed flush is a WARN, not
a non-zero exit (`cmd/*/telemetry.go`).

## Exporter configuration

| Variable | `cmd/api` default | `cmd/mcp` default | `cmd/a2a` default | Meaning |
| --- | --- | --- | --- | --- |
| `OTEL_SERVICE_NAME` | `customer-payments` | `customer-payments-mcp` | `customer-payments-a2a` | `service.name`; also the `app` label of the Loki stream and the name the HTTP spans carry |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | same | same | Collector OTLP/gRPC; a bare `host:port` becomes `http://host:port` |
| `SERVICE_VERSION` | `dev` | `dev` | `dev` | `service.version` (the chart passes the image tag) |
| `ENVIRONMENT` | `local` | `local` | `local` | `deployment.environment.name` (the chart passes `production`) |

`telemetry.Setup` also honours the standard `OTEL_RESOURCE_ATTRIBUTES` through
`resource.Default()`. The chart renders the `OTEL_*` variables only with
`otel.enabled=true` (the default); with `false` the binaries fall back to
`localhost:4317` ([Configuration](/docs/operations/configuration)).

The resource carries `service.name`, `service.version`,
`deployment.environment.name` and the SDK attributes. The W3C `traceparent` and
`baggage` propagators are installed globally.

## Metrics

Metrics are pushed every 15 s by a periodic reader
(`internal/adapters/outbound/telemetry/telemetry.go`, `metricInterval`).

| Instrument | Type | Unit | Attributes | Emitted by | Source |
| --- | --- | --- | --- | --- | --- |
| `payments.authorizations.decided` | Int64 counter | `{authorization}` | `outcome` = `authorized` \| `declined` \| `failed`; `reason` on `declined` (the provider's decline code such as `CARD_DECLINED`) and on `failed` (a slug such as `payment-provider-unavailable`), absent on `authorized` | `cmd/api` (one increment per authorization attempt) | `internal/adapters/outbound/telemetry/payment_metrics.go` |
| `http.server.request.duration` | Float64 histogram | `s` | HTTP method, route pattern, status code (the `otelchimetric` server-duration instrument; Prometheus name `http_server_request_duration_seconds`) | all three binaries, every chi route | `internal/adapters/inbound/http/server.go`, `cmd/mcp/router.go`, `cmd/a2a/router.go` |
| Go runtime metrics (`go.goroutine.count`, `go.memory.used`, GC and allocation instruments) | gauges / counters | per instrument | per instrument | all three binaries | `runtime.Start` in `telemetry.Setup` |
| `customer_payments_parked_facts` | Prometheus gauge (not OTel) | count | none | `GET /metrics` of `cmd/api` only | `internal/adapters/inbound/http/handlers.go` |

How to read the business counter (its own description says it): a falling
`authorized` share with a rising `declined` means shoppers' payment methods are
being refused at checkout; a rising `failed` with
`reason="payment-provider-unavailable"` means the provider is unreachable and
checkouts are blocked, not that shoppers are doing anything wrong.

`GET /metrics` on `cmd/api` is a hand-written Prometheus text exposition of one
gauge, `customer_payments_parked_facts`: the number of fulfilment facts (shipped,
cancelled, unsourceable, lost, returned) waiting in `parked_facts` for their
order to be bound to an authorization. It queries Postgres on every scrape and
answers `500` if the count fails. It has no other series: the OTel metrics above
leave through OTLP, not through this endpoint.

There is no queue-depth or outbox-lag instrument in the code. Outbox lag is
visible only in the database (`outbox_events WHERE published_at IS NULL`, see
[Runbook](/docs/operations/runbook)).

## Traces

| Span | Kind | Created by | Attributes |
| --- | --- | --- | --- |
| `<METHOD> <route pattern>` (e.g. `POST /payment-authorizations/{authorizationId}/voids`) | server | `otelchi` middleware, all three binaries; named after the chi route pattern so there is one span name per route, not per id | HTTP semantic-convention attributes |
| `<topic> process` (e.g. `retailnetwork.checkout.events process`) | consumer | `internal/adapters/inbound/kafka/tracing.go`, one per handling attempt | `messaging.system=kafka`, `messaging.destination.name`, `messaging.operation.name=process`; errors recorded with status `Error` |
| `a2a.skill <skill>` (`a2a.skill get_payment_status`, `a2a.skill get_refund_status`) | internal | `internal/adapters/inbound/a2a/skills.go` | `a2a.skill`; status `Error` when the skill answers a problem |

Trace context crosses Kafka in both directions:

1. When a request or a consumed message raises domain events, the active span
   context is injected as `traceparent`/`tracestate` headers into the outbox
   row, in the same transaction as the aggregate
   (`internal/adapters/outbound/postgres/outbox_trace.go`).
2. The relay publishes those headers as ordinary Kafka headers next to the
   CloudEvents `content-type` header.
3. A downstream consumer's `Extract` makes its `<topic> process` span a child of
   the span that enqueued the event. Customer Payments does the same for the
   messages it consumes: a message without headers starts a new trace.

There is no span around the payment-provider HTTP call, the outbox relay pass
or the policy passes; look for their log lines instead.

## Logs

JSON on stdout through `slog` (`newLogger` in each `cmd/*`). The handler is
wrapped by `telemetry.WithTraceContext`, so any record written with a
span-carrying context (the `*Context` slog variants) carries `trace_id` and
`span_id`. `LOG_LEVEL` selects the threshold.

**HTTP access log** (`internal/adapters/inbound/requestlog`): one line per
request, message `http request`, fields `method`, `route` (the matched pattern,
never the raw path, `unmatched` when none matched), `status`, `duration_ms`,
`bytes`, `request_id`. Level: 5xx = ERROR, 4xx = WARN, otherwise INFO.
`/healthz` and `/readyz` are not logged.

**Boot and worker lines** that operators grep for (`cmd/api`):

| Message | Level | Fields | When |
| --- | --- | --- | --- |
| `telemetry configured` | INFO | `service_name`, `otlp_endpoint` | after config validation |
| `postgres adapters configured` | INFO | | migrations and first ping done |
| `consumer running` | INFO | `consumer`, `topic`, `group_id`, `brokers` | one per consumer |
| `outbox relay running` | INFO | `interval`, `max_attempts`, `topic` | relay started |
| `policies running` | INFO | `interval` | policy loop started |
| `http server listening` | INFO | `addr` | listener bound |
| `policy pass done` | INFO | `policy`, `handled` | a policy pass changed something |
| `policy pass failed` | ERROR | `policy`, `error` | retried at the next tick |
| `invalid OUTBOX_RELAY_INTERVAL; using the default` (and the `OUTBOX_MAX_ATTEMPTS`, `POLICY_INTERVAL`, `SHUTDOWN_DRAIN_DELAY` variants) | WARN | `value`, `default` | a malformed value |
| `<consumer> handling failed; retrying the same message` | ERROR | `error`, `partition`, `offset`, `attempt`, `max_attempts`, `retry_in` | transient failure of one message |
| `<consumer> handling failed after the maximum attempts; dead-lettering the message` | ERROR | `error`, `partition`, `offset`, `attempts` | the fifth failure |
| `<consumer> message can never be applied; dead-lettering it without retry` | ERROR | `error`, `partition`, `offset` | poison payload |
| `skipping a message that is not a valid CloudEvent` | WARN | `consumer`, `topic`, `error` | non-CloudEvents message, committed past |
| `<consumer> broker connection stalled or lost; fetching again after the backoff` | WARN | `error`, `attempt`, `backoff` | idle TCP drop, recovered by the reader |
| `outbox relay pass failed` | ERROR | `error` | Kafka or Postgres problem in a relay pass |
| `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | ERROR | | once per capped row |
| `service exited with error` / `<worker> stopped unexpectedly: exiting so the orchestrator restarts the service` | ERROR | `error` | the process is about to exit non-zero |
| `shutdown: readiness flipped to not-ready; waiting for traffic to drain` | INFO | `drain_delay` | SIGTERM received |

`cmd/mcp` and `cmd/a2a` log `postgres repositories configured`,
`mcp server starting` / `a2a agent starting` (with `version` and `environment`),
`... listening` and the access log.

## Dashboards and alerts

Read from `warehouse-infra` (`origin/develop`):

- **Dashboard** `terraform/dashboards/contexts/customer-payments.json`, uid
  `warehouse-customer-payments`, title "Warehouse — Customer Payments":
  Kong request rate, 5xx ratio and p95 latency for the route
  `/api/customer-payments` (and its `-mcp` sibling), service HTTP RED by
  route and p95 by process (`http_server_request_duration_seconds` filtered on
  `service_name=~"customer-payments.*"`), Go goroutines and memory, and Loki
  panels over `{app="customer-payments"}` (live stream, volume by level, errors
  and warnings only).
- **Helm values** `helm-values/customer-payments.yaml`: the MCP component is
  left OFF there; the A2A agent is on, with the route `/api/a2a/customer-payments`.
- **Alert rules**: `terraform/alerting/warehouse-alert-rules.json` has no group
  for this context (the groups present cover other applications; a group per
  application carries the `WarehouseKong5xxRatioHigh`, `WarehousePodRestarting`
  and SLO burn-rate rules). Nothing alerts on this service's own signals today,
  in particular not on `payments.authorizations.decided`, the parked-facts gauge
  or unpublished outbox rows.

The dashboard has no panel for `payments.authorizations.decided` or for
`customer_payments_parked_facts`; query the counter in Prometheus directly
(the exact series name depends on the Collector's OTLP-to-Prometheus
translation; look for `payments_authorizations_decided`).
