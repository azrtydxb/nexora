{{/* Name prefix: the release name when it already says nexora, else <release>-nexora. */}}
{{- define "nexora.prefix" -}}
{{- if contains "nexora" .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-nexora" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* dict root, name */}}
{{- define "nexora.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{/* dict root, name */}}
{{- define "nexora.labels" -}}
{{ include "nexora.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/version: {{ .root.Values.image.tag | default .root.Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* dict root, name */}}
{{- define "nexora.image" -}}
{{ .root.Values.image.registry }}/{{ .name }}:{{ .root.Values.image.tag | default .root.Chart.AppVersion }}
{{- end -}}

{{- define "nexora.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "nexora.databaseEnv" -}}
- name: NEXORA_DATABASE_URL
  valueFrom:
    secretKeyRef:
{{- if eq .Values.database.mode "cnpg" }}
      name: {{ printf "%s-app" .Values.database.cnpg.clusterName }}
      key: uri
{{- else }}
      name: {{ required "database.external.existingSecret is required when database.mode=external" .Values.database.external.existingSecret }}
      key: {{ .Values.database.external.key | default "uri" }}
{{- end }}
{{- end -}}

{{- define "nexora.managementURL" -}}
{{- .Values.engine.managementURL | default (printf "https://%s-mgmt-grpc.%s.svc.cluster.local:9443" (include "nexora.prefix" .) .Release.Namespace) -}}
{{- end -}}

{{- define "nexora.grpcServerNames" -}}
{{- $svc := printf "%s-mgmt-grpc" (include "nexora.prefix" .) -}}
{{- $names := list $svc (printf "%s.%s.svc" $svc .Release.Namespace) (printf "%s.%s.svc.cluster.local" $svc .Release.Namespace) -}}
{{- if and .Values.mgmt.grpcLoadBalancer.enabled .Values.mgmt.grpcLoadBalancer.loadBalancerIP -}}
{{- $names = append $names .Values.mgmt.grpcLoadBalancer.loadBalancerIP -}}
{{- end -}}
{{- range .Values.mgmt.grpcServerNames -}}
{{- $names = append $names . -}}
{{- end -}}
{{- $names | uniq | join "," -}}
{{- end -}}

{{/* The OTLP endpoint mgmt hands to engines: explicit, else the bundled collector, else none. */}}
{{- define "nexora.otlpEndpoint" -}}
{{- if .Values.mgmt.otlpEndpoint -}}
{{- .Values.mgmt.otlpEndpoint -}}
{{- else if .Values.otelCollector.enabled -}}
{{- printf "http://%s-otelcol.%s.svc.cluster.local:4317" (include "nexora.prefix" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{/* dict root, group: the workload (and ConfigMap) name of an engine group. */}}
{{- define "nexora.engineWorkload" -}}
{{- .group.workloadName | default (printf "%s-engine-%s" (include "nexora.prefix" .root) .group.name) -}}
{{- end -}}

{{/* dict root, group: engine.toml for one engine group; node_name is replaced by NEXORA_ENGINE_NODE_NAME. */}}
{{- define "nexora.engineToml" -}}
{{- $p := .root.Values.engine.ports -}}
node_name = "engine"
state_dir = "/var/lib/nexora"
management_urls = [{{ include "nexora.managementURL" .root | quote }}]
join_token_file = "/etc/nexora/join/{{ .group.joinTokenKey | default "join-token" }}"
listen_udp = ["0.0.0.0:{{ $p.dns }}"]
listen_tcp = ["0.0.0.0:{{ $p.dns }}"]
{{- with $p.dot }}
listen_dot = ["0.0.0.0:{{ . }}"]
{{- end }}
{{- with $p.doh }}
listen_doh = ["0.0.0.0:{{ . }}"]
doh_path = {{ $.root.Values.engine.dohPath | quote }}
{{- end }}
{{- with $p.doq }}
listen_doq = ["0.0.0.0:{{ . }}"]
{{- end }}
metrics_listen = "0.0.0.0:{{ $p.metrics }}"
workers = {{ .root.Values.engine.workers }}
shutdown_drain_seconds = {{ .root.Values.engine.shutdownDrainSeconds }}
{{- end -}}
