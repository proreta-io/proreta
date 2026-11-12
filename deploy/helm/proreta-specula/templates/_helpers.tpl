{{- define "proreta-specula.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "proreta-specula.fullname" -}}
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
{{- end }}

{{- define "proreta-specula.selectorLabels" -}}
app.kubernetes.io/name: {{ include "proreta-specula.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "proreta-specula.labels" -}}
{{ include "proreta-specula.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Image tag: image.tag, else the chart's appVersion; a git checkout has neither. */}}
{{- define "proreta-specula.image" -}}
{{- $tag := .Values.image.tag -}}
{{- if not $tag -}}
{{- if or (not .Chart.AppVersion) (eq .Chart.AppVersion "0.0.0") -}}
{{- fail "image.tag is required (this chart has no release appVersion): a release version such as 1.2.3 or sha-<7 hex>" -}}
{{- end -}}
{{- $tag = .Chart.AppVersion -}}
{{- end -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}
