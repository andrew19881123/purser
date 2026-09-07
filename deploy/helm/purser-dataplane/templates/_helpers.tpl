{{/*
Expand the name of the chart.
*/}}
{{- define "purser-dp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. Truncated to 63 chars for DNS-label safety.
*/}}
{{- define "purser-dp.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name and version, as used by the helm.sh/chart label.
*/}}
{{- define "purser-dp.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels shared by every resource.
*/}}
{{- define "purser-dp.labels" -}}
helm.sh/chart: {{ include "purser-dp.chart" . }}
{{ include "purser-dp.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: purser
{{- end -}}

{{/*
Selector labels (release-scoped).
*/}}
{{- define "purser-dp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "purser-dp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: gateway
{{- end -}}

{{/*
Image reference (falls back to .Chart.AppVersion when tag is empty).
*/}}
{{- define "purser-dp.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Secret name for Data Plane credentials (join-token, api-keys).
*/}}
{{- define "purser-dp.secretName" -}}
{{- printf "%s-secrets" (include "purser-dp.fullname" .) -}}
{{- end -}}
