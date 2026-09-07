{{/*
Expand the name of the chart.
*/}}
{{- define "purser-cp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. Truncated to 63 chars for DNS-label safety.
*/}}
{{- define "purser-cp.fullname" -}}
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
{{- define "purser-cp.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels shared by every resource.
*/}}
{{- define "purser-cp.labels" -}}
helm.sh/chart: {{ include "purser-cp.chart" . }}
{{ include "purser-cp.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: purser
{{- end -}}

{{/*
Selector labels (release-scoped).
*/}}
{{- define "purser-cp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "purser-cp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: control-plane
{{- end -}}

{{/*
Image reference (falls back to .Chart.AppVersion when tag is empty).
*/}}
{{- define "purser-cp.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Secret name for the Control Plane secrets (db-url, license-key).
*/}}
{{- define "purser-cp.secretName" -}}
{{- printf "%s-secrets" (include "purser-cp.fullname" .) -}}
{{- end -}}

{{/*
PVC name for the Control Plane data volume.
*/}}
{{- define "purser-cp.pvcName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-data" (include "purser-cp.fullname" .) -}}
{{- end -}}
{{- end -}}
