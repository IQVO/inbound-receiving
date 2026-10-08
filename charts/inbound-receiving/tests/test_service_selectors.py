#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates (api, mcp). A Service that selects on those two
alone selects ALL of them -- verified live in other fleet charts, where a Kong
request for an api /healthz was answered by the wrong pod. Every Deployment here
carries `app.kubernetes.io/component` in selector.matchLabels and its pod
labels, and every Service selects on it, so each Service selects EXACTLY ONE
Deployment. This test fails if that ever stops being true.

It mirrors product-master's charts/.../tests/test_service_selectors.py (and
warehouse-infra's scripts/check-chart-selectors.py), restricted to the
components this chart has: api (always), mcp (optional, default off) and
frontend (optional, default off). The frontend is the nginx pod serving the
inbound_mfe remote: its own workload, component=frontend, a ClusterIP Service,
never routed by this chart (warehouse-infra's Nginx web gateway owns
/mfes/inbound-receiving/).

Run: python3 charts/inbound-receiving/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "inbound-receiving"

# Dummy DSN, no password: the chart refuses to render without a database source.
BASE = ["--set", "database.url=postgres://u@example.invalid:5432/db"]
ENABLE_EVERYTHING = BASE + [
    "--set", "mcp.enabled=true",
    "--set", "frontend.enabled=true",
    "--set", "autoscaling.api.enabled=true",
    "--set", "config.eventPublisher=kafka",
    "--set", "config.productMode=kafka",
    "--set", "config.productConsumerGroup=inbound-receiving-product",
    "--set", "config.dockDoorMode=kafka",
    "--set", "config.dockDoorConsumerGroup=inbound-receiving-dock-door",
    "--set", "kafka.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
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
            "DOCK_DOOR_CONSUMER_GROUP",
        } & mcp_env
        if stray:
            failures.append(f"the MCP Deployment must not get Kafka/relay/consumer env (it never dials Kafka): {sorted(stray)}")
        # DOCK_DOOR_MODE is the one deliberate exception: it only labels list_docks.
        for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "DOCK_DOOR_MODE"):
            if want not in mcp_env:
                failures.append(f"the MCP Deployment does not render {want}")

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
    ):
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")


def check_refusals(failures: list[str]) -> None:
    kafka_modes = BASE + ["--set", "config.productMode=kafka", "--set", "config.productConsumerGroup=g"]
    for label, args, needle in (
        ("without a database source", [], "requires database.url or database.existingSecret"),
        ("EVENT_PUBLISHER=kafka without kafka", BASE + ["--set", "config.eventPublisher=kafka"], "kafka.enabled is false"),
        ("an unknown EVENT_PUBLISHER", BASE + ["--set", "config.eventPublisher=nats"], "want \"log\" or \"kafka\""),
        ("an unknown PRODUCT_MODE", BASE + ["--set", "config.productMode=strict"], "want \"permissive\" or \"kafka\""),
        ("PRODUCT_MODE=kafka without a group", BASE + ["--set", "config.productMode=kafka", "--set", "kafka.enabled=true"],
         "PRODUCT_MODE=kafka requires PRODUCT_CONSUMER_GROUP"),
        ("DOCK_DOOR_MODE=kafka without a group", BASE + ["--set", "config.dockDoorMode=kafka", "--set", "kafka.enabled=true"],
         "DOCK_DOOR_MODE=kafka requires DOCK_DOOR_CONSUMER_GROUP"),
        ("a kafka consumer mode without kafka", kafka_modes, "PRODUCT_MODE/DOCK_DOOR_MODE=kafka requires KAFKA_BROKERS"),
        ("a product group in permissive mode", BASE + ["--set", "config.productConsumerGroup=g"], "would silently not start"),
        ("a dock-door group in permissive mode", BASE + ["--set", "config.dockDoorConsumerGroup=g"], "would silently not start"),
    ):
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy the MCP component, an HPA or a route.
    defaults = render(BASE)
    stray = [d["metadata"]["name"] for d in defaults if d["metadata"]["name"].endswith(("-mcp", "-frontend"))]
    stray += [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    if {"PRODUCT_CONSUMER_GROUP", "DOCK_DOOR_CONSUMER_GROUP", "KAFKA_BROKERS"} & set(names):
        failures.append("the consumers / KAFKA_BROKERS must be off with default values")
    if "PRODUCT_MODE" not in names or "DOCK_DOOR_MODE" not in names:
        failures.append("PRODUCT_MODE / DOCK_DOOR_MODE must always be rendered (permissive by default)")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, frontend); mcp, frontend, HPA and routes are off by "
          "default; the chart refuses to render without a database source, with an unknown publisher/mode, "
          "with a Kafka mode but no broker or no consumer group, or with a group set but its consumer off")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
