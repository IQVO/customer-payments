---
id: before-first-release
title: Before the first release
sidebar_label: Before the first release
sidebar_position: 5
---

# Before the first release: customer-payments

## Status in this repository

Checked against the code on this branch:

| # | Item | State |
| --- | --- | --- |
| 1 | Real runtime only | Done. `cmd/**` imports no memory adapter and `cmd/api` refuses to boot without `DATABASE_URL`, `KAFKA_BROKERS`, `PAYMENT_PROVIDER_URL` and the three consumer group ids (see [Configuration](/docs/operations/configuration)). Guarded by `TestRuntimeBinaryHasNoInMemoryAdapters`. |
| 2 | No Go file at the repo root | Done; godog lives in `internal/bdd/` with the scenarios in `features/`. Guarded by `TestNoGoFilesAtRepoRoot`. |
| 3 | `Dockerfile` builds every `cmd/*` | Done: a loop over `cmd/*/` builds `api`, `mcp` and `a2a` into `/app`; `ENTRYPOINT ["./api"]`; the chart overrides `command` for the other two. `EXPOSE 8080 8093` omits the MCP port 8090 (cosmetic: Kubernetes ignores `EXPOSE`). |
| 4 | `.dockerignore` | Present. |
| 5 | Helm chart | Done: `charts/customer-payments` with `Chart.yaml` name `customer-payments`; three components (api, optional mcp, optional a2a). |
| 6 | DSN from a Secret | Done (`database.existingSecret` or `database.url` stored in a chart-created Secret). |
| 7 | Hardening and shutdown room | Done: non-root uid 1000, read-only root filesystem, capabilities dropped; `terminationGracePeriodSeconds` 70 and refused when too small. |
| 8 | Chart tests | Done: `charts/customer-payments/tests/render_test.sh` and `test_service_selectors.py`, run by the `chart-tests` job. |
| 9 | Consumer group ids are chart values | Done: `config.checkoutConsumerGroup`, `config.orderSourcingConsumerGroup`, `config.shippingAndTrackingConsumerGroup`, all mandatory. |
| 10 | Optional CI jobs match the service | `ci.yml` has no `web` and no `docs-api-drift` job (the service has no `web/`); `api-lint` lints `apis/openapi.yaml` and `apis/asyncapi.yaml`. The documentation site has its own workflow, `docs.yml`. |
| 11 | Registry access | Images push to `ghcr.io/iqvo` with the Actions token. |
| 12 | `govulncheck` clean | Checked by the `vuln` job on every push; `make vuln` runs the same command. |

The original checklist follows unchanged.

## The checklist

The first merge to `main` runs `docker-publish` and `release`. Two services built from
this template (pick-face-replenishment, task-interleaving) each needed a rework because
packaging was missing or wrong when that moment came. Work through this list on a feature
branch, **before** the first `develop` → `main` PR. Delete a row only when the service
really does not have that part, and say so in the PR.

`new-service.sh` already generated `Dockerfile` and `.dockerignore` for you (verbatim from
the fleet reference). The chart is NOT generated: it is shaped by what the service runs
(consumer groups, MCP, analytics, frontend), so copy the closest sibling's
`charts/<repo>/` and adapt it. References on `origin/develop`:
`IQVO/pick-face-replenishment` (Kafka consumers, MCP, analytics, frontend) and
`IQVO/task-interleaving`.

| # | Item | Done when | Guarded by |
|---|------|-----------|------------|
| 1 | **Real runtime only.** `cmd/api` wires Postgres and Kafka; the in-memory adapters (`internal/adapters/outbound/memory`) are test doubles. No `*_MODE`/permissive fallback, no default that boots "healthy" without `DATABASE_URL` or `KAFKA_BROKERS`. | `cmd/**` has no import of the memory package; the binary exits at boot on missing `DATABASE_URL`, `KAFKA_BROKERS` or a consumer group id. | `TestRuntimeBinaryHasNoInMemoryAdapters` (`internal/architecture/runtime_no_memory_fitness_test.go`). The boot-error behaviour has no generic sensor: cover it in `cmd/api` tests and in the chart test (row 8). |
| 2 | **No Go file at the repo root.** The godog runner and steps live in `features/` (`package features_test`, `Paths: "."`), binaries in `cmd/<name>/`, everything else in `internal/`. | `ls *.go` at the root prints nothing; `make bdd` still finds `TestFeatures`. | `TestNoGoFilesAtRepoRoot` (`internal/architecture/no_go_files_at_root_fitness_test.go`). |
| 3 | **`Dockerfile` builds every `cmd/*`.** Generated; confirm `ENTRYPOINT ["./api"]` matches your main binary and `EXPOSE` matches `HTTP_ADDR`. | `docker build .` succeeds locally; `docker run --rm <image> /app/api` starts (or exits with the intended config error). | `trivy-scan` job builds the image on every PR to `main`/`develop`; `docker-publish` on push to `main`. |
| 4 | **`.dockerignore`** excludes docs, charts, harness, tests output. Generated. | Build context is small; `.git`, `.claude/`, `docs/`, `charts/` are not sent. | none: review once. |
| 5 | **Helm chart `charts/customer-payments/`** with `Chart.yaml` `name: customer-payments`. The chart directory name, the `Chart.yaml` name and the `helm package` file name in the `release` job must all be `customer-payments`. | `helm lint charts/customer-payments` and `helm template` with the required values both pass. | `helm-lint` (PRs) and the `release` job (packages `customer-payments-<version>.tgz`). |
| 6 | **`DATABASE_URL` comes from a Secret** (`secretKeyRef`, chart `secret.yaml` or `database.existingSecret`), never from the ConfigMap or a literal. | `helm template` shows `secretKeyRef` for `DATABASE_URL`; the ConfigMap has no DSN. | chart test (row 8). |
| 7 | **Pod hardening and shutdown room.** `runAsNonRoot`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`; `terminationGracePeriodSeconds` greater than the sum of the binary's shutdown bounds (drain delay + HTTP + consumers + relay). | Values and Deployment carry all of it; the chart refuses a grace period below the sum. | chart test (row 8). |
| 8 | **Chart tests in `charts/customer-payments/tests/`** (`render_test.sh`, `test_service_selectors.py`): envFrom ConfigMap mounted, mandatory values refuse to render, every env var `cmd/api` reads is rendered, no `*_MODE`, Secret for the DSN, hardening, grace period, every Service selects exactly one Deployment. Copy and adapt the reference tests. | `bash charts/customer-payments/tests/render_test.sh` prints `PASS`. | `chart-tests` job runs every `tests/*.sh` and `test_*.py`. **It only warns when the directory is empty**, so an absent test passes CI: the checklist is the guard. |
| 9 | **Consumer group ids are chart values**, stable and service-prefixed, never derived from the pod name. | Each `*_CONSUMER_GROUP` is rendered from `config.*` and refuses to render empty. | chart test (row 8); `kafka-testing-and-consumers.md` rule. |
| 10 | **Optional CI jobs match the service.** `web` (needs `web/`), `docs-api-drift` (needs `docs/` and `apis/openapi.yaml`), `api-lint` (needs `apis/*.yaml` and the spectral rulesets): delete a job and its `docker-publish` `needs:` entry if the service has no such part, or add the part. | No job in `ci.yml` is permanently red or skipped for a missing directory. | first PR's CI run. |
| 11 | **Registry access** images push to `ghcr.io/iqvo` with the Actions token (`packages: write` on `docker-publish`/`release`, already in `ci.yml`); no Docker Hub secrets are needed. | A dry-run of `docker-publish` on a throwaway branch, or ask the repo admin. | none. |
| 12 | **`govulncheck` is clean** before the PR to `main` (CI `vuln` job installs `@latest`; `make vuln` runs the same command). | `make vuln` exits 0. | `vuln` job (a `docker-publish` prerequisite). |

Tick this list in the PR that adds `Dockerfile`/chart wiring, and keep the file: it is the
service's packaging record. Reference for the three failures this list prevents: a runtime that
can run without Postgres/Kafka (row 1), Go files at the root (row 2), a release that fails on
the first push to `main` because packaging was never built (rows 3-8).
