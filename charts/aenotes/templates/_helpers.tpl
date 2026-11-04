{{/* Chart name. */}}
{{- define "aenotes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name. */}}
{{- define "aenotes.fullname" -}}
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

{{/* Chart label. */}}
{{- define "aenotes.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. */}}
{{- define "aenotes.labels" -}}
helm.sh/chart: {{ include "aenotes.chart" . }}
{{ include "aenotes.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels. */}}
{{- define "aenotes.selectorLabels" -}}
app.kubernetes.io/name: {{ include "aenotes.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The tunnel connector's selector labels: a name of its own, so the
     Service, which selects the aenotes pods by aenotes.selectorLabels, never
     sends traffic to a connector pod. */}}
{{- define "aenotes.cloudflaredSelectorLabels" -}}
app.kubernetes.io/name: {{ include "aenotes.name" . }}-cloudflared
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The data claim's name. */}}
{{- define "aenotes.claimName" -}}
{{- default (printf "%s-data" (include "aenotes.fullname" .)) .Values.persistence.existingClaim }}
{{- end }}
