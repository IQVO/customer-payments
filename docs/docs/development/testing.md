---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

The test pyramid as it is on this branch, the `make` targets that run each
layer, and the CI jobs that enforce them. Counts were taken from the source
tree (`func Test...` per file, `Scenario:` per feature file).

## The pyramid

| Layer | What it covers | Where | Count | Run with |
| --- | --- | --- | --- | --- |
| Unit: domain | `PaymentAuthorization` and `Refund` aggregates, shared money / id rules | `internal/domain/*/` | 40 tests (23 `paymentauthorization`, 10 `refund`, 7 `shared`) | `make test` |
| Unit: use cases | every command, query, consumer and policy over in-memory doubles | `internal/application/usecases/` | 36 tests | `make test` |
| Unit: adapters | HTTP handlers and routes (httptest over the real chi router), Kafka consumer loop / dead-letter / tracing, CloudEvents codec, outbound encoder and writer, payment provider client (httptest), telemetry, MCP and A2A adapters, boot configuration of each `cmd/*` | `internal/adapters/**`, `cmd/*/*_test.go`, `internal/bootretry` | the rest of the 307 (about 200 outside the fitness tests and the godog runner) | `make test` |
| Behaviour (godog / Gherkin) | acceptance scenarios through the real router and use cases, in-memory doubles | `features/*.feature`, runner `internal/bdd/features_test.go` (`TestFeatures`, `Strict: true`) | 3 feature files, 16 scenarios (`authorization` 5, `capture_release` 6, `refunds` 5) | `make bdd` |
| Contract: repositories | one contract suite every repository implementation must pass, run against the memory doubles and against real Postgres | `internal/adapters/outbound/repocontract/` (driven by `memory_test.go` and `repositories_integration_test.go`) | shared suite | `make test`, `make integration` |
| Contract: wire formats | golden CloudEvents per event type (`internal/adapters/outbound/kafka/testdata/*.golden.json`), MCP tool registry golden (`internal/adapters/inbound/mcp/testdata/tool_registry.golden.json`), the event catalogue checked against `apis/asyncapi.yaml` (`TestEventCatalogueMatchesContract`), OpenAPI and AsyncAPI linted with spectral in CI (`api-lint`) | as listed | 10 event goldens, 1 tool golden | `make test`, `make arch-test`; spectral in CI only |
| Integration | real Postgres and real Kafka started by testcontainers: repositories, migrations, outbox relay, outbox trace headers, the `cmd/api` end to end, `/readyz` wiring, the `cmd/mcp` binary | files tagged `//go:build integration` (9 files) | 13 tests | `make integration` |
| Mutation | gremlins over every package under `internal/domain` | `.gremlins.yaml` | thresholds: efficacy 85, mutant coverage 85 (measured 2026-10-26: `paymentauthorization` 85.94 / 85.33, `refund` 86.05 / 100, `shared` 100 / 100) | `make mutation` (blocking subset), `make mutation-full` |
| Architecture fitness | structure and policy as executable tests | `internal/architecture/` | 32 tests | `make arch-test` |

Totals: 307 unit-tagged and 13 integration-tagged test functions plus the 16
godog scenarios. Integration tests never read `DATABASE_URL` or `KAFKA_BROKERS`
and never `t.Skip` on them: each boots its own containers
(`TestPostgresIntegrationTestsUseTestcontainers`,
`TestKafkaIntegrationTestsUseTestcontainers`), so they need only Docker.

## Fitness tests (`internal/architecture/`)

| Test | Enforces |
| --- | --- |
| `TestHexagonalArchitecture` | the domain imports no outer layer; adapters do not reach sideways into siblings |
| `TestMCPAdapterDependencyRule`, `TestMCPOptionalToolInputsCarryOmitempty` | the MCP adapter stays read-only and its optional inputs are `omitempty` |
| `TestA2AAdapterDependencyRule`, `TestA2AServerIsReadOnlyAndHasNoOutboundClient`, `TestA2ASDKIsImportedByTheAdapterAndItsCommandOnly` | the A2A agent is read-only, holds no outbound client, and the SDK is confined |
| `TestNoAuthMiddlewareReintroduced` | no bearer / JWT check under `internal/adapters/inbound` (auth was removed fleet-wide) |
| `TestRuntimeBinaryHasNoInMemoryAdapters` | no `cmd/**` imports `internal/adapters/outbound/memory` |
| `TestNoGoFilesAtRepoRoot` | Go files live under `cmd/`, `internal/` |
| `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope` | CloudEvents 1.0 is the only envelope; no toggle, no flat envelope |
| `TestEventCatalogueMatchesContract` | the encoder's event types match `apis/asyncapi.yaml` |
| `TestReplayConsumersSetCommitInterval`, `TestKafkaConsumerGroupNeverHardcodedInline` | consumer commit and group-id rules |
| `TestKafkaWritersSetRequiredAcks`, `TestKafkaWriterTopicNotSetTwice` | writers use `RequiredAcks: All` and never set the topic on both writer and message |
| `TestDeadLetterCallsGuardCancellation` | dead-letter publishes respect context cancellation (shutdown) |
| `TestOutboxRelaysPassAttemptCap` | every relay is built with the attempt cap |
| `TestPostgresIntegrationTestsUseTestcontainers`, `TestKafkaIntegrationTestsUseTestcontainers` | integration tests start their own containers |

Most carry a companion `...SensorBites` / `...FailsOnBadFixtures` test proving
the sensor fails on a bad fixture.

## `make` targets

| Target | Runs | In `check` / `check-all`? |
| --- | --- | --- |
| `make build` | `go build ./...` | check |
| `make vet` | `go vet ./...` | check |
| `make fmt` / `make fmt-check` | `gofmt -w .` / fail if `gofmt -l .` lists files | `fmt-check` in check |
| `make lint` | `golangci-lint run --allow-parallel-runners ./...` (v2.14.0; config `.golangci.yml`, complexity gates) | check |
| `make test` | `go test ./... -race` (unit + httptest + godog) | check |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...`, then fails below 90 % | check-all |
| `make arch-test` | `go test ./internal/architecture/... -v` | check-all |
| `make bdd` | `go test ./... -run TestFeatures -v` | check-all |
| `make integration` | `go test -tags=integration ./... -race -count=1` (Docker needed) | neither (slow) |
| `make mutation` / `make mutation-full` | gremlins per `internal/domain/*/` package (`-full` uses `--workers 1 --timeout-coefficient 30`) | neither |
| `make vuln` | `govulncheck ./...` | neither |
| `make check` | `fmt-check vet build lint test` | the pre-push gate (lefthook) |
| `make check-all` | `check coverage arch-test bdd` | run before pushing |
| `make check-fast` | `fmt-check vet arch-test` plus the tests of packages changed vs HEAD | the agent Stop hook |
| `make guide-lint`, `make harness-test` | agent-guide lint and the unit tests of `scripts/harness/hook.py` | CI `guide-lint` |

lefthook: pre-commit runs `fmt-check`, `vet`, `lint`; pre-push runs
`make check`. `integration` and `mutation` are deliberately outside the hooks.

## CI (`.github/workflows/ci.yml`)

Triggers: push and pull request on `main` and `develop`, a weekly schedule
(Monday 06:00 UTC, for the exhaustive mutation job only) and manual dispatch.

| Job | What it runs |
| --- | --- |
| `lint` | golangci-lint v2.14.0 |
| `guide-lint` | `scripts/harness/guide_lint.py` and `test_hook.py` (advisory: `continue-on-error`) |
| `complexity` | golangci-lint restricted to `gocyclo, gocognit, cyclop, funlen, nestif`, plus an informational gocyclo report |
| `test` | `go build`, `go vet`, then `make coverage` (90 % gate), coverage artifact |
| `bdd` | `go test ./... -run TestFeatures -v` |
| `integration` | `go test -tags=integration ./... -race -count=1` (testcontainers on the runner) |
| `mutation-fast` | gremlins on every `./internal/domain/*/` package, blocking |
| `mutation` | the same with `--workers 1 --timeout-coefficient 30`, on schedule or dispatch only; opens or closes a `harness:red` issue |
| `api-lint` | spectral on `apis/openapi.yaml` and `apis/asyncapi.yaml` (`--fail-severity=warn`) |
| `vuln` | `govulncheck ./...` |
| `helm-lint` | pull requests only: `ct lint` on `charts/customer-payments` with both `ci/*-values.yaml`, then the Service-selector test |
| `chart-tests` | every `charts/customer-payments/tests/*.sh` and `test_*.py` on every push |
| `arch-test` | `go test ./internal/architecture/... -v` |
| `docker-publish` | needs `lint, complexity, test, bdd, integration, mutation-fast, vuln, api-lint, arch-test`; builds and pushes the image |
| `release` | needs `docker-publish`; tags and packages the chart |

The documentation site is built by its own workflow, `docs.yml` (build on pull
request, deploy on push to `main`).

## Running one layer

```sh
go test ./internal/domain/... -race          # domain only
go test ./internal/application/usecases/ -run TestAuthorize -v
make bdd                                     # the 16 scenarios
make integration                             # Docker required
go test ./cmd/api -count=1                   # boot-configuration tests, no containers
```

When adding behaviour, write the failing godog scenario or domain test first;
a new event type also needs its golden file, its entry in `apis/asyncapi.yaml`
and the catalogue test to agree.
