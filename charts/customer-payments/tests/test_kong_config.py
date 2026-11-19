#!/usr/bin/env python3
"""Assert this chart's Kong configuration contract (fleet decision 2026-10-13).

ALL Kong configuration -- routes AND plugins -- belongs to the application's Helm chart, not warehouse-infra
Terraform. Chart-rendered objects are Helm/ArgoCD reconciled forever; the old Terraform-side objects vanished on a
cluster rebuild. This test is the sensor for that decision. It fails if:
  * the default values (ci/default-values.yaml) render any Kong object or HTTPRoute (standalone installs must be unaffected),
  * any enabled HTTPRoute renders WITHOUT the konghq.com/plugins annotation,
  * the annotation names KongPlugins the chart does not render,
  * any auth/authorization plugin is configured (forbidden fleet-wide),
  * the MCP route does not rewrite its prefix to /mcp or the reports route to /reports,
  * the CORS plugin does not allow Idempotency-Key and If-Match, does not expose ETag, Idempotency-Replayed and
    Mcp-Session-Id, or allows origin "*".

The enabled render uses ci/all-components-values.yaml (every route on) with the three plugins switched on.
Run: python3 charts/customer-payments/tests/test_kong_config.py   (needs helm and PyYAML)
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART = Path(__file__).resolve().parents[1]
FORBIDDEN_PLUGINS = {
    "basic-auth", "key-auth", "jwt", "oauth2", "oidc", "acl",
    "hmac-auth", "ldap-auth", "mtls", "session", "request-termination",
}
PLUGINS_ON = [
    "--set", "kongPlugins.cors.enabled=true",
    "--set", "kongPlugins.prometheus.enabled=true",
    "--set", "kongPlugins.opentelemetry.enabled=true",
]


def render(values_file: str, extra: list[str]) -> list[dict]:
    out = subprocess.run(
        ["helm", "template", "customer-payments", str(CHART), "-f", str(CHART / "ci" / values_file), *extra],
        capture_output=True, text=True, check=True,
    ).stdout
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("FAIL: PyYAML not available; a Kong contract test must not pass vacuously", file=sys.stderr)
        raise SystemExit(1)
    return [d for d in yaml.safe_load_all(out) if d]


def main() -> int:
    failures: list[str] = []

    default_docs = render("default-values.yaml", [])
    kong = [d for d in default_docs if "Kong" in (d.get("kind") or "")]
    if kong:
        failures.append(f"default values render Kong objects {sorted(d['metadata']['name'] for d in kong)}; keep plugin blocks default-off")
    if any(d.get("kind") == "HTTPRoute" for d in default_docs):
        failures.append("default values render an HTTPRoute; routes must be default-off")

    docs = render("all-components-values.yaml", PLUGINS_ON)
    plugins = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "KongPlugin"}
    routes = [d for d in docs if d.get("kind") == "HTTPRoute"]
    if not routes:
        failures.append("the all-components render has no HTTPRoute at all")
    if not plugins:
        failures.append("the enabled render contains no KongPlugin at all")
    for r in routes:
        ann = (r.get("metadata", {}).get("annotations") or {}).get("konghq.com/plugins")
        if not ann:
            failures.append(f"HTTPRoute {r['metadata']['name']} has no konghq.com/plugins annotation")
            continue
        for name in ann.split(","):
            if name not in plugins:
                failures.append(f"HTTPRoute {r['metadata']['name']} references plugin '{name}' that the chart does not render")

    for name, p in plugins.items():
        if p.get("plugin", "") in FORBIDDEN_PLUGINS:
            failures.append(f"KongPlugin {name} is '{p['plugin']}': authentication/authorization plugins are forbidden at the gateway")

    for r in routes:
        name = r["metadata"]["name"]
        for rule in r.get("spec", {}).get("rules", []):
            for f in rule.get("filters", []):
                if f.get("type") == "URLRewrite":
                    target = f["urlRewrite"]["path"].get("replacePrefixMatch")
                    if name.endswith("-mcp") and target != "/mcp":
                        failures.append(f"MCP route {name} rewrites to {target!r}, expected '/mcp'")
                    if name.endswith("-reports") and target != "/reports":
                        failures.append(f"reports route {name} rewrites to {target!r}, expected '/reports'")

    cors = [p for p in plugins.values() if p.get("plugin") == "cors"]
    for p in cors:
        cfg = p.get("config", {})
        for header in ("Idempotency-Key", "If-Match"):
            if header not in cfg.get("headers", []):
                failures.append(f"cors plugin does not allow the {header} request header")
        for header in ("ETag", "Idempotency-Replayed", "Mcp-Session-Id"):
            if header not in cfg.get("exposed_headers", []):
                failures.append(f"cors plugin does not expose the {header} response header")
        if "*" in cfg.get("origins", []):
            failures.append("cors plugin allows origin '*'; use the exact origin")
    if not cors:
        failures.append("the enabled render contains no cors KongPlugin")

    if failures:
        print("FAIL: customer-payments chart Kong-config contract")
        for f in failures:
            print(f"  - {f}")
        return 1
    print(f"PASS: {len(routes)} HTTPRoutes annotated, {len(plugins)} KongPlugins, no auth plugin, defaults are a no-op")
    return 0


if __name__ == "__main__":
    sys.exit(main())
