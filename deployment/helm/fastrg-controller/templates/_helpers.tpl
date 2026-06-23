{{/*
Expand the name of the chart.
*/}}
{{- define "fastrg-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fastrg-controller.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "fastrg-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "fastrg-controller.labels" -}}
helm.sh/chart: {{ include "fastrg-controller.chart" . }}
{{ include "fastrg-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "fastrg-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fastrg-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the Secret holding jwt-secret / database-url. Uses an existing Secret
when controller.secrets.existingSecret is set, otherwise the chart-managed one.
*/}}
{{- define "fastrg-controller.secretName" -}}
{{- if .Values.controller.secrets.existingSecret -}}
{{- .Values.controller.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "fastrg-controller.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
Compute the PostgreSQL DSN (internal builds from postgresql-endpoint; external
uses the provided URL). Empty when postgresql.type is "none".
*/}}
{{- define "fastrg-controller.databaseUrl" -}}
{{- if eq .Values.postgresql.type "internal" -}}
postgres://{{ .Values.postgresql.internal.auth.username }}:{{ .Values.postgresql.internal.auth.password }}@postgresql-endpoint:{{ .Values.postgresql.internal.service.port }}/{{ .Values.postgresql.internal.auth.database }}?sslmode=disable
{{- else if and (eq .Values.postgresql.type "external") .Values.postgresql.external.url -}}
{{ .Values.postgresql.external.url }}
{{- end -}}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "fastrg-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "fastrg-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
