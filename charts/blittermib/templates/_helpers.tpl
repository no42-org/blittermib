{{/*
Expand the name of the chart.
*/}}
{{- define "blittermib.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "blittermib.fullname" -}}
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
Chart name and version, for the chart label.
*/}}
{{- define "blittermib.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "blittermib.labels" -}}
helm.sh/chart: {{ include "blittermib.chart" . }}
{{ include "blittermib.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "blittermib.selectorLabels" -}}
app.kubernetes.io/name: {{ include "blittermib.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "blittermib.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "blittermib.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validation guards. Invoked from a template that always renders so the
checks fire on every render/install.
*/}}
{{- define "blittermib.validate" -}}
{{- if and .Values.ingress.enabled .Values.httpRoute.enabled -}}
{{- fail "blittermib: enable at most one of ingress.enabled or httpRoute.enabled, not both" -}}
{{- end -}}
{{- if ne (int .Values.replicaCount) 1 -}}
{{- fail "blittermib: replicaCount must be exactly 1 — the SQLite cache is per-pod and uploads are node-local, so replicas diverge; a persistence PVC is also ReadWriteOnce (single-attach)" -}}
{{- end -}}
{{- if and .Values.httpRoute.enabled (not .Values.httpRoute.parentRefs) -}}
{{- fail "blittermib: httpRoute.enabled requires at least one httpRoute.parentRefs entry (the existing Gateway to attach to)" -}}
{{- end -}}
{{- end -}}
