{{/*
Expand the name of the chart.
*/}}
{{- define "inbound-receiving.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "inbound-receiving.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "inbound-receiving.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "inbound-receiving.labels" -}}
helm.sh/chart: {{ include "inbound-receiving.chart" . }}
{{ include "inbound-receiving.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. Shared by every pod this release creates, so every
Deployment and Service ALSO pins app.kubernetes.io/component (api, mcp):
a Service selecting on these two alone would select every component.
*/}}
{{- define "inbound-receiving.selectorLabels" -}}
app.kubernetes.io/name: {{ include "inbound-receiving.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "inbound-receiving.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "inbound-receiving.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "inbound-receiving.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "inbound-receiving.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Fully qualified name of the MCP server deployment/service.
*/}}
{{- define "inbound-receiving.mcpFullname" -}}
{{- include "inbound-receiving.fullname" . }}-mcp
{{- end }}

{{/*
Image reference shared by every Deployment (api, mcp, analytics-projector, analytics-reports).
*/}}
{{- define "inbound-receiving.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY (ADR 0011: Postgres is the only runtime). cmd/api and cmd/mcp exit at
boot without DATABASE_URL; there is no in-memory mode. Fails the render
naming the values instead of crash-looping.
*/}}
{{- define "inbound-receiving.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "inbound-receiving requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it (ADR 0011)." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY (ADR 0011: Kafka is the only runtime). cmd/api exits at boot without
KAFKA_BROKERS and rejects EVENT_PUBLISHER=log; the projector needs the brokers
too. There is no default broker, no log sink and no way to switch Kafka off.
kafka.enabled is kept only so values written before ADR 0011 still render
(warehouse-infra sets it to "true"): it may be true, never false.
*/}}
{{- define "inbound-receiving.requireKafka" -}}
{{- $_ := required "inbound-receiving requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it (ADR 0011)." .Values.kafka.brokers -}}
{{- if and (hasKey .Values.kafka "enabled") (or (not .Values.kafka.enabled) (eq (toString .Values.kafka.enabled) "false")) -}}
{{- fail "kafka.enabled is false — Kafka cannot be switched off: the service runs on real Kafka only (ADR 0011). Drop the key or set it to true and set kafka.brokers." -}}
{{- end -}}
{{- if ne (toString .Values.config.eventPublisher) "kafka" -}}
{{- fail (printf "config.eventPublisher is %q — the only publisher is \"kafka\": the log sink was removed (ADR 0011); the binary rejects EVENT_PUBLISHER=log at boot." (toString .Values.config.eventPublisher)) -}}
{{- end -}}
{{- end -}}

{{/*
PRODUCT_MODE / DOCK_DOOR_MODE (ADR 0003, ADR 0011): the only mode is "kafka"
(the names are kept because docs and infra set them). "permissive" and "off"
no longer exist: the binary exits at boot on them. Also used by the MCP
Deployment, which reports DOCK_DOOR_MODE in list_docks.
*/}}
{{- define "inbound-receiving.requireConsumerModes" -}}
{{- range $key := list "productMode" "dockDoorMode" -}}
{{- $mode := index $.Values.config $key -}}
{{- if ne (toString $mode) "kafka" -}}
{{- fail (printf "config.%s is %q — the only mode is \"kafka\": the local copies are always fed from real Kafka and permissive/off were removed (ADR 0011); the binary exits at boot otherwise." $key (toString $mode)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Both consumers always run, each under a STABLE consumer group id that must
come from values (cmd/api exits at boot without PRODUCT_CONSUMER_GROUP /
DOCK_DOOR_CONSUMER_GROUP). A stable id is what lets a rescheduled pod resume
from its committed offsets.
*/}}
{{- define "inbound-receiving.requireConsumerGroups" -}}
{{- if not .Values.config.productConsumerGroup -}}
{{- fail "config.productConsumerGroup is required (PRODUCT_CONSUMER_GROUP): the product consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id (ADR 0011)." -}}
{{- end -}}
{{- if not .Values.config.dockDoorConsumerGroup -}}
{{- fail "config.dockDoorConsumerGroup is required (DOCK_DOOR_CONSUMER_GROUP): the dock-door consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id (ADR 0011)." -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not bring back a log or permissive/off mode (ADR 0011): no
EVENT_PUBLISHER other than kafka and no *_MODE variable at all (the modes have
dedicated values and the only legal value is "kafka").
*/}}
{{- define "inbound-receiving.requireNoInMemoryEnv" -}}
{{- range .Values.extraEnv -}}
{{- $name := toString .name -}}
{{- $value := toString (default "" .value) -}}
{{- if and (eq $name "EVENT_PUBLISHER") (ne $value "kafka") -}}
{{- fail (printf "extraEnv sets EVENT_PUBLISHER=%s — the only publisher is \"kafka\" (ADR 0011)." $value) -}}
{{- end -}}
{{- if hasSuffix "_MODE" $name -}}
{{- fail (printf "extraEnv sets %s — *_MODE variables are set through config.productMode / config.dockDoorMode and the only legal value is \"kafka\": inbound-receiving runs on real Postgres and real Kafka only, with no permissive or off mode (ADR 0011)." $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment (always rendered) includes it, so a mis-set release fails
`helm template` / `helm install` whichever component is enabled.
*/}}
{{- define "inbound-receiving.requireRealRuntime" -}}
{{- include "inbound-receiving.requireDatabase" . -}}
{{- include "inbound-receiving.requireKafka" . -}}
{{- include "inbound-receiving.requireConsumerModes" . -}}
{{- include "inbound-receiving.requireConsumerGroups" . -}}
{{- include "inbound-receiving.requireNoInMemoryEnv" . -}}
{{- end -}}

{{/*
Fully qualified name of the analytics projector deployment (ADR 0006).
*/}}
{{- define "inbound-receiving.projectorFullname" -}}
{{- include "inbound-receiving.fullname" . }}-projector
{{- end }}

{{/*
Fully qualified name of the analytics reports deployment/service (ADR 0006).
The reports Service is cluster-internal (component=analytics-reports); nothing
in this chart routes external traffic to it (warehouse-infra owns the edge).
*/}}
{{- define "inbound-receiving.reportsFullname" -}}
{{- include "inbound-receiving.fullname" . }}-reports
{{- end }}

{{/*
Name of the Secret holding the analytical DSNs: the operator's own
(analytics.database.existingSecret, keys ANALYTICS_DATABASE_URL and
ANALYTICS_READER_DATABASE_URL) or the one this chart creates.
*/}}
{{- define "inbound-receiving.analyticsSecretName" -}}
{{- if .Values.analytics.database.existingSecret }}
{{- .Values.analytics.database.existingSecret }}
{{- else }}
{{- include "inbound-receiving.fullname" . }}-analytics
{{- end }}
{{- end }}

{{/*
analytics.enabled needs an analytical DSN source (both binaries refuse to boot
without ANALYTICS_DATABASE_URL). The projector consumes
warehouse.inbound-receiving.analytics from kafka.brokers, which the api
Deployment already requires (requireKafka); the projector's consumer group
comes from analytics.projector.consumerGroup.
*/}}
{{- define "inbound-receiving.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.database.projectorUrl .Values.analytics.database.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.database.projectorUrl nor analytics.database.existingSecret is set — the projector and reports binaries refuse to boot without ANALYTICS_DATABASE_URL." -}}
{{- end -}}
{{- end -}}

{{/*
Comma-joined names of this chart's enabled KongPlugins, for the
konghq.com/plugins annotation on every HTTPRoute this chart renders.
Empty string (annotation omitted) when none are enabled — standalone
installs without Kong are unaffected.
*/}}
{{- define "inbound-receiving.kongPluginNames" -}}
{{- $names := list -}}
{{- range $name, $cfg := .Values.kongPlugins -}}
{{- if $cfg.enabled -}}
{{- $names = append $names (printf "%s-%s" (include "inbound-receiving.fullname" $) $name) -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}
