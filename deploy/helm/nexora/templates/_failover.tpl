{{/* Availability pairs are independent of engine policy groups and immutable workload selectors. */}}
{{- define "nexora.failoverPairFor" -}}
{{- range .group.failoverPairs | default list -}}
{{- if has $.instance .members -}}{{ .name }}{{- end -}}
{{- end -}}
{{- end -}}

{{- define "nexora.validateFailover" -}}
{{- $e := .Values.engine -}}
{{- $selected := false -}}
{{- $states := dict -}}
{{- $serviceNames := dict -}}
{{- $addresses := dict -}}
{{- if and $e.pairedRollout.enabled (ne $e.kind "DaemonSet") -}}
{{- fail "failover pairedRollout requires DaemonSet workloads" -}}
{{- end -}}
{{- range $g := $e.groups -}}
{{- $group := include "nexora.engineWorkload" (dict "root" $ "group" $g) -}}
{{- $instances := dict -}}
{{- range $i := $g.instances | default list -}}
{{- if hasKey $instances $i.name -}}{{ fail "failover instance names must be unique" }}{{- end -}}
{{- $_ := set $instances $i.name $i -}}
{{- if eq $e.pairedRollout.workload (printf "%s-%s" $group $i.name) -}}{{- $selected = true -}}{{- end -}}
{{- if $g.failoverPairs -}}
{{- $state := printf "%s/%s" $i.node ($i.stateDirName | default $group) -}}
{{- if hasKey $states $state -}}{{ fail "failover instances on the same node must not share state" }}{{- end -}}
{{- $_ := set $states $state true -}}
{{- if $i.service -}}{{ fail "failover instances must use pair Services, not instance Services" }}{{- end -}}
{{- end -}}
{{- end -}}
{{- $members := dict -}}
{{- $pairs := dict -}}
{{- if $g.failoverPairs -}}
{{- if not $e.pairedRollout.enabled -}}{{ fail "failover pairs require pairedRollout.enabled" }}{{- end -}}
{{- if or $e.hostNetwork (ne $e.stateDir.type "hostPath") -}}{{ fail "failover pairs require pod networking and persistent hostPath state" }}{{- end -}}
{{- if or $g.service $g.extraServices -}}{{ fail "failover groups must expose only their pair Services" }}{{- end -}}
{{- end -}}
{{- range $p := $g.failoverPairs | default list -}}
{{- if hasKey $pairs $p.name -}}{{ fail "failover pair names must be unique within a group" }}{{- end -}}
{{- $_ := set $pairs $p.name true -}}
{{- if or (hasKey $serviceNames $p.service.name) (hasKey $addresses $p.service.loadBalancerIP) -}}{{ fail "failover Services and addresses must be distinct" }}{{- end -}}
{{- $_ := set $serviceNames $p.service.name true -}}
{{- $_ := set $addresses $p.service.loadBalancerIP true -}}
{{- $nodes := dict -}}
{{- range $m := $p.members -}}
{{- if not (hasKey $instances $m) -}}{{ fail "failover pair names an unknown member" }}{{- end -}}
{{- if hasKey $members $m -}}{{ fail "failover pair members must be disjoint" }}{{- end -}}
{{- $_ := set $members $m true -}}
{{- $node := (get $instances $m).node -}}
{{- if hasKey $nodes $node -}}{{ fail "failover pair members must run on different nodes" }}{{- end -}}
{{- $_ := set $nodes $node true -}}
{{- end -}}
{{- end -}}
{{- if and $g.failoverPairs (ne (len $members) (len $instances)) -}}{{ fail "failover pairs must cover every instance" }}{{- end -}}
{{- end -}}
{{- if and $e.pairedRollout.workload (not $selected) -}}{{ fail "failover pairedRollout names an unknown workload" }}{{- end -}}
{{- end -}}
