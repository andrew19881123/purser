{{/*
Expand the name of the chart.
*/}}
{{- define "purser.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. Truncated to 63 chars for DNS-label safety.
*/}}
{{- define "purser.fullname" -}}
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
{{- define "purser.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels shared by every resource.
*/}}
{{- define "purser.labels" -}}
helm.sh/chart: {{ include "purser.chart" . }}
{{ include "purser.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: purser
{{- end -}}

{{/*
Selector labels shared by every resource (release-scoped).
*/}}
{{- define "purser.selectorLabels" -}}
app.kubernetes.io/name: {{ include "purser.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Per-component labels. Call as: include "purser.componentLabels" (dict "ctx" . "component" "control-plane")
*/}}
{{- define "purser.componentLabels" -}}
{{ include "purser.labels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Per-component selector labels. Call as: include "purser.componentSelectorLabels" (dict "ctx" . "component" "control-plane")
*/}}
{{- define "purser.componentSelectorLabels" -}}
{{ include "purser.selectorLabels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Component resource names.
*/}}
{{- define "purser.controlPlane.fullname" -}}{{ printf "%s-control-plane" (include "purser.fullname" .) }}{{- end -}}
{{- define "purser.gateway.fullname" -}}{{ printf "%s-gateway" (include "purser.fullname" .) }}{{- end -}}
{{- define "purser.ui.fullname" -}}{{ printf "%s-ui" (include "purser.fullname" .) }}{{- end -}}

{{/*
Secret names.
*/}}
{{- define "purser.gatewaySecretName" -}}{{ printf "%s-gateway" (include "purser.fullname" .) }}{{- end -}}
{{- define "purser.licenseSecretName" -}}{{ printf "%s-license" (include "purser.fullname" .) }}{{- end -}}

{{/*
Image references (fall back to .Chart.AppVersion when tag is empty).
*/}}
{{- define "purser.controlPlane.image" -}}
{{- $tag := .Values.image.controlPlane.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.controlPlane.repository $tag -}}
{{- end -}}
{{- define "purser.gateway.image" -}}
{{- $tag := .Values.image.gateway.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.gateway.repository $tag -}}
{{- end -}}
{{- define "purser.ui.image" -}}
{{- $tag := .Values.image.ui.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.ui.repository $tag -}}
{{- end -}}

{{/*
Resolve the shared gateway internal token:
  1. explicit .Values.gateway.internalToken, else
  2. reuse the value already stored in the gateway Secret (idempotent upgrades), else
  3. generate a random 40-char token.
During `helm template`/`lint` the cluster lookup returns empty, so a random
value is rendered — which renders cleanly (no error).
*/}}
{{- define "purser.gatewayToken" -}}
{{- if .Values.gateway.internalToken -}}
{{- .Values.gateway.internalToken -}}
{{- else -}}
{{- $existing := (lookup "v1" "Secret" .Release.Namespace (include "purser.gatewaySecretName" .)) | default dict -}}
{{- $data := (get $existing "data") | default dict -}}
{{- if hasKey $data "internal-token" -}}
{{- get $data "internal-token" | b64dec -}}
{{- else -}}
{{- randAlphaNum 40 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Gateway service base URL used by the Control Plane for route sync.
*/}}
{{- define "purser.gatewayURL" -}}
{{- printf "http://%s:%d" (include "purser.gateway.fullname" .) (int .Values.gateway.port) -}}
{{- end -}}
