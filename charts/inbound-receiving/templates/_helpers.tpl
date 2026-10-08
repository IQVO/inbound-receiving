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
Fails chart rendering with a clear message if no DATABASE_URL source is
configured. The binary would silently fall back to in-memory adapters (state
lost on restart) -- fine for `go run`, never a deployable state, so surface it
as a helm render-time error instead.
*/}}
{{- define "inbound-receiving.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "inbound-receiving requires database.url or database.existingSecret to be set — without DATABASE_URL the binary silently runs on in-memory adapters, which is not a deployable state." -}}
{{- end -}}
{{- end -}}

{{/*
EVENT_PUBLISHER=kafka makes the binary refuse to boot without KAFKA_BROKERS
(cmd/api startOutboxRelay), and KAFKA_BROKERS is only rendered when
kafka.enabled is true -- so that combination would crash-loop. Fail at render.
*/}}
{{- define "inbound-receiving.requireKafkaForPublisher" -}}
{{- if and (eq .Values.config.eventPublisher "kafka") (not .Values.kafka.enabled) -}}
{{- fail "config.eventPublisher is \"kafka\" but kafka.enabled is false — the binary exits at boot (EVENT_PUBLISHER=kafka requires KAFKA_BROKERS). Set kafka.enabled=true and kafka.brokers." -}}
{{- end -}}
{{- end -}}

{{/*
config.eventPublisher must be one of the values the binary parses
(parsePublisherMode): anything else exits at boot.
*/}}
{{- define "inbound-receiving.requireKnownPublisher" -}}
{{- if not (has .Values.config.eventPublisher (list "log" "kafka")) -}}
{{- fail (printf "config.eventPublisher is %q — want \"log\" or \"kafka\" (the binary exits at boot otherwise)." .Values.config.eventPublisher) -}}
{{- end -}}
{{- end -}}

{{/*
PRODUCT_MODE / DOCK_DOOR_MODE (ADR 0003) must be permissive or kafka, and a
kafka mode starts a consumer: it needs a stable consumer group id
(cmd/api loadConfig refuses to boot without one) and KAFKA_BROKERS. Without a
group the pod would crash-loop; without brokers it would too. Fail at render.
*/}}
{{- define "inbound-receiving.requireConsumerConfig" -}}
{{- range $key := list "productMode" "dockDoorMode" -}}
{{- $mode := index $.Values.config $key -}}
{{- if not (has $mode (list "permissive" "kafka")) -}}
{{- fail (printf "config.%s is %q — want \"permissive\" or \"kafka\" (the binary exits at boot otherwise)." $key $mode) -}}
{{- end -}}
{{- end -}}
{{- if and (eq .Values.config.productMode "kafka") (not .Values.config.productConsumerGroup) -}}
{{- fail "config.productMode is \"kafka\" but config.productConsumerGroup is empty — the binary exits at boot (PRODUCT_MODE=kafka requires PRODUCT_CONSUMER_GROUP). Use a STABLE id." -}}
{{- end -}}
{{- if and (eq .Values.config.dockDoorMode "kafka") (not .Values.config.dockDoorConsumerGroup) -}}
{{- fail "config.dockDoorMode is \"kafka\" but config.dockDoorConsumerGroup is empty — the binary exits at boot (DOCK_DOOR_MODE=kafka requires DOCK_DOOR_CONSUMER_GROUP). Use a STABLE id." -}}
{{- end -}}
{{- if and (or (eq .Values.config.productMode "kafka") (eq .Values.config.dockDoorMode "kafka")) (not .Values.kafka.enabled) -}}
{{- fail "config.productMode/dockDoorMode is \"kafka\" but kafka.enabled is false — the binary exits at boot (PRODUCT_MODE/DOCK_DOOR_MODE=kafka requires KAFKA_BROKERS). Set kafka.enabled=true and kafka.brokers." -}}
{{- end -}}
{{- end -}}

{{/*
A consumer group id set while its mode is permissive is a silent no-op (the
consumer never starts). Refuse it so the intent is never lost.
*/}}
{{- define "inbound-receiving.requireNoOrphanGroups" -}}
{{- if and .Values.config.productConsumerGroup (ne .Values.config.productMode "kafka") -}}
{{- fail "config.productConsumerGroup is set but config.productMode is not \"kafka\" — the product consumer would silently not start. Set productMode=kafka or clear the group." -}}
{{- end -}}
{{- if and .Values.config.dockDoorConsumerGroup (ne .Values.config.dockDoorMode "kafka") -}}
{{- fail "config.dockDoorConsumerGroup is set but config.dockDoorMode is not \"kafka\" — the dock-door consumer would silently not start. Set dockDoorMode=kafka or clear the group." -}}
{{- end -}}
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
without ANALYTICS_DATABASE_URL) and a broker (the projector consumes
warehouse.inbound-receiving.analytics from kafka.brokers). Fail at render
instead of crash-looping. Events only reach the analytics topic when
config.eventPublisher is "kafka"; that is documented, not enforced, since the
projector is harmless without events.
*/}}
{{- define "inbound-receiving.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.database.projectorUrl .Values.analytics.database.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.database.projectorUrl nor analytics.database.existingSecret is set — the projector and reports binaries refuse to boot without ANALYTICS_DATABASE_URL." -}}
{{- end -}}
{{- if not .Values.kafka.enabled -}}
{{- fail "analytics.enabled is true but kafka.enabled is false — the projector consumes warehouse.inbound-receiving.analytics and needs kafka.brokers. Set kafka.enabled=true." -}}
{{- end -}}
{{- end -}}
