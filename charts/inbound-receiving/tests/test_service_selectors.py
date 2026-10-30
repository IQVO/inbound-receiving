#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates (api, mcp, a2a). A Service that selects on those two
alone selects ALL of them -- verified live in other fleet charts, where a Kong
request for an api /healthz was answered by the wrong pod. Every Deployment here
carries `app.kubernetes.io/component` in selector.matchLabels and its pod
labels, and every Service selects on it, so each Service selects EXACTLY ONE
Deployment. This test fails if that ever stops being true.

It mirrors product-master's charts/.../tests/test_service_selectors.py (and
warehouse-infra's scripts/check-chart-selectors.py), restricted to the
components this chart has: api (always), mcp and a2a (optional, default off), frontend (optional, default off) and the
analytics read side (ADR 0006: analytics-projector, analytics-reports; optional, default off). The frontend is the
nginx pod serving the inbound_mfe remote: its own workload, component=frontend, a ClusterIP Service,
never routed by this chart (warehouse-infra's Nginx web gateway owns /mfes/inbound-receiving/).

Run: python3 charts/inbound-receiving/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "inbound-receiving"

# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and both consumer groups (the real runtime, ADR 0011).
DB = ["--set", "database.url=postgres://u@example.invalid:5432/db"]
KAFKA = ["--set", "kafka.brokers=kafka.example.invalid:9092"]
GROUPS = [
    "--set", "config.productConsumerGroup=inbound-receiving-product",
    "--set", "config.dockDoorConsumerGroup=inbound-receiving-dock-door",
    "--set", "config.packagingConsumerGroup=inbound-receiving-packaging",
]
BASE = DB + KAFKA + GROUPS
ENABLE_EVERYTHING = BASE + [
    "--set", "mcp.enabled=true",
    "--set", "a2a.enabled=true",
    "--set", "frontend.enabled=true",
    "--set", "autoscaling.api.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    "--set", "analytics.enabled=true",
    "--set", "analytics.database.projectorUrl=postgres://projector@example.invalid:5432/a",
    "--set", "analytics.database.reportsUrl=postgres://reports@example.invalid:5432/a",
]


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


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in dep["spec"]["template"]["spec"]["containers"][0].get("env", [])]


def check_a2a(services: dict, deployments: dict, failures: list[str]) -> None:
    """The A2A agent (docs/adr/0016): its own component, read-only like mcp."""
    a2a = f"{RELEASE}-a2a"
    if a2a not in services:
        failures.append("the A2A Service was not rendered with a2a.enabled=true")
    elif selector_of(services[a2a]).get("app.kubernetes.io/component") != "a2a":
        failures.append("the A2A Service selector must pin component=a2a")
    if a2a not in deployments:
        failures.append("the A2A Deployment was not rendered with a2a.enabled=true")
        return
    container = deployments[a2a]["spec"]["template"]["spec"]["containers"][0]
    if container.get("command") != ["/app/a2a"]:
        failures.append("the A2A Deployment must run /app/a2a")
    env = set(env_names(deployments[a2a]))
    stray = {
        "KAFKA_BROKERS", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL", "PRODUCT_MODE", "DOCK_DOOR_MODE",
        "PRODUCT_CONSUMER_GROUP", "DOCK_DOOR_CONSUMER_GROUP", "PACKAGING_CONSUMER_GROUP", "MCP_ADDR",
    } & env
    if stray:
        failures.append(f"the A2A Deployment is read-only and must not get Kafka/relay/consumer/mcp env: {sorted(stray)}")
    for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "A2A_ADDR", "A2A_AGENT_URL"):
        if want not in env:
            failures.append(f"the A2A Deployment does not render {want}")


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    mcp = f"{RELEASE}-mcp"
    if mcp not in services:
        failures.append("the MCP Service was not rendered with mcp.enabled=true")
    elif selector_of(services[mcp]).get("app.kubernetes.io/component") != "mcp":
        failures.append("the MCP Service selector must pin component=mcp")
    if mcp not in deployments:
        failures.append("the MCP Deployment was not rendered with mcp.enabled=true")
    else:
        container = deployments[mcp]["spec"]["template"]["spec"]["containers"][0]
        if container.get("command") != ["/app/mcp"]:
            failures.append("the MCP Deployment must run /app/mcp")
        mcp_env = set(env_names(deployments[mcp]))
        stray = {
            "KAFKA_BROKERS", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL", "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP",
            "DOCK_DOOR_CONSUMER_GROUP", "PACKAGING_CONSUMER_GROUP",
        } & mcp_env
        if stray:
            failures.append(f"the MCP Deployment must not get Kafka/relay/consumer env (it never dials Kafka): {sorted(stray)}")
        # DOCK_DOOR_MODE is the one deliberate exception: it only labels list_docks.
        for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "DOCK_DOOR_MODE"):
            if want not in mcp_env:
                failures.append(f"the MCP Deployment does not render {want}")

    check_a2a(services, deployments, failures)

    frontend = f"{RELEASE}-frontend"
    if frontend not in services:
        failures.append("the frontend Service was not rendered with frontend.enabled=true")
    else:
        if selector_of(services[frontend]).get("app.kubernetes.io/component") != "frontend":
            failures.append("the frontend Service selector must pin component=frontend")
        if services[frontend]["spec"].get("type") != "ClusterIP":
            failures.append("the frontend Service must be ClusterIP")
    if frontend not in deployments:
        failures.append("the frontend Deployment was not rendered with frontend.enabled=true")
    elif {"DATABASE_URL", "KAFKA_BROKERS"} & set(env_names(deployments[frontend])):
        failures.append("the frontend Deployment must not get database/Kafka env (it serves static bytes)")
    # Frontend routing belongs to warehouse-infra's Nginx web gateway, not this chart.
    for d in docs:
        if d.get("kind") in {"Ingress", "HTTPRoute"} and "frontend" in d["metadata"]["name"]:
            failures.append(f"{d['kind']} {d['metadata']['name']}: frontend routing must not live in this chart")
    check_analytics(services, deployments, failures)

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

    # The api HPA owns replicas when enabled.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    api_env = env_names(deployments.get(RELEASE, {"spec": {"template": {"spec": {"containers": [{}]}}}}))
    for want in (
        "EVENT_PUBLISHER", "KAFKA_BROKERS", "DATABASE_URL", "MIGRATIONS_DATABASE_URL",
        "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP", "DOCK_DOOR_MODE", "DOCK_DOOR_CONSUMER_GROUP",
        "PACKAGING_CONSUMER_GROUP",
    ):
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")


def check_analytics(services: dict, deployments: dict, failures: list[str]) -> None:
    """ADR 0006: the projector and the reports binaries, each its own component."""
    projector, reports = f"{RELEASE}-projector", f"{RELEASE}-reports"
    for name, component, command in (
        (projector, "analytics-projector", ["/app/inbound-projector"]),
        (reports, "analytics-reports", ["/app/inbound-reports"]),
    ):
        dep = deployments.get(name)
        if dep is None:
            failures.append(f"the {component} Deployment was not rendered with analytics.enabled=true")
            continue
        if dep["spec"]["selector"]["matchLabels"].get("app.kubernetes.io/component") != component:
            failures.append(f"Deployment {name} must pin component={component}")
        container = dep["spec"]["template"]["spec"]["containers"][0]
        if container.get("command") != command:
            failures.append(f"Deployment {name} must run {command}")
        env = set(env_names(dep))
        if "ANALYTICS_DATABASE_URL" not in env or "DATABASE_URL" in env:
            failures.append(f"Deployment {name} must read ANALYTICS_DATABASE_URL and never the OLTP DATABASE_URL")
    if projector in deployments and not {"KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP"} <= set(env_names(deployments[projector])):
        failures.append("the projector needs KAFKA_BROKERS and its fixed ANALYTICS_CONSUMER_GROUP")
    if reports in deployments:
        ref = next((e for e in deployments[reports]["spec"]["template"]["spec"]["containers"][0]["env"]
                    if e["name"] == "ANALYTICS_DATABASE_URL"), {})
        if ref.get("valueFrom", {}).get("secretKeyRef", {}).get("key") != "ANALYTICS_READER_DATABASE_URL":
            failures.append("the reports Deployment must be fed the READER DSN")
        if "KAFKA_BROKERS" in env_names(deployments[reports]):
            failures.append("the reports Deployment never dials Kafka")
    if projector in services:
        failures.append("the projector has no Service (it serves only probes)")
    if reports not in services:
        failures.append("the reports Service was not rendered")
    elif selector_of(services[reports]).get("app.kubernetes.io/component") != "analytics-reports":
        failures.append("the reports Service selector must pin component=analytics-reports")


def check_refusals(failures: list[str]) -> None:
    for label, args, needle in (
        ("without a database source", KAFKA + GROUPS, "requires database.url or database.existingSecret"),
        ("without Kafka brokers", DB + GROUPS, "kafka.brokers"),
        ("with Kafka switched off", BASE + ["--set", "kafka.enabled=false"], "Kafka cannot be switched off"),
        ("with EVENT_PUBLISHER=log", BASE + ["--set", "config.eventPublisher=log"], "the only publisher"),
        ("an unknown EVENT_PUBLISHER", BASE + ["--set", "config.eventPublisher=nats"], "the only publisher"),
        ("PRODUCT_MODE=permissive", BASE + ["--set", "config.productMode=permissive"], "the only mode is"),
        ("DOCK_DOOR_MODE=off", BASE + ["--set", "config.dockDoorMode=off"], "the only mode is"),
        ("an unknown PRODUCT_MODE", BASE + ["--set", "config.productMode=strict"], "the only mode is"),
        ("without a product consumer group", DB + KAFKA + GROUPS[2:], "config.productConsumerGroup is required"),
        ("without a dock-door consumer group", DB + KAFKA + GROUPS[:2], "config.dockDoorConsumerGroup is required"),
        ("without a packaging consumer group", DB + KAFKA + GROUPS[:4], "config.packagingConsumerGroup is required"),
        ("EVENT_PUBLISHER=log through extraEnv",
         BASE + ["--set", "extraEnv[0].name=EVENT_PUBLISHER", "--set", "extraEnv[0].value=log"], "extraEnv sets EVENT_PUBLISHER"),
        ("a *_MODE through extraEnv",
         BASE + ["--set", "extraEnv[0].name=DOCK_DOOR_MODE", "--set", "extraEnv[0].value=permissive"], "extraEnv sets DOCK_DOOR_MODE"),
        ("analytics without a DSN source", BASE + ["--set", "analytics.enabled=true"], "neither analytics.database.projectorUrl"),
    ):
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}: want {needle!r}, got {result.stderr.strip()[:200]!r}")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy the MCP or A2A component, an HPA or a route.
    defaults = render(BASE)
    stray = [d["metadata"]["name"] for d in defaults
             if d["metadata"]["name"].endswith(("-mcp", "-a2a", "-frontend", "-projector", "-reports", "-analytics"))]
    stray += [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    for want in ("PRODUCT_CONSUMER_GROUP", "DOCK_DOOR_CONSUMER_GROUP", "KAFKA_BROKERS", "PRODUCT_MODE", "DOCK_DOOR_MODE", "EVENT_PUBLISHER"):
        if want not in names:
            failures.append(f"{want} must always be rendered: the consumers, the relay and Kafka are always on (ADR 0011)")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, a2a, frontend, analytics-reports); mcp, a2a, frontend, analytics, HPA and routes are off by "
          "default; the chart refuses to render without a database source, without Kafka brokers, without a "
          "consumer group, with kafka switched off, with the log publisher or a permissive/off mode (values or "
          "extraEnv), or with analytics but no analytical DSN")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
