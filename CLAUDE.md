# CLAUDE.md — customer-payments

`customer-payments` is the **Customer Payments** bounded context (Generic,
`storefront` tier) of the retail-network organisation's storefront domain
(GitHub org `IQVO`, Go module `github.com/IQVO/customer-payments`). It moves
money with the shipment facts: authorize at checkout, capture each shipment
group's allocation when it ships, release on cancellation, refund on lost or
returned packages, and re-authorize uncaptured allocations before the
provider's validity expires. It owns the `PaymentAuthorization` (one per
checkout session, allocations per shipment group) and `Refund` aggregates. It
talks to the payment provider only through an anti-corruption port;
payment-provider-simulator is today's adapter target. It never sees a card
number, only PSP tokens. Binaries: `cmd/api` (REST + outbox relay + three
Kafka consumers + the policy loop), `cmd/mcp` (read-only MCP, `:8090`) and
`cmd/a2a` (read-only A2A, `:8093`). No `web/`.

## Hard rules (each one cost an incident or is fitness-tested)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal
   but domain; `internal/application` only on domain and application; adapters
   never depend on each other; only `cmd/` wires layers. `make arch-test`
   (`TestHexagonalArchitecture`) enforces it.
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or
   consumed (structured mode, no flat envelope, no dual-write, no toggle:
   `TestNoEventEnvelopeToggleOrFlatEnvelope`, `TestCloudEventsOnly`). Build and
   decode only through `internal/adapters/kafka/cloudevents/`; `type` is
   `com.retailnetwork.storefront.customer-payments.<entity>.<Event>`. A breaking
   payload change is a new `.v2` type and dataschema, never a mutation.
   Catalogue: `docs/adr/0003-cloudevents-envelope-and-type-catalogue.md`, kept
   in step with `apis/asyncapi.yaml` by `TestEventCatalogueMatchesContract`.
3. **Events leave through the transactional outbox only**: the aggregate save
   and the outbox row commit in one transaction; the always-on relay in
   `cmd/api` publishes to `retailnetwork.customer-payments.events`, capped by
   `OUTBOX_MAX_ATTEMPTS` (`TestOutboxRelaysPassAttemptCap`).
4. **Consumers dedupe on the CloudEvents `id`**, dispatch on the full `type`
   string and dead-letter poison messages, but never on shutdown cancellation
   (`TestDeadLetterCallsGuardCancellation`). Group ids come from the mandatory
   `CHECKOUT_CONSUMER_GROUP`, `ORDER_SOURCING_CONSUMER_GROUP` and
   `SHIPPING_AND_TRACKING_CONSUMER_GROUP` variables, never a literal
   (`TestKafkaConsumerGroupNeverHardcodedInline`).
5. **Facts for an unbound order are parked**, applied on
   BindOrderOnCheckoutCompleted (cross-topic ordering), purged after 30 days
   and dead-lettered with an alert after 24 hours. Never drop or reorder them.
6. **Money invariants live in the domain**: allocations always sum to the
   authorized total (`ErrAllocationMismatch`); no over-capture, no capture past
   `expiresAt`; refunds never exceed the captured total (`ErrOverRefund`); a
   FAILED refund is retried up to 5 attempts. Captures, releases and
   re-authorizations come from Kafka facts or the policy loop, never REST.
7. **The provider sits behind the ACL port** (Authorize, Capture, Void,
   Refund, Reauthorize; `internal/adapters/outbound/paymentprovider`). Provider
   idempotency keys are deterministic per (operation, authorizationId,
   groupNo). Swapping the provider must change nothing outside that adapter.
8. **Every POST is behind `RequireIdempotencyKey`** (ADR 0006): a global
   1-128 printable-ASCII key, replays carry `Idempotency-Replayed: true`, the
   same key with another request is 409. Errors are RFC 9457 problems.
9. **Real Postgres and Kafka only**; the memory adapters are test doubles
   (`TestRuntimeBinaryHasNoInMemoryAdapters`), integration tests use
   testcontainers only.
10. **No auth layer on REST, MCP or A2A** (fleet-wide revert 2026-09-11,
    `TestNoAuthMiddlewareReintroduced`). MCP and A2A are read-only and can
    never move money. Every MCP tool and A2A skill declares an authority level
    (ADR 0007; all L1 today): `TestEveryMCPToolDeclaresAuthority`,
    `TestA2ASkillsDeclareAuthority`, `TestMCPServerOffersResourceAndPrompt`.
    The level is a label, not access control.
11. **An ADR comes first** for a new consumed or published event, a new
    provider operation, an L3/L4 agent surface or any auth. ADRs live in
    `docs/adr/`.

## Commands

```bash
make check-fast   # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check        # fmt-check vet build lint test
make check-all    # check + coverage (90% gate on domain+application) + arch-test + bdd
make integration  # real Postgres/Kafka via testcontainers (needs Docker)
make mutation     # gremlins over every internal/domain package (CI job mutation-fast, blocking)
make vuln         # govulncheck ./...
make guide-lint   # these guides: skills load, references resolve, context budget
```

Run locally: `DATABASE_URL=... KAFKA_BROKERS=... PAYMENT_PROVIDER_URL=...`
plus the three `*_CONSUMER_GROUP` variables, then `go run ./cmd/api` (REST
`:8080`, migrations at boot, `/readyz` gates on Postgres and Kafka);
`go run ./cmd/mcp` and `A2A_AGENT_URL=... go run ./cmd/a2a` need only
`DATABASE_URL`.

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`: ubiquitous language, aggregates, events, use
  cases and policies. `.claude/rules/integration-events.md`: published and
  consumed types, outbox, consumers. `.claude/rules/rest-api.md` is still the
  template skeleton: the routes are in `apis/openapi.yaml` and
  `internal/adapters/inbound/http/server.go`.
- `.claude/rules/fleet/*.md`: fleet-wide rules copied from the harness
  template (including `agent-authority.md`); never edit them here.
- `docs/adr/`: decisions (index: `docs/adr/index.md`). `apis/openapi.yaml`,
  `apis/asyncapi.yaml`: contracts (`api-lint` runs the Spectral
  `rest-guideline` ruleset from `.spectral.yaml`). Operating docs:
  `docs/docs/operations/` (configuration, runbook, observability,
  troubleshooting), `docs/docs/development/testing.md`,
  `docs/docs/ecosystem/integration.md`, `docs/docs/mcp/tools.md`.
  `HARNESS.md`: what each sensor runs.
- `charts/customer-payments/`: the Helm chart (api always on; MCP and A2A
  opt-in).

harness-template: v4
(hooks, guide-lint).

<!-- harness:scoped-rules:start (generated by tools/migrate_v3.py in warehouse-harness-template; do not hand-edit) -->
## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml`, `apis/openapi/**` | `.claude/rules/rest-api.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode) block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to generated files, and feed gofmt/vet findings back after each edit. Before saying "done" run `make check-fast`; the full gate is `make check-all`. `HARNESS_OFF=1` disables the hooks when debugging the harness itself.
<!-- harness:scoped-rules:end -->
