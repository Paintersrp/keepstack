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

{{- define "keepstack.migrate.fullname" -}}
{{- printf "%s-migrate" (include "keepstack.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "keepstack.serviceAccountName.api" -}}
{{- if .Values.serviceAccounts.api.name -}}
{{- .Values.serviceAccounts.api.name -}}
{{- else -}}
{{- printf "%s-api" (include "keepstack.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "keepstack.serviceAccountName.worker" -}}
{{- if .Values.serviceAccounts.worker.name -}}
{{- .Values.serviceAccounts.worker.name -}}
{{- else -}}
{{- printf "%s-worker" (include "keepstack.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "keepstack.serviceAccountName.migrator" -}}
{{- if .Values.serviceAccounts.migrator.name -}}
{{- .Values.serviceAccounts.migrator.name -}}
{{- else -}}
{{- printf "%s-migrator" (include "keepstack.fullname" .) -}}
{{- end -}}
{{- end -}}
