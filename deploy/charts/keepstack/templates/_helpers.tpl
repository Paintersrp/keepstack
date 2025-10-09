{{- define "keepstack.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "keepstack.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "keepstack.namespace" -}}
{{- if .Values.namespace }}{{ .Values.namespace }}{{ else }}{{ .Release.Namespace }}{{ end -}}
{{- end -}}

{{- define "keepstack.image" -}}
{{- $registry := .Values.image.registry -}}
{{- $tag := .Values.image.tag -}}
{{- $pullPolicy := .Values.image.pullPolicy -}}
{{- dict "registry" $registry "tag" $tag "pullPolicy" $pullPolicy -}}
{{- end -}}
