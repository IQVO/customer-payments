#!/usr/bin/env bash
# Chart render assertions for customer-payments: Postgres, Kafka and the
# payment provider are the only runtime, so the mandatory configuration has no
# silent default, no *_MODE is ever rendered, the consumer group ids are stable
# chart values, the pod is hardened and has room to shut down, and the env
# cmd/api reads is the env the chart renders.
# Run: bash charts/customer-payments/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=customer-payments
# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers, the payment-provider URL and the three consumer
# groups. None is ever dialled.
groups=(checkoutConsumerGroup:customer-payments-checkout
  orderSourcingConsumerGroup:customer-payments-order-sourcing
  shippingAndTrackingConsumerGroup:customer-payments-shipping-and-tracking)
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092
  --set config.paymentProviderUrl=http://payment-provider.example.invalid)
for g in "${groups[@]}"; do base+=(--set "config.${g%%:*}=${g#*:}"); done
# Every optional value set, so every env var cmd/api can read is rendered.
everything=(--set config.outboxRelayInterval=2s --set config.policyInterval=3s
  --set config.shutdownDrainDelay=5s --set database.migrationsUrl=postgres://u@example.invalid:5432/direct
  --set autoscaling.api.enabled=true --set ingress.enabled=true --set gatewayApi.enabled=true)

fail() { echo "FAIL: $*" >&2; exit 1; }
command -v helm >/dev/null || fail "helm is not installed"

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# Every non-secret config value is rendered into the chart ConfigMap, and the api
# Deployment must mount it with envFrom, or a value that is only in the
# ConfigMap silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' \
  || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" \
  || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"
grep -q 'checksum/config:' <<<"$api_deployment" || fail "the pods must roll when the ConfigMap changes (checksum/config)"

# A tuned config.* value reaches the ConfigMap the api mounts and the container env.
tuned="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 \
  --set config.policyInterval=9s --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_RELAY_INTERVAL: "7s"' <<<"$tuned" || fail "config.outboxRelayInterval is not rendered into the ConfigMap"
grep -q 'OUTBOX_MAX_ATTEMPTS: "5"' <<<"$tuned" || fail "config.outboxMaxAttempts is not rendered into the ConfigMap"
grep -q 'POLICY_INTERVAL: "9s"' <<<"$tuned" || fail "config.policyInterval is not rendered into the ConfigMap"

# The relay attempt cap defaults to 20 and 0 (unlimited) is rendered as is.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20"
grep -A1 'name: OUTBOX_MAX_ATTEMPTS' <<<"$api_deployment" | grep -q 'value: "20"' \
  || fail "api Deployment does not set OUTBOX_MAX_ATTEMPTS"
unlimited="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"
zero_drain="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.shutdownDrainDelay=0 --show-only templates/configmap.yaml)"
grep -q 'SHUTDOWN_DRAIN_DELAY: "0"' <<<"$zero_drain" || fail "config.shutdownDrainDelay=0 is not rendered"

# Chart vs binary: every env var cmd/api reads must be rendered into the
# Deployment when every optional value is set. The names come from the binary's
# own os.Getenv / getenv("...") / env("...") literals (including the telemetry
# adapter, which reads ENVIRONMENT) and from the consumer spec table's
# *_CONSUMER_GROUP literals (read through a variable), so a new variable in
# cmd/api fails this test until the chart renders it.
binary_env() {
  {
    grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$repo_dir"/cmd/api/*.go \
      "$repo_dir"/internal/adapters/outbound/telemetry/telemetry.go --exclude='*_test.go' | sed -E 's/.*\("//; s/"$//'
    grep -ohE '"[A-Z][A-Z0-9_]*_CONSUMER_GROUP"' "$repo_dir"/cmd/api/*.go --exclude='*_test.go' | tr -d '"'
  } | sort -u
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
api_vars=($(binary_env))
(( ${#api_vars[@]} >= 14 )) || fail "found only ${#api_vars[@]} env names in cmd/api; the scan is looking in the wrong place"
full_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/deployment.yaml)"
for var in "${api_vars[@]}" DATABASE_URL MIGRATIONS_DATABASE_URL KAFKA_BROKERS PAYMENT_PROVIDER_URL \
  CHECKOUT_CONSUMER_GROUP ORDER_SOURCING_CONSUMER_GROUP SHIPPING_AND_TRACKING_CONSUMER_GROUP HTTP_ADDR \
  OUTBOX_RELAY_INTERVAL POLICY_INTERVAL OUTBOX_MAX_ATTEMPTS SHUTDOWN_DRAIN_DELAY LOG_LEVEL \
  SERVICE_VERSION ENVIRONMENT OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT; do
  grep -q "name: $var\$" <<<"$full_deployment" || fail "the api Deployment does not render $var, which cmd/api reads"
done

# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys), and
# the ConfigMap never carries a DSN.
full_configmap="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/configmap.yaml)"
api_known=" ${api_vars[*]} "
while read -r key; do
  [[ "$api_known" == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$full_configmap")
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$full_configmap"; then fail "a DSN must never land in the ConfigMap"; fi

# The mandatory configuration is rendered from values.
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
[[ "$(env_value "$api_deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
[[ "$(env_value "$api_deployment" PAYMENT_PROVIDER_URL)" == "http://payment-provider.example.invalid" ]] \
  || fail "PAYMENT_PROVIDER_URL is not rendered from config.paymentProviderUrl"
grep -q '^  KAFKA_BROKERS: "kafka.example.invalid:9092"' <<<"$configmap" || fail "the ConfigMap does not carry KAFKA_BROKERS"
grep -q '^  PAYMENT_PROVIDER_URL: "http://payment-provider.example.invalid"' <<<"$configmap" || fail "the ConfigMap does not carry PAYMENT_PROVIDER_URL"
for kv in 'CHECKOUT_CONSUMER_GROUP=customer-payments-checkout' 'ORDER_SOURCING_CONSUMER_GROUP=customer-payments-order-sourcing' \
  'SHIPPING_AND_TRACKING_CONSUMER_GROUP=customer-payments-shipping-and-tracking'; do
  [[ "$(env_value "$api_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv"
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$configmap" || fail "the ConfigMap does not carry $kv"
done
# The group ids are stable: they do not change with the release name and are
# never derived from the pod name or the release.
other="$(helm template another-release "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
for kv in 'CHECKOUT_CONSUMER_GROUP=customer-payments-checkout' 'ORDER_SOURCING_CONSUMER_GROUP=customer-payments-order-sourcing' \
  'SHIPPING_AND_TRACKING_CONSUMER_GROUP=customer-payments-shipping-and-tracking'; do
  [[ "$(env_value "$other" "${kv%%=*}")" == "${kv#*=}" ]] || fail "${kv%%=*} must not depend on the release name"
done
if grep -E 'name: [A-Z_]*CONSUMER_GROUP$' -A3 <<<"$api_deployment" | grep -qE 'fieldRef|metadata.name|HOSTNAME'; then
  fail "a consumer group id must never come from the pod name"
fi

# DATABASE_URL comes from a Secret; MIGRATIONS_DATABASE_URL is optional.
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "MIGRATIONS_DATABASE_URL must come from a Secret (secretKeyRef)"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'optional: true' || fail "MIGRATIONS_DATABASE_URL must be optional: true"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'key: MIGRATIONS_DATABASE_URL' \
  || fail "MIGRATIONS_DATABASE_URL must read database.migrationsExistingSecretKey"
existing="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db --set kafka.brokers=k:9092 \
  --set config.paymentProviderUrl=http://pp "${base[@]:6}" --show-only templates/deployment.yaml)"
grep -A4 'name: DATABASE_URL$' <<<"$existing" | grep -q 'name: my-db' || fail "database.existingSecret must be the DATABASE_URL Secret"
no_secret="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db --set kafka.brokers=k:9092 \
  --set config.paymentProviderUrl=http://pp "${base[@]:6}")"
if grep -q 'kind: Secret' <<<"$no_secret"; then fail "no Secret may be created when database.existingSecret is set"; fi
helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" | grep -q 'kind: Secret' || fail "database.url must create the Secret"

# Pod hardening, probes, the image tag and shutdown room.
for want in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault' 'path: /healthz' 'path: /readyz'; do
  grep -q -- "$want" <<<"$api_deployment" || fail "the api Deployment lacks '$want'"
done
grep -A2 'drop:' <<<"$api_deployment" | grep -q 'ALL' || fail "the api container must drop ALL capabilities"
grep -A3 'livenessProbe:' <<<"$api_deployment" | grep -q '/healthz' || fail "livenessProbe must be /healthz"
grep -A3 'readinessProbe:' <<<"$api_deployment" | grep -q '/readyz' || fail "readinessProbe must be /readyz"
grep -q 'image: "warehouse/customer-payments:v0.1.0"' <<<"$api_deployment" \
  || fail "an empty image.tag must resolve to v<appVersion>, the tag the release job publishes"
grace="$(awk '/terminationGracePeriodSeconds:/ {print $2; exit}' <<<"$api_deployment")"
# SHUTDOWN_DRAIN_DELAY 5s + HTTP 10s + 4 workers x 10s (three consumers + the
# policy loop) + relay 10s = 65s.
(( grace > 65 )) || fail "terminationGracePeriodSeconds is $grace; it must exceed the binary's shutdown bounds (65s)"

# No *_MODE or in-memory/permissive wording anywhere in the render.
render_all="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}")"
if grep -qE '(^|[^A-Z_])[A-Z]+_MODE\b' <<<"$render_all"; then fail "the render carries a *_MODE variable, which this service does not have"; fi
if grep -qiE 'in-memory|silently runs|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then
  fail "the render or NOTES.txt mentions an in-memory / permissive mode"
fi

# Only the api component exists by default: one Deployment, one Service; no
# frontend, projector or reports component ever renders from this chart.
for stray in frontend projector reports analytics; do
  if grep -qE "app.kubernetes.io/component: .*$stray" <<<"$render_all"; then fail "the chart must not render a $stray component"; fi
done
[[ "$(grep -c '^kind: Deployment' <<<"$render_all")" == 1 ]] || fail "exactly one Deployment (the api) must render without mcp.enabled"
[[ "$(grep -c '^kind: Service$' <<<"$render_all")" == 1 ]] || fail "exactly one Service (the api) must render without mcp.enabled"
for off in HorizontalPodAutoscaler Ingress HTTPRoute; do
  if grep -q "^kind: $off" <<<"$(helm template "$release" "$chart_dir" "${base[@]}")"; then fail "$off must be off by default"; fi
  grep -q "^kind: $off" <<<"$render_all" || fail "$off must render when enabled"
done

# --- observability (ADR 0004, fleet standard) --------------------------------
# The api renders the fleet pipeline's env: the Collector endpoint, the service
# name, the version (image tag, appVersion when empty) and the environment.
otel_endpoint="otel-collector.observability.svc.cluster.local:4317"
[[ "$(env_value "$api_deployment" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] \
  || fail "the api Deployment does not render OTEL_EXPORTER_OTLP_ENDPOINT from otel.endpoint"
[[ "$(env_value "$api_deployment" OTEL_SERVICE_NAME)" == "customer-payments" ]] \
  || fail "the api Deployment does not render OTEL_SERVICE_NAME=customer-payments from otel.serviceName"
[[ "$(env_value "$api_deployment" SERVICE_VERSION)" == "0.1.0" ]] \
  || fail "SERVICE_VERSION must default to the chart appVersion"
[[ "$(env_value "$api_deployment" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from environment"
tagged="$(helm template "$release" "$chart_dir" "${base[@]}" --set image.tag=v9.9.9 --set otel.serviceName=payments --set otel.endpoint=http://collector:4317 \
  --set environment=staging --show-only templates/deployment.yaml)"
[[ "$(env_value "$tagged" SERVICE_VERSION)" == "v9.9.9" ]] || fail "SERVICE_VERSION must follow image.tag"
[[ "$(env_value "$tagged" OTEL_SERVICE_NAME)" == "payments" ]] || fail "OTEL_SERVICE_NAME must follow otel.serviceName"
[[ "$(env_value "$tagged" OTEL_EXPORTER_OTLP_ENDPOINT)" == "http://collector:4317" ]] || fail "OTEL_EXPORTER_OTLP_ENDPOINT must follow otel.endpoint"
[[ "$(env_value "$tagged" ENVIRONMENT)" == "staging" ]] || fail "ENVIRONMENT must follow environment"
# otel.enabled=false renders no OTEL_* env (SERVICE_VERSION stays: it also labels the logs' build).
otel_off="$(helm template "$release" "$chart_dir" "${base[@]}" --set otel.enabled=false --set mcp.enabled=true)"
if grep -q 'name: OTEL_' <<<"$otel_off"; then fail "otel.enabled=false must render no OTEL_* env in any component"; fi
grep -q 'name: SERVICE_VERSION$' <<<"$otel_off" || fail "SERVICE_VERSION must still render with otel.enabled=false"
# The values are chart-owned: extraEnv may not set them a second time.
for owned in OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT; do
  err="$(helm template "$release" "$chart_dir" "${base[@]}" --set "extraEnv[0].name=$owned" --set 'extraEnv[0].value=x' 2>&1)" \
    && fail "extraEnv must not be able to set $owned"
  grep -qF "extraEnv sets $owned" <<<"$err" || fail "the refusal for extraEnv $owned does not name it: $err"
done

# --- refusals: the mandatory values name themselves ---------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':
$err"
}
# args that satisfy everything except the one value a case removes.
mandatory_except() { # mandatory_except <database|kafka|paymentProviderUrl|<group key>>
  local skip="$1" out=()
  [[ "$skip" == database ]] || out+=(--set database.url=postgres://u@h/db)
  [[ "$skip" == kafka ]] || out+=(--set kafka.brokers=k:9092)
  [[ "$skip" == paymentProviderUrl ]] || out+=(--set config.paymentProviderUrl=http://pp)
  for g in "${groups[@]}"; do
    [[ "${g%%:*}" == "$skip" ]] || out+=(--set "config.${g%%:*}=${g#*:}")
  done
  printf '%s\n' "${out[@]}"
}
refusal_without() { # refusal_without <value to leave out> <needle>
  local args=()
  while IFS= read -r line; do args+=("$line"); done < <(mandatory_except "$1")
  expect_refusal "no $1" "$2" "${args[@]}"
}
refusal_without database "database.url or database.existingSecret"
refusal_without kafka "kafka.brokers"
refusal_without paymentProviderUrl "config.paymentProviderUrl"
for g in "${groups[@]}"; do refusal_without "${g%%:*}" "config.${g%%:*}"; done
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=STORAGE_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "a *_MODE through extraEnv (off)" "_MODE" "${base[@]}" --set 'extraEnv[0].name=KAFKA_MODE' --set 'extraEnv[0].value=off'
expect_refusal "a consumer group through extraEnv" "CHECKOUT_CONSUMER_GROUP" "${base[@]}" --set 'extraEnv[0].name=CHECKOUT_CONSUMER_GROUP' --set 'extraEnv[0].value=x'
expect_refusal "a chart-owned variable through extraEnv" "DATABASE_URL" "${base[@]}" --set 'extraEnv[0].name=DATABASE_URL' --set 'extraEnv[0].value=x'
expect_refusal "a grace period under the shutdown bounds" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=55
expect_refusal "a grace period equal to the shutdown bounds" "must exceed 65" "${base[@]}" --set terminationGracePeriodSeconds=65
expect_refusal "no grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=0
helm template "$release" "$chart_dir" "${base[@]}" --set terminationGracePeriodSeconds=66 >/dev/null \
  || fail "a 66s grace period (just above the 65s bounds) must still render"
# A longer drain delay raises the bar by the same amount.
expect_refusal "a 30s drain delay with the default grace period" "must exceed 90" "${base[@]}" --set config.shutdownDrainDelay=30s
helm template "$release" "$chart_dir" "${base[@]}" --set config.shutdownDrainDelay=30s --set terminationGracePeriodSeconds=91 >/dev/null \
  || fail "a 30s drain delay with a 91s grace period must render"
expect_refusal "a drain delay the chart cannot size" "config.shutdownDrainDelay" "${base[@]}" --set config.shutdownDrainDelay=soon
expect_refusal "an http address off the container port" "config.httpAddr" "${base[@]}" --set config.httpAddr=:9090

# --- the optional mcp component (default off) --------------------------------

# binary_env_of <file...>: the env literals one binary reads.
binary_env_of() {
  local f
  for f in "$@"; do
    grep -hoE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$f" || true
  done | sed -E 's/.*"([A-Z0-9_]+)".*/\1/' | sort -u
}
# assert_env_rendered <rendered> <label> <vars...>
assert_env_rendered() {
  local rendered="$1" label="$2"; shift 2
  local var
  for var in "$@"; do
    grep -q "name: $var\$" <<<"$rendered" || fail "$label does not render $var, which its binary reads"
  done
}

# mcp: its own env, never the api ConfigMap, never Kafka (read-only).
mcp_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-deployment.yaml)"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_dep" || fail "the mcp Deployment was not rendered with mcp.enabled=true"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_dep" || fail "the mcp Deployment must run /app/mcp"
if grep -q 'configMapRef:' <<<"$mcp_dep"; then fail "mcp-deployment.yaml must not mount the api ConfigMap"; fi
if grep -Eq 'name: (KAFKA_BROKERS|PAYMENT_PROVIDER_[A-Z_]+|ORDER_SOURCING_[A-Z_]+|SHIPPING_AND_TRACKING_[A-Z_]+|CHECKOUT_[A-Z_]+|OUTBOX_[A-Z_]+|POLICY_INTERVAL|[A-Z]+_CONSUMER_GROUP)$' <<<"$mcp_dep"; then
  fail "the mcp Deployment must not get Kafka/relay/consumer/provider env (read-only, never dials Kafka)"
fi
# shellcheck disable=SC2207
mcp_vars=($(binary_env_of "$repo_dir"/cmd/mcp/main.go "$repo_dir"/cmd/mcp/telemetry.go))
(( ${#mcp_vars[@]} >= 5 )) || fail "found only ${#mcp_vars[@]} env literals in cmd/mcp; the scan is looking in the wrong place"
assert_env_rendered "$mcp_dep" "the mcp Deployment" "${mcp_vars[@]}" DATABASE_URL MIGRATIONS_DATABASE_URL MCP_ADDR LOG_LEVEL SERVICE_VERSION ENVIRONMENT \
  OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT
mcp_versioned="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set mcp.serviceVersion=dev --show-only templates/mcp-deployment.yaml)"
[[ "$(env_value "$mcp_versioned" SERVICE_VERSION)" == "dev" ]] || fail "SERVICE_VERSION must come from mcp.serviceVersion"
[[ "$(env_value "$mcp_dep" SERVICE_VERSION)" == "0.1.0" ]] || fail "the mcp SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$mcp_dep" OTEL_SERVICE_NAME)" == "customer-payments-mcp" ]] || fail "the mcp component must export as <otel.serviceName>-mcp"
[[ "$(env_value "$mcp_dep" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] || fail "the mcp component must export to otel.endpoint"
mcp_named="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set otel.serviceName=payments --show-only templates/mcp-deployment.yaml)"
[[ "$(env_value "$mcp_named" OTEL_SERVICE_NAME)" == "payments-mcp" ]] || fail "the mcp service name must follow otel.serviceName"
[[ "$(env_value "$mcp_dep" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from mcp.environment"
mcp_svc="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-service.yaml)"
grep -q 'kind: Service' <<<"$mcp_svc" || fail "the mcp Service was not rendered with mcp.enabled=true"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_svc" || fail "the mcp Service must select component=mcp"

# The mcp route is a SEPARATE switch: mcp on alone exposes nothing.
mcp_noroute="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true)"
if grep -q 'kind: HTTPRoute' <<<"$mcp_noroute"; then fail "the mcp HTTPRoute must not render while mcp.route.enabled is false"; fi
mcp_route="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set mcp.route.enabled=true --show-only templates/mcp-route.yaml)"
grep -q 'value: /api/mcp/customer-payments' <<<"$mcp_route" || fail "the mcp route must carry its configured host path"
grep -q 'replacePrefixMatch: /mcp' <<<"$mcp_route" || fail "the mcp route must rewrite the prefix to /mcp (the SDK's canonical endpoint)"
grep -q "name: $release-mcp" <<<"$mcp_route" || fail "the mcp route must target the mcp Service"

# ALL optional components render TOGETHER (ct lint's values file).
together="$(helm template "$release" "$chart_dir" -f "$chart_dir/ci/all-components-values.yaml")"
for component in api mcp; do
  grep -q "app.kubernetes.io/component: $component\$" <<<"$together" || fail "the full render lacks the $component component"
done
[[ "$(grep -c '^kind: Deployment' <<<"$together")" == 2 ]] || fail "the full render must carry the api and mcp Deployments"
[[ "$(grep -c '^kind: Service$' <<<"$together")" == 2 ]] || fail "the full render must carry the api and mcp Services"

echo "PASS: customer-payments chart render assertions"
