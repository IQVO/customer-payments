# customer-payments

Storefront (e-commerce) domain of the retail-network organisation, bounded context **Customer Payments** (Generic, storefront tier).

Moves money with the shipment facts: authorize at checkout, capture each shipment group's allocation when it ships, release on cancellation, refund on lost or returned packages, re-authorize uncaptured allocations before the provider's authorization validity expires [9]. Speaks to the payment provider through an Anti-Corruption Layer port (Authorize, Capture, Void, Refund, Reauthorize); the payment-provider-simulator is the stand-in adapter and swapping it for a real provider changes nothing else. Never sees a card number, only PSP tokens [11][12].

It owns the `PaymentAuthorization` (one per checkout session, with allocations per shipment group) and `Refund` aggregates. It does **not** own the checkout session, the fulfillment assignment or the package journey facts — it consumes checkout's, order-sourcing's and shipping-and-tracking's published events. This repository is part of the storefront domain: it consumes the retail network's Open Host Service routes and its published `retailnetwork.*` events; it never calls an in-org or restricted network route.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message (`com.retailnetwork.storefront.customer-payments.<entity>.<Event>`), transactional outbox. The design lives in [warehouse-docs](https://iqvo.github.io/warehouse-docs/storefront) `storefront/model/model.yaml` (single source of truth).

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

## Documentation

This repository has no docs site of its own. Its design documentation lives on the warehouse-docs storefront pages:

| Area | Pages |
|---|---|
| Storefront domain | [Overview](https://iqvo.github.io/warehouse-docs/storefront), [Edge and supporting components](https://iqvo.github.io/warehouse-docs/storefront/components) (the payment simulator) |
| This context | [Index / service facts](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments), [Business context](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/business-context), [Domain events](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/domain-events), [Use cases](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/use-cases) |
| DDD artifact pack | [Ubiquitous language](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/ubiquitous-language), [Context map](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/context-map), [Core domain chart](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/core-domain-chart), [Bounded context canvas](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/bounded-context-canvas), [Aggregate design canvas](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/aggregate-design-canvas), [EventStorming](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/eventstorming), [Class diagram](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/class-diagram), [Sequence diagrams](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/sequence-diagrams), [ER diagram](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/entity-relationship), [Domain message flow](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments/domain-message-flow) |
| Decisions & contracts | [Storefront ADRs](https://iqvo.github.io/warehouse-docs/storefront/adr), [API reference](https://iqvo.github.io/warehouse-docs/storefront/api-reference) |

In-tree: [docs/adr/](docs/adr/) (decisions), [docs/operations/runbook.md](docs/operations/runbook.md) (runtime shape, shutdown order, misbehaviour playbooks), `docs/before-first-release.md` (packaging checklist), `SPEC.md`, `HARNESS.md`, `EVALS.md`.

## Architecture

Hexagonal / ports & adapters. Dependencies point inward only:

```
cmd/api/                     composition root: REST API + outbox relay + the three
                             Kafka consumers + the clock-driven policy loop, on real
                             Postgres and real Kafka (no in-memory mode, no log sink)
cmd/mcp/                     read-only MCP server (Streamable HTTP on /mcp)
cmd/a2a/                     read-only A2A agent (JSON-RPC over HTTP, Agent Card at
                             /.well-known/agent-card.json)
internal/
  domain/                    paymentauthorization, refund, shared — pure business logic
  application/
    ports/                   OUT interfaces only (repos, EventPublisher, Clock, provider)
    usecases/                one struct per use case
    idempotency/             the Idempotency-Key machinery
    outbox/ parked/          outbox orchestration; facts parked until their order binds
    provider/ views/         provider port errors; the read models
  adapters/
    inbound/http/            chi handlers, RFC 9457 problem mapping, idempotency middleware
    inbound/kafka/           checkout, order-sourcing, shipping-and-tracking consumers
    inbound/mcp/             the four read-only MCP tools
    inbound/a2a/             the two read-only A2A skills
    inbound/requestlog/      the request log line
    kafka/cloudevents/       the CloudEvents 1.0 codec (structured content mode)
    outbound/postgres/       pgxpool repos, embedded golang-migrate, UnitOfWork
    outbound/outbox/         transactional outbox writer + relay
    outbound/paymentprovider/ the ACL adapter: HTTP calls to PAYMENT_PROVIDER_URL
    outbound/kafka/          the topic publisher behind the relay
    outbound/telemetry/      OTel wiring + the business counter
    outbound/clock/ ids/     wall clock, id source
    outbound/memory/         test doubles only, never linked into cmd/** (fitness-tested)
    outbound/repocontract/   the repository contract tests
  architecture/              the fitness tests below
  bdd/ bootretry/ testing/   godog wiring, boot retry, shared test helpers
apis/                        openapi.yaml (REST) + asyncapi.yaml (events)
docs/adr/                    5 ADRs
features/                    godog scenarios (authorization, capture_release, refunds)
charts/customer-payments/    the Helm chart
```

The layering is an executable test, not a convention: `internal/architecture/architecture_test.go` (`TestHexagonalArchitecture`) fails the build if the domain ever imports an outer layer or an adapter reaches sideways into a sibling. The same package pins the rest of the shape: CloudEvents-only envelopes and the event catalogue (`catalogue_fitness_test.go`, `events_fitness_test.go`), Kafka DLQ/acks/topic rules (`kafka_*_fitness_test.go`), the outbox relay attempt cap (`outbox_relay_cap_fitness_test.go`), `runtime_no_memory_fitness_test.go` (no in-memory adapter in any binary), and the read-only MCP/A2A surfaces (`mcp_optional_inputs_test.go`, `a2a_fitness_test.go`).

## Domain events

All facts leave as CloudEvents 1.0 (structured content mode, `application/cloudevents+json`); the envelope and type catalogue are [ADR 0003](docs/adr/0003-cloudevents-envelope-and-type-catalogue.md). The type convention is `com.retailnetwork.storefront.customer-payments.<entity>.<Event>`, for example:

```
com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized
com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentReauthorized
com.retailnetwork.storefront.customer-payments.refund.RefundIssued
```

Specified in `apis/asyncapi.yaml`:

| Topic | Direction | Messages |
|---|---|---|
| `retailnetwork.customer-payments.events` | publishes (via the transactional outbox) | `PaymentAuthorized`, `PaymentCaptured`, `PaymentReleased`, `PaymentAuthorizationExpired`, `PaymentDeclined`, `PaymentReauthorized`, `RefundIssued`, `RefundFailed` |
| `retailnetwork.checkout.events` | consumes | `CheckoutCompleted` |
| `retailnetwork.order-sourcing.events` | consumes | `FulfillmentShipped`, `FulfillmentCancelled`, `FulfillmentUnsourceable` |
| `retailnetwork.shipping-and-tracking.events` | consumes | `PackageLost`, `PackageReturned` |

Captures, releases and re-authorizations arrive as Kafka events or run in the policy loop — never as REST writes. Facts still travel only as CloudEvents on Kafka; MCP carries tools, A2A carries delegation, never data sync.

## Running it

`cmd/api` needs Postgres and Kafka; there is no in-memory mode (a missing mandatory variable is a boot error naming it, and the relay, the policy loop and all three consumers always run — a worker returning on its own exits the process non-zero). The repo has no compose file: bring your own Postgres and Kafka (the integration suite starts its own via testcontainers).

```sh
export DATABASE_URL="postgres://user:pass@localhost:5432/customer_payments?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"
export PAYMENT_PROVIDER_URL="http://localhost:8081"          # the payment-provider-simulator
export CHECKOUT_CONSUMER_GROUP=customer-payments-checkout
export ORDER_SOURCING_CONSUMER_GROUP=customer-payments-order-sourcing
export SHIPPING_AND_TRACKING_CONSUMER_GROUP=customer-payments-shipping-and-tracking
go run ./cmd/api        # migrations run at boot; REST on :8080, /readyz gates on Postgres+Kafka
```

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address |
| `DATABASE_URL` | **required** | Postgres DSN |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations |
| `KAFKA_BROKERS` | **required** | Comma-separated brokers (relay, consumers, `/readyz`) |
| `PAYMENT_PROVIDER_URL` | **required** | Base URL of the payment provider API (the simulator in dev) |
| `CHECKOUT_CONSUMER_GROUP` | **required** | Stable group id of the checkout consumer |
| `ORDER_SOURCING_CONSUMER_GROUP` | **required** | Stable group id of the order-sourcing consumer |
| `SHIPPING_AND_TRACKING_CONSUMER_GROUP` | **required** | Stable group id of the shipping-and-tracking consumer |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Relay poll interval (Go duration or seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row (`0` = unlimited) |
| `POLICY_INTERVAL` | `5s` | Clock-driven policy pass interval (re-authorization) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait after flipping `/readyz` before closing |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `OTEL_SERVICE_NAME` | `customer-payments` | `service.name` of spans and metrics |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | telemetry default | Collector OTLP/gRPC endpoint; export never blocks |
| `SERVICE_VERSION` | `dev` | `service.version` |
| `ENVIRONMENT` | `local` | `deployment.environment.name` |

`cmd/mcp` (read-only: no Kafka, no provider, no writes — it can never insert into the outbox):

| Env var | Default | Meaning |
|---|---|---|
| `MCP_ADDR` | `:8090` | Listen address (Streamable HTTP `/mcp` + `GET /healthz`) |
| `DATABASE_URL` | **required** | Postgres DSN (the service database `cmd/api` owns) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations |
| `LOG_LEVEL` | `info` | log level |
| `OTEL_SERVICE_NAME` | `customer-payments-mcp` | `service.name` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | telemetry default | Collector endpoint |
| `SERVICE_VERSION` / `ENVIRONMENT` | `dev` / `local` | resource attributes |

`cmd/a2a` (read-only, same rules as `cmd/mcp`):

| Env var | Default | Meaning |
|---|---|---|
| `A2A_ADDR` | `:8093` | Listen address (JSON-RPC at `/` and `/a2a`) |
| `A2A_AGENT_URL` | **required** | Absolute http(s) URL advertised on the Agent Card |
| `DATABASE_URL` | **required** | Postgres DSN |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations |
| `LOG_LEVEL` | `info` | log level |
| `OTEL_SERVICE_NAME` | `customer-payments-a2a` | `service.name` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | telemetry default | Collector endpoint |
| `SERVICE_VERSION` / `ENVIRONMENT` | `dev` / `local` | resource attributes |

## Observability

Logs, metrics and traces follow the fleet standard ([ADR 0004](docs/adr/0004-fleet-observability-standard.md)): JSON logs on stdout carry `trace_id`/`span_id` and a `request_id`; `cmd/api` and `cmd/mcp` export OTLP/gRPC to the Collector (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `SERVICE_VERSION`, `ENVIRONMENT`; export never blocks, so no Collector costs telemetry, nothing else) with HTTP request duration, Go runtime metrics and the business counter `payments.authorizations.decided` (`outcome`, `reason`); the trace continues across Kafka in the `traceparent` header of each published event and is picked up by the consumers. `cmd/api` additionally serves `GET /metrics` as a Prometheus text exposition of one gauge, `customer_payments_parked_facts` (fulfilment facts parked until their order is bound).

## API

REST contract: [`apis/openapi.yaml`](apis/openapi.yaml) — called by checkout (authorize, void) and by storefront ops (refunds, reads). Every write takes an `Idempotency-Key` header; provider keys are deterministic per (operation, authorizationId, groupNo). No authentication. Errors are RFC 9457 problem details (`application/problem+json`) whose `type` is `/problems/<slug>`: shared types `not-found` (404), `illegal-transition` (409), `concurrent-update` (409), `validation-failed` (422), plus one kebab-case slug per model error.

Routes served by `cmd/api`:

| Method | Route | Purpose |
|---|---|---|
| `POST` | `/payment-authorizations` | Authorize at checkout (Idempotency-Key) |
| `GET` | `/payment-authorizations` | List; exactly one of `orderId` or `checkoutSessionId` required |
| `GET` | `/payment-authorizations/{authorizationId}` | Get one |
| `POST` | `/payment-authorizations/{authorizationId}:void` | Void before capture (Idempotency-Key) |
| `POST` | `/refunds` | Request a refund (Idempotency-Key) |
| `GET` | `/refunds` | List refunds |
| `GET` | `/refunds/{refundId}` | Get one refund |
| `POST` | `/refunds/{refundId}:retry` | Retry a failed refund (Idempotency-Key) |
| `GET` | `/healthz`, `/readyz`, `/metrics` | Liveness, readiness (gates on Postgres+Kafka), the parked-facts gauge |

## Helm chart

`charts/customer-payments` deploys `cmd/api` as one Deployment running the REST API, the outbox relay, the three consumers and the policy loop. Values that matter: `config.httpAddr` (must equal `service.targetPort`), `config.paymentProviderUrl`, the three `config.*ConsumerGroup` values, `database.url` (or `existingSecret`), `kafka.brokers`, `otel.enabled` (on by default; `false` renders no `OTEL_*` env at all) and `environment`. The chart refuses a `terminationGracePeriodSeconds` that does not exceed the worst-case drain (defaults to `70`).

```sh
helm upgrade --install customer-payments ./charts/customer-payments \
  --set database.url="postgres://user:pass@postgres:5432/customer_payments?sslmode=disable" \
  --set kafka.brokers="kafka:9092" \
  --set config.paymentProviderUrl="http://payment-provider-simulator:8080"
```

Linted and rendered in CI by the `helm-lint` (`ct lint`) and `chart-tests` jobs.

### Running the MCP server in Kubernetes

Both agent interfaces are optional, read-only, unauthenticated (fleet-wide decision) and off by default in the chart; neither can move money.

`cmd/mcp` (`internal/adapters/inbound/mcp`) becomes a separate `<release>-mcp` Deployment + ClusterIP Service on `:8090` with `mcp.enabled=true`. It serves the four read tools `get_payment_authorization`, `list_payment_authorizations`, `get_refund`, `list_refunds` over Streamable HTTP on `/mcp`, over the same read use cases as the REST GETs; `GET /healthz` backs the probes. There is no auth of any kind: access control is the in-cluster ClusterIP boundary. The Kong gateway route `/api/mcp/customer-payments` rewrites to `/mcp`.

### Running the A2A agent in Kubernetes

`cmd/a2a` ([ADR 0005](docs/adr/0005-a2a-inbound-adapter.md), `internal/adapters/inbound/a2a`) becomes a `<release>-a2a` Deployment + ClusterIP Service on `:8093` with `a2a.enabled=true`. It accepts a **delegated goal** as a message and answers in-band; skills `get_payment_status` (by `authorizationId`, `orderId` or `checkoutSessionId`) and `get_refund_status` (by `refundId` or `orderId`). JSON-RPC at `/` and `/a2a`, Agent Card at `/.well-known/agent-card.json`; `A2A_ADDR` default `:8093`, `A2A_AGENT_URL` (absolute http(s) URL advertised on the card) is mandatory and defaults in the chart to the in-cluster Service DNS. Problems (404/400/422/500) travel in the reply envelope, never as JSON-RPC errors.

## Local development / quality gate

Make is the single entry point (`make help` lists everything): `make check` (format check, vet, build, lint, unit tests + handler and godog scenarios), `make check-fast` (format check, vet, arch tests), `make check-all`, `make coverage`, `make integration` (testcontainers Postgres and Kafka, needs Docker), `make bdd`, `make mutation` / `make mutation-full` (gremlins, thresholds in `.gremlins.yaml`), `make arch-test`, `make vuln` (govulncheck), `make guide-lint`, `make harness-test`.

lefthook wires the same targets as hooks (`lefthook install` once per clone): pre-commit runs `make fmt-check`, `make vet`, `make lint`; pre-push runs `make check`. `integration` and `mutation` are deliberately excluded from the hooks so committing and pushing stay fast and offline-friendly.

## Tests

- **Unit**: `make test` — domain, use cases, handlers, adapters.
- **BDD**: `make bdd` — godog scenarios in `features/` (`authorization.feature`, `capture_release.feature`, `refunds.feature`).
- **Integration**: `make integration` — testcontainers Postgres + Kafka (consumers, relay, wiring, `/readyz`).
- **Mutation**: `make mutation` (blocking subset) / `make mutation-full` — gremlins over `internal/domain`, config in `.gremlins.yaml`.
- **Architecture fitness**: `make arch-test` — `internal/architecture/` (hexagonal rule, CloudEvents-only, catalogue, Kafka rules, outbox cap, no-memory-in-binaries, read-only MCP/A2A).

CI (`.github/workflows/ci.yml`) runs `lint`, `guide-lint`, `complexity`, `test`, `bdd`, `integration`, `mutation-fast`, `api-lint`, `vuln`, `helm-lint`, `chart-tests` and `arch-test` on every push and PR; the exhaustive `mutation` job runs on schedule; `docker-publish` pushes the image and `release` tags and publishes the chart.

## Architecture Decision Records

[docs/adr/](docs/adr/) — 5 ADRs (0001–0005: bounded context, assumptions, CloudEvents envelope and type catalogue, fleet observability standard, A2A inbound adapter). Cross-repo index: the [storefront ADR pages](https://iqvo.github.io/warehouse-docs/storefront/adr). See `docs/before-first-release.md` for the packaging checklist.

## License

[MIT](LICENSE).
