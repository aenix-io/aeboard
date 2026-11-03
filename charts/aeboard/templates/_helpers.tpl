{{/* Chart name. */}}
{{- define "aeboard.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name. */}}
{{- define "aeboard.fullname" -}}
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
{{- define "aeboard.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. */}}
{{- define "aeboard.labels" -}}
helm.sh/chart: {{ include "aeboard.chart" . }}
{{ include "aeboard.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels. */}}
{{- define "aeboard.selectorLabels" -}}
app.kubernetes.io/name: {{ include "aeboard.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The tunnel connector's selector labels: a name of its own, so the
     Service, which selects the aeboard pods by aeboard.selectorLabels, never
     sends traffic to a connector pod. */}}
{{- define "aeboard.cloudflaredSelectorLabels" -}}
app.kubernetes.io/name: {{ include "aeboard.name" . }}-cloudflared
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* The data claim's name. */}}
{{- define "aeboard.claimName" -}}
{{- default (printf "%s-data" (include "aeboard.fullname" .)) .Values.persistence.existingClaim }}
{{- end }}
