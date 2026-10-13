{{/* Resource name prefix: <release>-clouddeploy, or just the release name if it already contains "clouddeploy". */}}
{{- define "clouddeploy.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "clouddeploy.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Selector labels must never change across upgrades (they are immutable). */}}
{{- define "clouddeploy.selector" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "clouddeploy.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/* Pod/container hardening that satisfies the Pod Security "restricted" profile. */}}
{{- define "clouddeploy.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "clouddeploy.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{/* Guards: refuse to render unsafe or inconsistent combinations. */}}
{{- define "clouddeploy.validate" -}}
{{- if not .Values.existingSecret -}}
{{- fail "existingSecret is required: the chart never contains secrets" -}}
{{- end -}}
{{- if and .Values.dev.dockerSocket.enabled (eq .Values.config.appEnv "production") -}}
{{- fail "dev.dockerSocket.enabled is forbidden when config.appEnv=production (root on the node; see ADR-008)" -}}
{{- end -}}
{{- if and (eq .Values.worker.builder "docker") (not .Values.dev.dockerSocket.enabled) -}}
{{- fail "worker.builder=docker requires dev.dockerSocket.enabled (development only)" -}}
{{- end -}}
{{- if not (has .Values.config.appEnv (list "development" "staging" "production")) -}}
{{- fail "config.appEnv must be development, staging or production" -}}
{{- end -}}
{{- if and .Values.api.hpa.enabled (lt (int .Values.api.hpa.minReplicas) 2) -}}
{{- fail "api.hpa.minReplicas must be >= 2 for availability" -}}
{{- end -}}
{{- end -}}
