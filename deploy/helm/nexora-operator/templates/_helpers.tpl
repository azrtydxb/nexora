{{- define "nexora-operator.selectorLabels" -}}
app.kubernetes.io/name: nexora-operator
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "nexora-operator.labels" -}}
{{ include "nexora-operator.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* The rule set shared by the ClusterRole and the per-namespace Roles. */}}
{{- define "nexora-operator.rules" -}}
- apiGroups: [apps]
  resources: [deployments, daemonsets]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [""]
  resources: [services, configmaps, secrets]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [policy]
  resources: [poddisruptionbudgets]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [networking.k8s.io]
  resources: [ingresses]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [postgresql.cnpg.io]
  resources: [clusters, scheduledbackups]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: [monitoring.coreos.com]
  resources: [servicemonitors, prometheusrules]
  verbs: [get, list, watch, create, update, patch, delete]
- apiGroups: ["", events.k8s.io]
  resources: [events]
  verbs: [create, patch]
- apiGroups: [nexora.io]
  resources: [nexorainstallations, nexoraenginegroups]
  verbs: [get, list, watch, update, patch]
- apiGroups: [nexora.io]
  resources: [nexorainstallations/status, nexoraenginegroups/status]
  verbs: [get, update, patch]
- apiGroups: [nexora.io]
  resources: [nexorainstallations/finalizers, nexoraenginegroups/finalizers]
  verbs: [update]
{{- end -}}
