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
{{- if .Values.azure.enabled -}}
{{- range $k := list "tenantId" "keyVaultName" -}}
{{- if not (index $.Values.azure $k) }}{{ fail (printf "azure.%s is required when azure.enabled" $k) }}{{ end -}}
{{- end -}}
{{- range $k := list "api" "worker" "migrate" -}}
{{- if not (index $.Values.azure.clientIds $k) }}{{ fail (printf "azure.clientIds.%s is required when azure.enabled" $k) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- if and (eq .Values.worker.builder "acr") (not (and .Values.azure.enabled .Values.azure.acrRegistryId)) -}}
{{- fail "worker.builder=acr requires azure.enabled and azure.acrRegistryId" -}}
{{- end -}}
{{- if and (not .Values.azure.enabled) (not .Values.existingSecret) -}}
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


{{/* Name of the Secret a component reads: its own Key Vault-synced Secret
on Azure, otherwise the shared existingSecret. */}}
{{- define "clouddeploy.secretName" -}}
{{- if .root.Values.azure.enabled -}}
{{ include "clouddeploy.fullname" .root }}-{{ .component }}-kv
{{- else -}}
{{ .root.Values.existingSecret }}
{{- end -}}
{{- end -}}

{{/* Workload identity label for a pod template (Azure only). */}}
{{- define "clouddeploy.wiLabel" -}}
{{- if .Values.azure.enabled }}
azure.workload.identity/use: "true"
{{- end -}}
{{- end -}}

{{/* CSI volume that fetches Key Vault secrets and keeps the synced Secret
in existence while the pod runs (Azure only). */}}
{{- define "clouddeploy.kvVolume" -}}
{{- if .root.Values.azure.enabled }}
- name: key-vault
  csi:
    driver: secrets-store.csi.k8s.io
    readOnly: true
    volumeAttributes: {secretProviderClass: {{ include "clouddeploy.fullname" .root }}-{{ .component }}}
{{- end -}}
{{- end -}}

{{- define "clouddeploy.kvMount" -}}
{{- if .Values.azure.enabled }}
- {name: key-vault, mountPath: /mnt/secrets, readOnly: true}
{{- end -}}
{{- end -}}
