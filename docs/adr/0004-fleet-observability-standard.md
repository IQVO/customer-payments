# ADR 0004: Adopt the fleet observability standard (logs, metrics, traces)

## Status

Accepted (2026-10-26).

## Context

customer-payments logged JSON with `log/slog` and nothing else: no trace or span id on a log line,
no request id, no request log, no metrics, no traces and no `traceparent` on the events it publishes.
The warehouse-systems services (inventory-storage ADR 0016, wes-work-planning ADR 0013, "standard
metrics convention") and customer-ordering (ADR 0007) already share one pipeline and one convention:

- pods export OTLP/gRPC to the in-cluster Collector
  (`otel-collector.observability.svc.cluster.local:4317`), which feeds Jaeger (traces) and a
  Prometheus exporter (metrics);
- logs stay on stdout as JSON for Alloy and Loki.

Without the same wiring a payment could not be followed from the authorize call through the Kafka
events to the consumers, a log line could not be joined to its trace, and no dashboard could show
authorization rate, declines or latency. A service-specific setup was rejected (every repo should answer
the same query the same way); so were OTel auto-instrumentation agents (they cannot follow a trace
across `segmentio/kafka-go`, nor name spans by the chi route pattern).

## Decision

Tier 1 (identical in every repo) and Tier 2 (this context's business metric):

1. **Telemetry adapter.** `internal/adapters/outbound/telemetry` holds `Setup` (OTLP/gRPC traces and
   metrics, W3C TraceContext + Baggage propagator, Go runtime metrics; exporters dial lazily, so no
   Collector means dropped telemetry, never a blocked boot or request; returns a shutdown func) and
   `WithTraceContext` (stamps `trace_id`/`span_id` on a record written with a span-carrying context).
   Only the composition roots and the adapters import it; `internal/domain` and `internal/application`
   never import OpenTelemetry.
2. **Logger.** `slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, ...)))`, `LOG_LEVEL`,
   uppercase levels. Request and message paths use the `*Context` slog variants.
3. **HTTP middleware order** on both chi routers (REST and MCP): `RequestID` -> `otelchi.Middleware`
   -> `otelchimetric.NewServerRequestDuration` -> `RequestLogger` -> `Recoverer`; `rejectNUL` and the
   Idempotency-Key middleware stay after them. `telemetry.Setup` runs before the router is built and is
   flushed last on shutdown. The request line carries `method`, `route` (the chi pattern, never the raw
   path), `status`, `duration_ms`, `bytes`, `request_id`; 5xx is ERROR, 4xx WARN, the rest INFO;
   `/healthz` and `/readyz` are not logged. `RequestLogger` lives in
   `internal/adapters/inbound/requestlog` because `cmd/mcp` may not import the REST adapter.
4. **Traces over Kafka.** `OutboxRepo.Append` stamps `traceparent`/`tracestate` from the active span
   into the stored outbox headers inside the transaction that writes the aggregate
   (`internal/adapters/outbound/postgres/outbox_trace.go`). The relay publishes them unchanged; each
   inbound consumer (checkout, order-sourcing, shipping-and-tracking) extracts them and runs every
   handling attempt in a consumer span named `<topic> process`. Only extra headers are added: the
   CloudEvents attributes and body are unchanged (ADR 0003).
5. **Environment contract.** `cmd/api` and `cmd/mcp` read `OTEL_SERVICE_NAME` (defaults
   `customer-payments` and `customer-payments-mcp`), `OTEL_EXPORTER_OTLP_ENDPOINT` (default
   `telemetry.DefaultEndpoint`), `SERVICE_VERSION`, `ENVIRONMENT` and `LOG_LEVEL`.
6. **Tier 2 business metric.** One counter, `payments.authorizations.decided`, unit `{authorization}`,
   attribute `outcome` (`authorized` | `declined` | `failed`) and, except when authorized, `reason`: the
   provider's decline code for `declined` (a small set such as `CARD_DECLINED`), and for `failed` one of
   `payment-provider-unavailable`, `allocation-mismatch`, `concurrent-update`, `validation-failed`,
   `internal-error`. `ports.PaymentMetrics` is the application port; `AuthorizePayment` calls it once per
   attempt (a replay returning the session's existing authorization is not an attempt); the OTel
   implementation is in the telemetry adapter and injected from `cmd/api`; a nil `Deps.Metrics` is a
   no-op. Choice rationale: authorization at checkout (`POST /payment-authorizations`, ADR 0001) is the
   context's primary command and the only one on the shopper's critical path; a decline is a successful
   command with a DECLINED authorization, so it is an outcome, not an error, and a falling authorized
   share separates "shoppers' payment methods are refused" from "the provider is down", which request
   status codes alone cannot. Capture, release and refund are driven by consumed events and are not
   counted here.

## Assumptions (made headless, nearest the reference)

- No Helm chart exists in this repo (ecommerce repos have none yet), so no `otel.*` values, chart env
  or `render_test.sh` change were made and no chart was created. When a chart arrives it must render
  the env contract above for api and mcp.
- No `docs/runtime.md` or ADR `index.md` exists; the README gains the "Observability" section instead.
- The existing `/metrics` parked-facts gauge is a hand-written Prometheus endpoint and is left as is
  (out of scope); the standard's OTLP path does not depend on it.
- The ADR is number 0004, the next free one. Dashboards and alerts live in warehouse-infra and are
  generated per deployed context; this context is not deployed yet.

## Consequences

- A request, its log lines, the events it enqueued and the consumers that handle them share one trace id.
- Request rate, errors and duration come from `http.server.request.duration` with no hand-rolled counter;
  payment health from `payments.authorizations.decided`.
- Every new log call on a request or message path must use a `*Context` variant.
- A consumer span is created per handling attempt, so a retried message shows as several spans under the
  same parent; the parent is the span active at the outbox write, the relay adds no span of its own.
- `OutboxRepo.Insert` (the raw path used by tests) stamps nothing; only `Append` does.
- Policy-driven events (clock passes) are enqueued with no active span and carry no `traceparent`.
