---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

This page takes you from a clean checkout to a running `customer-payments`
with real Postgres and real Kafka, and shows the first calls against it. The
outputs below were captured from a real run of the binaries built from this
branch (Postgres 16 in Docker, the fleet's single Kafka broker on
`localhost:9092`, the `payment-provider-simulator` as the payment provider).

## Prerequisites

| Need | Why |
| --- | --- |
| Go `1.27.3` (`go.mod`) | build and test |
| Docker | a local Postgres for a manual run; the integration suite starts its own Postgres and Kafka with testcontainers |
| One Kafka broker reachable at `localhost:9092` | the fleet's kind cluster publishes its single broker there; the docker-compose Kafka is retired, so do not start another one |
| `golangci-lint` v2.14.0, `gremlins` v0.6.0, `govulncheck` (optional) | `make lint`, `make mutation`, `make vuln` print the exact install command when the tool is missing |
| The `payment-provider-simulator` repository | the stand-in for the payment provider (`PAYMENT_PROVIDER_URL`); any HTTP service speaking the same contract works |
| `lefthook install` (once per clone) | pre-commit runs `fmt-check`, `vet`, `lint`; pre-push runs `make check` |

## Make targets

`make help` lists them all. The ones you will use:

| Target | What it runs |
| --- | --- |
| `make build` | `go build ./...` |
| `make test` | `go test ./... -race` (unit, handler and godog scenarios; no database needed) |
| `make check` | `fmt-check vet build lint test` (the pre-push gate) |
| `make check-all` | `check` + `coverage` (90 % gate) + `arch-test` + `bdd` |
| `make integration` | `go test -tags=integration ./... -race -count=1` (testcontainers Postgres and Kafka, needs only Docker) |
| `make bdd` | `go test ./... -run TestFeatures -v` (godog) |
| `make arch-test` | `go test ./internal/architecture/... -v` (fitness tests) |
| `make mutation` / `make mutation-full` | gremlins over `internal/domain` |
| `make vuln` | `govulncheck ./...` |

There is no `make run` target: you start the binaries directly, as below.

## 1. Postgres and the payment provider

The service owns its own database. The simulator needs one too; here both live
in one throwaway container:

```sh
docker run -d --name cp-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=customer_payments -p 55442:5432 postgres:16
docker exec cp-pg psql -U postgres -c "CREATE DATABASE simulator"
```

Start the simulator from its own checkout (`HTTP_ADDR` and `DATABASE_URL` are
its variables; it applies its migrations at boot):

```sh
cd ../payment-provider-simulator
HTTP_ADDR=:18081 DATABASE_URL="postgres://postgres:pw@localhost:55442/simulator?sslmode=disable" go run ./cmd/api
```

## 2. Start `cmd/api`

```sh
export DATABASE_URL="postgres://postgres:pw@localhost:55442/customer_payments?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"
export PAYMENT_PROVIDER_URL="http://localhost:18081"
export CHECKOUT_CONSUMER_GROUP=cp-doc-checkout
export ORDER_SOURCING_CONSUMER_GROUP=cp-doc-order-sourcing
export SHIPPING_AND_TRACKING_CONSUMER_GROUP=cp-doc-shipping
go run ./cmd/api
```

Migrations run at boot, then the REST server, the three consumers, the outbox
relay and the policy loop start. Every variable is explained in
[Configuration](/docs/operations/configuration). Without the mandatory ones the
process refuses to boot and names the variable:

```text
{"time":"2026-10-31T13:43:22.248882-03:00","level":"ERROR","msg":"service exited with error","error":"DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode"}
```

Check it is up (`/readyz` is 200 only while Postgres answers a ping and a Kafka
broker answers a metadata request):

```sh
curl -s -i localhost:8080/readyz
```

```text
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 19

{"status":"ready"}
```

The tables the migration created in `customer_payments`: `allocations`,
`idempotency_keys`, `outbox_events`, `parked_facts`, `payment_authorizations`,
`processed_events`, `refunds` (plus golang-migrate's `schema_migrations`).

## 3. First calls

REST is unauthenticated. Every `POST` needs an `Idempotency-Key` header.

**Authorize a checkout session** (two shipment groups, simulator token `tok_approve`):

```sh
curl -s -X POST localhost:8080/payment-authorizations \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: doc-auth-1' \
  -d '{"checkoutSessionId":"cs-demo-1","paymentMethodToken":"tok_approve","currency":"USD","totalMinor":12500,"allocations":[{"groupNo":1,"amountMinor":7500},{"groupNo":2,"amountMinor":5000}]}'
```

```json
{"authorizationId":"pay-69aac12569b7","checkoutSessionId":"cs-demo-1","orderId":null,"state":"AUTHORIZED","currency":"USD","totalMinor":12500,"allocations":[{"groupNo":1,"amountMinor":7500,"state":"PENDING","trackingNumber":null,"trackingNumbers":[]},{"groupNo":2,"amountMinor":5000,"state":"PENDING","trackingNumber":null,"trackingNumbers":[]}],"providerAuthorizationId":"hold_9fad7c0285ef","expiresAt":"2026-11-07T16:45:07.435808Z"}
```

Status `201`. Repeating the same request with the same key replays the stored
answer (status `201`, header `Idempotent-Replayed: true`). The same key with a
different body is `422`:

```json
{"type":"/problems/idempotency-key-reused","title":"Idempotency-Key was already used with a different request","status":422,"detail":"the Idempotency-Key was used with a different request","instance":"/payment-authorizations"}
```

No key at all is `400`:

```json
{"type":"/problems/idempotency-key-required","title":"Idempotency-Key header is required","status":400,"detail":"send an Idempotency-Key header of 1 to 128 characters, unique per logical request","instance":"/payment-authorizations"}
```

**Read it back** (by id, or list by exactly one of `orderId` and `checkoutSessionId`):

```sh
curl -s localhost:8080/payment-authorizations/pay-69aac12569b7
curl -s 'localhost:8080/payment-authorizations?checkoutSessionId=cs-demo-1'
curl -s localhost:8080/payment-authorizations
```

The last call has no filter and answers `422`:

```json
{"type":"/problems/validation-failed","title":"Validation failed","status":422,"detail":"validation failed: exactly one of orderId and checkoutSessionId is required","instance":"/payment-authorizations"}
```

**A decline** (`tok_decline`) is a stored, successful answer (status `201`) whose
state is `DECLINED`:

```json
{"authorizationId":"pay-83f2ebd5fa1c","checkoutSessionId":"cs-demo-2","orderId":null,"state":"DECLINED","currency":"USD","totalMinor":3000,"allocations":[{"groupNo":1,"amountMinor":3000,"state":"PENDING","trackingNumber":null,"trackingNumbers":[]}],"providerAuthorizationId":null,"declineReason":"CARD_DECLINED"}
```

**Void before capture** releases every allocation:

```sh
curl -s -X POST 'localhost:8080/payment-authorizations/pay-69aac12569b7:void' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: doc-void-1' -d '{}'
```

```json
{"authorizationId":"pay-69aac12569b7","checkoutSessionId":"cs-demo-1","orderId":null,"state":"VOIDED","currency":"USD","totalMinor":12500,"allocations":[{"groupNo":1,"amountMinor":7500,"state":"RELEASED","trackingNumber":null,"trackingNumbers":[]},{"groupNo":2,"amountMinor":5000,"state":"RELEASED","trackingNumber":null,"trackingNumbers":[]}],"providerAuthorizationId":"hold_9fad7c0285ef","expiresAt":"2026-11-07T16:45:07.435808Z"}
```

An unknown id is a problem document:

```json
{"type":"/problems/not-found","title":"Not found","status":404,"detail":"authorization nope: not found","instance":"/payment-authorizations/nope"}
```

**Refunds** are requested against an order whose payment authorization is
bound to it (the binding comes from the `CheckoutCompleted` event). For an
unknown order the answer is:

```sh
curl -s -X POST localhost:8080/refunds -H 'Content-Type: application/json' -H 'Idempotency-Key: doc-refund-1' \
  -d '{"orderId":"ord-demo-1","amountMinor":2500,"reason":"MANUAL"}'
```

```json
{"type":"/problems/not-found","title":"Not found","status":404,"detail":"authorization of order ord-demo-1: not found","instance":"/refunds"}
```

Captures are not REST writes: they come from the Kafka events of
the sibling contexts (see [Integration](/docs/ecosystem/integration)).

## 4. What reached Kafka

Each decision wrote an outbox row in the same transaction; the relay published
them. After the calls above:

```sh
docker exec cp-pg psql -U postgres -d customer_payments -c "SELECT id, event_type, attempts, published_at IS NOT NULL AS published FROM outbox_events ORDER BY id"
```

```text
 id |                                      event_type                                       | attempts | published
----+---------------------------------------------------------------------------------------+----------+-----------
  1 | com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized |        1 | t
  2 | com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentDeclined   |        1 | t
  3 | com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentReleased   |        1 | t
  4 | com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentReleased   |        1 | t
```

The void produced one `PaymentReleased` per allocation. They sit on the topic
`retailnetwork.customer-payments.events` ([Events](/docs/api-reference/events)).

`GET /metrics` serves one Prometheus gauge:

```text
# HELP customer_payments_parked_facts Fulfilment facts parked until their order is bound.
# TYPE customer_payments_parked_facts gauge
customer_payments_parked_facts 8
```

The value is not zero here because the consumers use brand-new group ids, and a
group with no committed offset starts at the first offset: it replayed the whole
shipping-and-tracking topic of the shared broker and parked the facts whose
order this database does not know.

## 5. The read-only MCP server and A2A agent

Both only need `DATABASE_URL` (plus `A2A_AGENT_URL` for the agent). They run the
idempotent migrations too, so they can start before `cmd/api`.

```sh
DATABASE_URL="$DATABASE_URL" go run ./cmd/mcp
DATABASE_URL="$DATABASE_URL" A2A_AGENT_URL=http://localhost:8093/ go run ./cmd/a2a
```

MCP is Streamable HTTP at `/mcp`: `initialize` answers a session id in the
`Mcp-Session-Id` header, then `tools/list` returns the four tools listed in
[MCP tools](/docs/mcp/tools).

```sh
curl -s localhost:8093/.well-known/agent-card.json
curl -s -X POST localhost:8093/ -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"get_payment_status","arguments":{"checkoutSessionId":"cs-demo-2"}}}]}}}'
```

The agent answers in the reply message, structured data first and a text line
second:

```json
{"jsonrpc":"2.0","id":1,"result":{"message":{"messageId":"01a192da-7cfb-7a68-8b4b-ba8098648d91","contextId":"01a192da-7cc3-7b65-bba5-ce85ec3059b2","parts":[{"data":{"skill":"get_payment_status","data":{"authorizations":{"authorizations":[{"authorizationId":"pay-83f2ebd5fa1c","checkoutSessionId":"cs-demo-2","state":"DECLINED","currency":"USD","totalMinor":3000,"allocations":[{"groupNo":1,"amountMinor":3000,"state":"PENDING","trackingNumbers":[]}],"declineReason":"CARD_DECLINED"}]}}}},{"text":"1 payment authorizations: pay-83f2ebd5fa1c DECLINED 3000 USD"}],"role":"ROLE_AGENT","taskId":"01a192da-7cc3-78be-a707-e057885f15d8"}}}
```

Without the `A2A-Version: 1.0` header the SDK answers
`VERSION_NOT_SUPPORTED` (`requested "0.3", served "1.0"`).

## Clean up

```sh
docker rm -f cp-pg
```

## Next

- [Architecture](/docs/overview/architecture) for what the binaries are made of.
- [Testing](/docs/development/testing) for the pyramid and the CI jobs.
- [Runbook](/docs/operations/runbook) for deployment with the Helm chart.
