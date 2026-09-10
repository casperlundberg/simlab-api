{{- define "simlab-api.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "simlab-api.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "simlab-api.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "simlab-api.labels" -}}
app.kubernetes.io/name: {{ include "simlab-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: autoscale-platform
{{- end -}}

{{- define "simlab-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "simlab-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "simlab-api.postgresName" -}}
{{- printf "%s-postgres" (include "simlab-api.fullname" .) -}}
{{- end -}}

{{- define "simlab-api.secretName" -}}
{{- printf "%s-config" (include "simlab-api.fullname" .) -}}
{{- end -}}

{{/*
The database URL. The embedded Postgres wins when it is enabled, so a
self-contained install needs no configuration at all.
*/}}
{{- define "simlab-api.databaseURL" -}}
{{- if .Values.database.url -}}
{{- .Values.database.url -}}
{{- else if .Values.database.embedded.enabled -}}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable"
    .Values.database.embedded.user .Values.database.embedded.password
    (include "simlab-api.postgresName" .) .Values.database.embedded.database -}}
{{- end -}}
{{- end -}}

{{/*
The autoscaler's address, defaulting to the release's own autoscaler in this
namespace — which is what deploying them together is for.
*/}}
{{- define "simlab-api.autoscalerURL" -}}
{{- if .Values.autoscaler.url -}}
{{- .Values.autoscaler.url -}}
{{- else -}}
{{- printf "http://%s-autoscaler:8080" .Release.Name -}}
{{- end -}}
{{- end -}}
