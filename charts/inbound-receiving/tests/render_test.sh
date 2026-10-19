#!/usr/bin/env bash
# Chart render assertions (gap fixes D12, D14, D21 and D24, 2026-10-12, and the
# real runtime of ADR 0011: Postgres and Kafka are the only runtime, so the
# mandatory configuration has no silent default, no permissive/off/log mode is
# ever rendered and the env the binaries read is the env the chart renders).
# Run: bash charts/inbound-receiving/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
release=inbound-receiving
# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and both consumer groups (the real runtime, ADR 0011).
# None is ever dialled.
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092
  --set config.productConsumerGroup=inbound-receiving-product
  --set config.dockDoorConsumerGroup=inbound-receiving-dock-doors)
analytics=(--set analytics.enabled=true
  --set analytics.database.projectorUrl=postgres://projector@example.invalid:5432/a
  --set analytics.database.reportsUrl=postgres://reports@example.invalid:5432/a)

fail() { echo "FAIL: $*" >&2; exit 1; }

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# D14: every config.* value is rendered into the chart ConfigMap, and the api
# Deployment -- the only workload whose binary (cmd/api) reads those keys
# (HTTP_ADDR, OUTBOX_RELAY_INTERVAL, OUTBOX_MAX_ATTEMPTS, SHUTDOWN_DRAIN_DELAY,
# ...) -- must mount it with envFrom, or a value that is only in the
# ConfigMap silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' \
  || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" \
  || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"

# A tuned config.* value reaches the ConfigMap the api mounts.
tuned="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_RELAY_INTERVAL: "7s"' <<<"$tuned" || fail "config.outboxRelayInterval is not rendered into the ConfigMap"
grep -q 'OUTBOX_MAX_ATTEMPTS: "5"' <<<"$tuned" || fail "config.outboxMaxAttempts is not rendered into the ConfigMap"

# D12 / ADR 0007: the relay attempt cap defaults to 20 and 0 (unlimited) is
# rendered as is.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20 (ADR 0007)"
grep -A1 'name: OUTBOX_MAX_ATTEMPTS' <<<"$api_deployment" | grep -q 'value: "20"' \
  || fail "api Deployment does not set OUTBOX_MAX_ATTEMPTS"
unlimited="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"

# D24 / ADR 0010: the housekeeping knobs reach the api (ConfigMap, envFrom)
# and the projector (explicit env), with the fleet defaults.
for kv in 'HOUSEKEEPING_INTERVAL: "1h"' 'IDEMPOTENCY_KEY_TTL: "24h"' 'OUTBOX_RETENTION: "168h"' 'PROCESSED_EVENTS_RETENTION: "720h"'; do
  grep -q "$kv" <<<"$configmap" || fail "ConfigMap is missing $kv (ADR 0010)"
done
projector_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --set config.processedEventsRetention=0 --show-only templates/projector-deployment.yaml)"
grep -A1 'name: HOUSEKEEPING_INTERVAL' <<<"$projector_deployment" | grep -q 'value: "1h"' \
  || fail "projector Deployment does not set HOUSEKEEPING_INTERVAL"
grep -A1 'name: PROCESSED_EVENTS_RETENTION' <<<"$projector_deployment" | grep -q 'value: "0"' \
  || fail "projector Deployment does not render PROCESSED_EVENTS_RETENTION (0 must stay 0)"

# The other workloads read their own explicit env and must NOT inherit the
# api's ConfigMap (reports and projector would bind the api's HTTP_ADDR).
for template in reports-deployment.yaml projector-deployment.yaml mcp-deployment.yaml; do
  if helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --set mcp.enabled=true --show-only "templates/$template" | grep -q 'configMapRef:'; then
    fail "$template must not mount the api ConfigMap"
  fi
done

# D21: every backend Deployment that sets up telemetry gets ENVIRONMENT
# (deployment.environment.name) and SERVICE_VERSION; the projector also gets
# the OTLP endpoint now that it calls telemetry.Setup.
for template in deployment.yaml mcp-deployment.yaml projector-deployment.yaml reports-deployment.yaml; do
  rendered="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --set mcp.enabled=true --set environment=staging --show-only "templates/$template")"
  grep -A1 'name: ENVIRONMENT' <<<"$rendered" | grep -q 'value: "staging"' || fail "$template does not set ENVIRONMENT from .Values.environment"
  grep -q 'name: SERVICE_VERSION' <<<"$rendered" || fail "$template does not set SERVICE_VERSION"
  grep -q 'name: OTEL_EXPORTER_OTLP_ENDPOINT' <<<"$rendered" || fail "$template does not set OTEL_EXPORTER_OTLP_ENDPOINT with otel.enabled"
done

# --- ADR 0011: the mandatory configuration is rendered from values -----------------
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
[[ "$(env_value "$api_deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
for kv in 'EVENT_PUBLISHER=kafka' 'PRODUCT_MODE=kafka' 'DOCK_DOOR_MODE=kafka' \
  'PRODUCT_CONSUMER_GROUP=inbound-receiving-product' 'DOCK_DOOR_CONSUMER_GROUP=inbound-receiving-dock-doors'; do
  [[ "$(env_value "$api_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv"
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$configmap" || fail "the ConfigMap does not carry $kv"
done
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
render_all="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --set mcp.enabled=true --set frontend.enabled=true)"
if grep -qiE 'value: "?(permissive|log|off)"?$' <<<"$render_all"; then fail "the render carries permissive/log/off, which the binaries reject at boot"; fi
if grep -qiE 'in-memory|silently runs|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then
  fail "the render or NOTES.txt mentions an in-memory / permissive mode"
fi
mcp_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-deployment.yaml)"
grep -A4 'name: DATABASE_URL$' <<<"$mcp_dep" | grep -q 'secretKeyRef:' || fail "the mcp DATABASE_URL must come from a Secret"
[[ "$(env_value "$mcp_dep" DOCK_DOOR_MODE)" == "kafka" ]] || fail "the mcp Deployment must report DOCK_DOOR_MODE=kafka"
proj="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --show-only templates/projector-deployment.yaml)"
[[ "$(env_value "$proj" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "the projector KAFKA_BROKERS is not rendered from kafka.brokers"
[[ "$(env_value "$proj" ANALYTICS_CONSUMER_GROUP)" == "inbound-receiving-analytics" ]] || fail "ANALYTICS_CONSUMER_GROUP must default to inbound-receiving-analytics"

# --- chart vs binary: every mandatory variable a binary names is rendered -----------
# A new `errors.New("X is required ...")` in a boot config fails here until the
# chart carries X for that binary.
repo_dir="$(cd "$chart_dir/../.." && pwd)"
reports="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --show-only templates/reports-deployment.yaml)"
for bin in api mcp inbound-projector inbound-reports; do
  case "$bin" in
    api) rendered="$api_deployment" ;;
    mcp) rendered="$mcp_dep" ;;
    inbound-projector) rendered="$proj" ;;
    *) rendered="$reports" ;;
  esac
  vars="$(grep -ho 'errors.New("[A-Z_]* is required' "$repo_dir"/cmd/"$bin"/main.go | sed -E 's/errors.New\("//; s/ is required//' | sort -u)"
  [[ -n "$vars" ]] || fail "found no mandatory variables in cmd/$bin; the scan is looking in the wrong place"
  for var in $vars; do
    grep -q "name: $var\$" <<<"$rendered" || fail "the $bin Deployment does not render $var, which cmd/$bin requires"
  done
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
expect_refusal "no database source" "database.url or database.existingSecret" "${base[@]:2}"
expect_refusal "no brokers" "kafka.brokers" --set database.url=postgres://u@h/db \
  --set config.productConsumerGroup=g1 --set config.dockDoorConsumerGroup=g2
expect_refusal "kafka switched off" "Kafka cannot be switched off" "${base[@]}" --set kafka.enabled=false
expect_refusal "the log publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=log
expect_refusal "permissive product mode" "config.productMode" "${base[@]}" --set config.productMode=permissive
expect_refusal "off dock-door mode" "config.dockDoorMode" "${base[@]}" --set config.dockDoorMode=off
expect_refusal "no product group" "config.productConsumerGroup" --set database.url=postgres://u@h/db --set kafka.brokers=k:9092 \
  --set config.dockDoorConsumerGroup=g2
expect_refusal "no dock-door group" "config.dockDoorConsumerGroup" --set database.url=postgres://u@h/db --set kafka.brokers=k:9092 \
  --set config.productConsumerGroup=g1
expect_refusal "the log publisher through extraEnv" "EVENT_PUBLISHER" "${base[@]}" --set 'extraEnv[0].name=EVENT_PUBLISHER' --set 'extraEnv[0].value=log'
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=PRODUCT_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "analytics without a DSN source" "analytics.database.projectorUrl" "${base[@]}" --set analytics.enabled=true
expect_refusal "mcp with a permissive dock-door mode" "config.dockDoorMode" "${base[@]}" --set mcp.enabled=true --set config.dockDoorMode=permissive

# --- what warehouse-infra sets keeps working -----------------------------------------
# tests/warehouse-infra-platform-values.yaml + tests/warehouse-infra-values.yaml
# mirror helm-values/_platform.yaml + helm-values/inbound-receiving.yaml of
# warehouse-infra, layered in the order ArgoCD uses: kafka.enabled='true',
# eventPublisher=kafka, both modes kafka with their groups, the existing
# database secret, the analytics secret, mcp and frontend on, empty extraEnv and
# the platform brokers. Infra needs NO values change for ADR 0011.
infra=(-f "$chart_dir/tests/warehouse-infra-platform-values.yaml" -f "$chart_dir/tests/warehouse-infra-values.yaml" --set image.tag=local-test)
infra_all="$(helm template "$release" "$chart_dir" "${infra[@]}")" || fail "the warehouse-infra values no longer render"
infra_api="$(helm template "$release" "$chart_dir" "${infra[@]}" --show-only templates/deployment.yaml)"
[[ "$(env_value "$infra_api" KAFKA_BROKERS)" == "kafka.warehouse-systems.svc.cluster.local:9092" ]] || fail "infra values: KAFKA_BROKERS"
[[ "$(env_value "$infra_api" EVENT_PUBLISHER)" == "kafka" ]] || fail "infra values: EVENT_PUBLISHER"
[[ "$(env_value "$infra_api" PRODUCT_CONSUMER_GROUP)" == "inbound-receiving-product" ]] || fail "infra values: PRODUCT_CONSUMER_GROUP"
[[ "$(env_value "$infra_api" DOCK_DOOR_CONSUMER_GROUP)" == "inbound-receiving-dock-doors" ]] || fail "infra values: DOCK_DOOR_CONSUMER_GROUP"
grep -q 'inbound-receiving-db' <<<"$infra_api" || fail "infra values: DATABASE_URL must come from the inbound-receiving-db secret"
infra_proj="$(helm template "$release" "$chart_dir" "${infra[@]}" --show-only templates/projector-deployment.yaml)"
[[ "$(env_value "$infra_proj" ANALYTICS_CONSUMER_GROUP)" == "inbound-receiving-analytics" ]] || fail "infra values: ANALYTICS_CONSUMER_GROUP"
for component in api mcp analytics-projector analytics-reports frontend; do
  grep -q "app.kubernetes.io/component: $component" <<<"$infra_all" || fail "infra values: component $component not rendered"
done

echo "PASS: inbound-receiving chart render assertions"
