{{- define "talosdeck.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "talosdeck.fullname" -}}
{{- default (printf "%s-%s" .Release.Name (include "talosdeck.name" .)) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "talosdeck.selectorLabels" -}}
app.kubernetes.io/name: {{ include "talosdeck.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "talosdeck.labels" -}}
{{ include "talosdeck.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
