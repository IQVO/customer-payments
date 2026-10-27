#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates. A Service that selects on those two alone selects
ALL of them, which turns into a wrong answer the day a second component is added.
Every Deployment here carries `app.kubernetes.io/component` in selector.matchLabels
and its pod labels, and every Service selects on it, so each Service selects
EXACTLY ONE Deployment. This test fails if that ever stops being true.

Components: the api (always; REST, outbox relay and the three consumers in one
process), the mcp server (ADR 0005, default off; read-only, never dials Kafka)
and the frontend (ADR 0006, the nginx pod serving the customer_ordering_mfe
remote, default off; never routed by this chart). The api-only checks run with
every optional template (HPA, Ingress, HTTPRoute) enabled together; the
all-components checks additionally switch mcp and frontend on.

Run: python3 charts/customer-ordering/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "customer-ordering"

# Dummy values, no password: the chart refuses to render without a database
# source, the Kafka brokers, the delivery-promise URL and the three consumer
# groups. None is ever dialled.
GROUPS = {
    "deliveryPromiseConsumerGroup": "customer-ordering-delivery-promise",
    "orderSourcingConsumerGroup": "customer-ordering-order-sourcing",
    "shippingConsumerGroup": "customer-ordering-shipping",
}


def runtime(skip: str | None = None) -> list[str]:
    """The mandatory values, minus the one named by skip (database, kafka, deliveryPromiseUrl or a group key)."""
    args: list[str] = []
    if skip != "database":
        args += ["--set", "database.url=postgres://u@example.invalid:5432/db"]
    if skip != "kafka":
        args += ["--set", "kafka.brokers=kafka.example.invalid:9092"]
    if skip != "deliveryPromiseUrl":
        args += ["--set", "config.deliveryPromiseUrl=http://delivery-promise.example.invalid"]
    for key, value in GROUPS.items():
        if skip != key:
            args += ["--set", f"config.{key}={value}"]
    return args


BASE = runtime()
ENABLE_EVERYTHING = BASE + [
    "--set", "autoscaling.api.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    "--set", "config.deliveryPromiseTimeout=3s",
    "--set", "config.outboxRelayInterval=2s",
    "--set", "config.shutdownDrainDelay=5s",
    "--set", "database.migrationsUrl=postgres://u@example.invalid:5432/direct",
]

# The optional components of ADR 0005 (mcp) and ADR 0006 (frontend) on top of
# everything the api can render.
ENABLE_ALL_COMPONENTS = ENABLE_EVERYTHING + [
    "--set", "mcp.enabled=true",
    "--set", "mcp.route.enabled=true",
    "--set", "frontend.enabled=true",
]

# The MCP server is read-only: no Kafka, no outbox relay, no consumers, no
# delivery-promise. The frontend renders no env at all.
MCP_STRAY = {
    "KAFKA_BROKERS", "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS",
    "DELIVERY_PROMISE_URL", "DELIVERY_PROMISE_TIMEOUT",
    "DELIVERY_PROMISE_CONSUMER_GROUP", "ORDER_SOURCING_CONSUMER_GROUP", "SHIPPING_CONSUMER_GROUP",
}
MCP_ENV = ("MCP_ADDR", "LOG_LEVEL", "SERVICE_VERSION", "ENVIRONMENT", "DATABASE_URL", "MIGRATIONS_DATABASE_URL")

# Every variable cmd/api reads, with a dedicated chart value.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS",
    "DELIVERY_PROMISE_URL", "DELIVERY_PROMISE_TIMEOUT",
    "DELIVERY_PROMISE_CONSUMER_GROUP", "ORDER_SOURCING_CONSUMER_GROUP", "SHIPPING_CONSUMER_GROUP",
    "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY",
)


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in env_of(dep)]


def env_value(dep: dict, name: str) -> str | None:
    return next((e.get("value") for e in env_of(dep) if e["name"] == name), None)


def command_of(dep: dict) -> list[str] | None:
    return dep["spec"]["template"]["spec"]["containers"][0].get("command")


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    if set(deployments) != {RELEASE}:
        failures.append(f"the chart has exactly one Deployment (the api), rendered: {sorted(deployments)}")
    if set(services) != {RELEASE}:
        failures.append(f"the chart has exactly one Service (the api), rendered: {sorted(services)}")
    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # Ingress and HTTPRoute route to the api Service only; the HPA owns replicas.
    for d in docs:
        if d.get("kind") == "Ingress":
            backends = {p["backend"]["service"]["name"] for r in d["spec"]["rules"] for p in r["http"]["paths"]}
            if backends != {RELEASE}:
                failures.append(f"the Ingress must route to the api Service only, got {sorted(backends)}")
        if d.get("kind") == "HTTPRoute":
            backends = {b["name"] for r in d["spec"]["rules"] for b in r["backendRefs"]}
            if backends != {RELEASE}:
                failures.append(f"the HTTPRoute must route to the api Service only, got {sorted(backends)}")
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    api = deployments.get(RELEASE)
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    api_env = env_names(api)
    for want in API_ENV:
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    for name, want in (("DELIVERY_PROMISE_TIMEOUT", "3s"), ("OUTBOX_RELAY_INTERVAL", "2s"), ("SHUTDOWN_DRAIN_DELAY", "5s")):
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")


def check_refusals(failures: list[str]) -> None:
    cases: list[tuple[str, list[str], str]] = [
        ("without a database source", runtime("database"), "requires database.url or database.existingSecret"),
        ("without kafka.brokers", runtime("kafka"), "kafka.brokers"),
        ("without the delivery-promise URL", runtime("deliveryPromiseUrl"), "config.deliveryPromiseUrl"),
        ("a *_MODE through extraEnv",
         BASE + ["--set", "extraEnv[0].name=SHIPPING_MODE", "--set", "extraEnv[0].value=permissive"], "extraEnv sets SHIPPING_MODE"),
        ("a consumer group through extraEnv",
         BASE + ["--set", "extraEnv[0].name=SHIPPING_CONSUMER_GROUP", "--set", "extraEnv[0].value=pod"], "extraEnv sets SHIPPING_CONSUMER_GROUP"),
        ("a chart-owned variable through extraEnv",
         BASE + ["--set", "extraEnv[0].name=KAFKA_BROKERS", "--set", "extraEnv[0].value=x:9092"], "extraEnv sets KAFKA_BROKERS"),
        ("a grace period under the shutdown bounds", BASE + ["--set", "terminationGracePeriodSeconds=45"], "terminationGracePeriodSeconds"),
        ("a grace period equal to the shutdown bounds", BASE + ["--set", "terminationGracePeriodSeconds=55"], "must exceed 55"),
        ("an http address on another port than the container's", BASE + ["--set", "config.httpAddr=:9090"], "config.httpAddr"),
    ]
    for key in GROUPS:
        cases.append((f"without config.{key}", runtime(key), f"config.{key} is required"))
    for label, args, needle in cases:
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def check_all_components(docs: list[dict], failures: list[str]) -> None:
    """ADR 0005 (mcp) and ADR 0006 (frontend) rendered together with the api."""
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    want = sorted([RELEASE, f"{RELEASE}-mcp", f"{RELEASE}-frontend"])
    if sorted(deployments) != want:
        failures.append(f"expected the Deployments {want}, got {sorted(deployments)}")
    if sorted(services) != want:
        failures.append(f"expected the Services {want}, got {sorted(services)}")

    # Each Service pins its component and selects exactly one Deployment.
    for name, component in ((RELEASE, "api"), (f"{RELEASE}-mcp", "mcp"), (f"{RELEASE}-frontend", "frontend")):
        svc = services.get(name)
        if not pins(svc, component):
            failures.append(f"the {component} Service must exist and pin component={component}")
            continue
        sel = selector_of(svc or {})
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    mcp = deployments.get(f"{RELEASE}-mcp")
    if mcp is not None:
        if command_of(mcp) != ["/app/mcp"]:
            failures.append("the MCP Deployment must run /app/mcp")
        env = set(env_names(mcp))
        stray = MCP_STRAY & env
        if stray:
            failures.append(f"the MCP Deployment must not get Kafka/relay/consumer/delivery-promise env (it is read-only and never dials Kafka): {sorted(stray)}")
        for want_env in MCP_ENV:
            if want_env not in env:
                failures.append(f"the MCP Deployment does not render {want_env}")

    frontend = deployments.get(f"{RELEASE}-frontend")
    if frontend is not None:
        if env_names(frontend):
            failures.append(f"the frontend Deployment must render no env at all (it serves static bytes): {env_names(frontend)}")
    for d in docs:
        if d.get("kind") in {"Ingress", "HTTPRoute"} and "frontend" in d["metadata"]["name"]:
            failures.append(f"{d['kind']} {d['metadata']['name']}: frontend routing must not live in this chart")
        if d.get("kind") == "HTTPRoute" and d["metadata"]["name"].endswith("-mcp"):
            backends = {b["name"] for r in d["spec"]["rules"] for b in r["backendRefs"]}
            if backends != {f"{RELEASE}-mcp"}:
                failures.append(f"the mcp HTTPRoute must route to the mcp Service only, got {sorted(backends)}")


def pins(svc: dict | None, component: str) -> bool:
    return svc is not None and selector_of(svc).get("app.kubernetes.io/component") == component


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)
    check_all_components(render(ENABLE_ALL_COMPONENTS), failures)

    # Default values must not deploy an HPA or a route.
    defaults = render(BASE)
    stray = [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional templates rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    off = {"DELIVERY_PROMISE_TIMEOUT", "OUTBOX_RELAY_INTERVAL", "SHUTDOWN_DRAIN_DELAY"}
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")
    for want in ("KAFKA_BROKERS", "DELIVERY_PROMISE_URL", *(k for k in API_ENV if k.endswith("_CONSUMER_GROUP"))):
        if want not in names:
            failures.append(f"{want} is mandatory and must always be rendered")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, frontend); mcp and frontend are off by default; "
          "the MCP Deployment gets no Kafka/relay/consumer/delivery-promise env; the frontend renders no env and no route; "
          "the HPA, Ingress and HTTPRoute are off by default "
          "and route to the api only; every cmd/api env var has a dedicated value; the chart refuses to render without a database "
          "source, brokers, the delivery-promise URL or any consumer group, with a *_MODE or a consumer group in extraEnv, "
          "with a grace period that does not exceed the shutdown bounds, or with an http address off the container port")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
