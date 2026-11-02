# ADR 0006: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Replaces the per-route `Idempotency-Key` store (a get-then-put outside the unit of work),
the 422 answer for key reuse, the `Idempotent-Replayed` header, the `:void` command route and the bare
`{authorizations}` / `{refunds}` list bodies of the REST contract in [ADR 0002](./0002-customer-payments-assumptions.md).

## Context

The 2026-10-30 application portfolio assessment measured the fleet's REST contracts: write operations only
partly keyed, two spellings of the replay header, key reuse answered 422 in most services and 409 in one,
three command-URL styles (`:verb`, verb sub-paths, sub-resources) and lists without paging. customer-payments
showed these:

- one command route in the `:verb` style, `POST /payment-authorizations/{authorizationId}:void`, and an
  outbound client (to the payment-provider-simulator) that called the provider's verb routes
  `/void`, `/reauthorize` and `/reduce`;
- the replay header was spelled `Idempotent-Replayed` and a reused key was answered 422;
- keys were scoped per route (`idempotency_keys` was keyed by `(route, key)`) and stored by a get-then-put that
  ran after, and outside the unit of work of, the aggregate change: two concurrent first requests could both
  run (the commands were individually idempotent through deterministic provider keys, but both ran), and a
  crash between the change and the put left a change without its key row;
- `GET /payment-authorizations` answered `{authorizations: [...]}` and `GET /refunds` answered `{refunds: [...]}`
  with no paging.

The fleet standard (warehouse-docs, "REST API guideline", rules R1-R8) removes each of those. This ADR adopts
it here as a hard cutover: no alias route, no dual header, no toggle.

## Decision

1. **Noun routes (R1, R2).** Status codes and response bodies do not change with a rename; the `operationId`
   stays, so the MCP and A2A mappings do not move.

   | Old | New |
   |---|---|
   | `POST /payment-authorizations/{authorizationId}:void` | `POST /payment-authorizations/{authorizationId}/voids` |

   `TestRetiredRoutesAreGone` asserts the old route answers 404 or 405.
2. **Idempotency (R3-R6, R8).** Every `POST` (`/payment-authorizations`, `.../voids`, `/refunds`, `/refunds/{refundId}:retry`)
   sits behind `RequireIdempotencyKey(store, clock)`, applied per route; there is no `PUT`, `PATCH` or `DELETE`
   route. The key is 1 to 128 printable ASCII characters: absent is 400 `idempotency-key-required`, malformed
   is 400 `idempotency-key-invalid` (a key longer than 128 characters used to be reported as `-required`). The
   fingerprint is method + path + SHA-256 of the body (the query string is no longer part of it): the same key
   and fingerprint replays the stored answer (status, body, `Content-Type`) with `Idempotency-Replayed: true`,
   and only a replay carries the header; the same key with another fingerprint is 409
   `idempotency-key-reused`, never 422. No route was unkeyed before.
3. **A global, durable key store joined to the unit of work (STANDARD 2.5).** The key is global, no longer per
   route (the `route` column of `idempotency_keys` holds the empty string; no migration; rows the per-route store
   wrote under a route name are never read again, and, as before, nothing purges expired rows: an expired row is
   only replaced when its key comes back). The port is
   `ports.IdempotencyStore.Do(ctx, key, requestHash, at, fn)`: Postgres claims the key row inside the transaction
   it opens, hands `fn` that transaction through the context (every unit of work joins it, as a savepoint, so
   the use cases' `ErrConcurrentUpdate` retry loops still start clean), and commits the claim, the aggregate
   change, the outbox rows and the stored answer together. Concurrent requests with one key are serialised by
   the unique index and the loser replays the winner's answer. A 2xx or 4xx answer is stored for 24 hours; a
   5xx or a panic rolls everything back and is never stored, so the retry re-executes. The in-memory store
   implements the same contract and is run by the same repository contract suite as Postgres.
4. **Keyset paging (R7).** `GET /payment-authorizations` (filters `orderId` | `checkoutSessionId`) and
   `GET /refunds` (filter `orderId`) take `limit` (1 to 500, default 100) and an opaque `cursor`, order by
   `(created_at, id)` ascending in both the Postgres and the in-memory repository
   (`AuthorizationRepository.PageBySession`, `RefundRepository.ListByOrder`), use the fleet `page` package
   (`internal/adapters/inbound/http/page`) and answer `{items, nextCursor}`. A bad value is 400 `invalid-limit`
   or `invalid-cursor`, and a query parameter the list does not declare (the retired `after`, `offset`,
   `nextToken`, `from`) is 400 `unknown-query-parameter`. A listing by `orderId` yields at most the one
   authorization bound to the order. The MCP tools `list_payment_authorizations` and `list_refunds` and the A2A
   skills `get_payment_status` and `get_refund_status` keep their own tool schemas but follow every page
   (`ListAuthorizations.All`, `ListRefunds.All`, 100 rows at a time, up to 1000 rows, documented in the tool
   descriptions), so they still return complete data.
5. **Contract and enforcement.** `apis/openapi.yaml` declares the `Idempotency-Key` parameter (required: the
   middleware is active in every runtime mode), 400 and 409 and the `Idempotency-Replayed` header on every 2xx
   of every `POST` (the `200` of `POST /payment-authorizations`, which the spec did not list, is now documented),
   and the `PaymentAuthorizationPage` and `RefundPage` schemas. The `rest-guideline` Spectral rule
   (`.spectral.yaml`, `.spectral/functions/restGuideline.js`) runs in the `api-lint` job with
   `--fail-severity=warn`. This repo has no Schemathesis contract job, so no `schemathesis.toml` is needed.
6. **Consumers changed in this repo.** The payment-provider client (`internal/adapters/outbound/paymentprovider`)
   now calls the simulator's noun routes `POST /authorizations/{id}/voids`, `/reauthorizations` and
   `/reductions` (it already sent an `Idempotency-Key` on every call, and `/captures` and `/refunds` were nouns);
   its `providerfake` test double and a wire-path test follow. There is no web remote and no seed script here.
   Callers of this service (checkout: `internal/adapters/outbound/customerpayments`) update in their own pull
   requests.

### Exemptions

None. `GET /payment-authorizations` and `GET /refunds` are the only collections and both are paged.
`GET /payment-authorizations/{authorizationId}`, `GET /refunds/{refundId}`, `/healthz`, `/readyz` and `/metrics`
are not collections.

### Open question

`POST /refunds/{refundId}:retry` (use case `RetryRefund`) is served by the router and sits behind the
`Idempotency-Key` middleware, but it is not in `apis/openapi.yaml` and not in the fleet rename map, so the
Spectral rule cannot see it and it keeps its `:retry` spelling. STANDARD 1.4 has no transition noun for
"retry"; naming one (and adding the route to the spec) is a fleet decision, not taken here.

## Consequences

- Breaking for every caller: a new void URL, the list envelope, `409` instead of `422` for key reuse. This pull
  request must deploy together with the rest of the REST cutover set (checkout, the payment-provider-simulator
  and the other services); `develop` is half-migrated until the last of them lands.
- A client that reused one `Idempotency-Key` across two different routes used to get two answers; it now gets
  409 `idempotency-key-reused`. A UUID per logical submit avoids it.
- The key row, the aggregate change and the outbox rows can no longer diverge, and two concurrent requests with
  one key call the provider once: a crash rolls all three back.
- The provider call now happens inside the transaction that holds the key row, like it already did inside the
  unit of work; a slow provider holds a connection for the length of its timeout.
- Keyset paging cannot skip or repeat a row that shares a creation instant with its neighbour; the walk is
  covered by the repository contract suite (memory and Postgres), the HTTP tests and a Postgres integration
  test.
