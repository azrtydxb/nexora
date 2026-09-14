package deploytest

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const chartDir = "../helm/nexora"

func helm(args ...string) (string, error) {
	out, err := exec.Command("helm", args...).CombinedOutput() // nosemgrep: dangerous-exec-command
	return string(out), err
}

type obj map[string]any

func (o obj) path(keys ...string) any {
	var cur any = map[string]any(o)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func render(t *testing.T, args ...string) []obj {
	t.Helper()
	out, err := helm(append([]string{"template", "nexora", chartDir, "--namespace", "nexora"}, args...)...)
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var docs []obj
	dec := yaml.NewDecoder(bytes.NewBufferString(out))
	for {
		// Decode into the unnamed map type: yaml.v3 gives nested mappings the type of the target
		// map, and path and the assertions below expect map[string]any.
		var o map[string]any
		if err := dec.Decode(&o); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if o != nil {
			docs = append(docs, obj(o))
		}
	}
	return docs
}

func find(t *testing.T, docs []obj, kind, name string) obj {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && d.path("metadata", "name") == name {
			return d
		}
	}
	t.Fatalf("%s/%s not rendered", kind, name)
	return nil
}

func has(docs []obj, kind string) bool {
	for _, d := range docs {
		if d["kind"] == kind {
			return true
		}
	}
	return false
}

func container(t *testing.T, workload obj, name string) obj {
	t.Helper()
	for _, c := range workload.path("spec", "template", "spec", "containers").([]any) {
		if c.(map[string]any)["name"] == name {
			return obj(c.(map[string]any))
		}
	}
	t.Fatalf("container %s missing", name)
	return nil
}

func env(c obj, name string) obj {
	for _, e := range c["env"].([]any) {
		if m := e.(map[string]any); m["name"] == name {
			return obj(m)
		}
	}
	return nil
}

func TestHelmTemplate(t *testing.T) {
	if out, err := helm("lint", chartDir, "--strict", "-f", chartDir+"/ci/lint-values.yaml"); err != nil {
		t.Fatalf("helm lint: %v\n%s", err, out)
	}

	kw := render(t, "-f", "../kw/values-kw.yaml", "--set", "image.tag=sha-0000000", "--api-versions", "monitoring.coreos.com/v1")
	mgmt := find(t, kw, "Deployment", "nexora-mgmt")
	if mgmt.path("spec", "replicas") != 2 {
		t.Errorf("mgmt replicas = %v", mgmt.path("spec", "replicas"))
	}
	mc := container(t, mgmt, "mgmt")
	if ref := env(mc, "NEXORA_DATABASE_URL").path("valueFrom", "secretKeyRef"); ref == nil || ref.(map[string]any)["name"] != "nexora-db-app" || ref.(map[string]any)["key"] != "uri" {
		t.Errorf("database env = %v", env(mc, "NEXORA_DATABASE_URL"))
	}
	for name, want := range map[string]string{
		"NEXORA_KEK_FILE": "/etc/nexora/kek/kek", "NEXORA_DNS_TLS_CERT_FILE": "/etc/nexora/dns-tls/tls.crt",
		"NEXORA_QUERYLOG_BACKEND": "opensearch", "NEXORA_SECURE_COOKIES": "true", "NEXORA_PUBLIC_URL": "https://nexora.kw.local",
	} {
		if e := env(mc, name); e == nil || e["value"] != want {
			t.Errorf("mgmt %s = %v, want %s", name, e, want)
		}
	}
	if names := env(mc, "NEXORA_GRPC_SERVER_NAMES"); names == nil || !strings.Contains(names["value"].(string), "192.168.10.135") ||
		!strings.Contains(names["value"].(string), "nexora-mgmt-grpc.nexora.svc.cluster.local") {
		t.Errorf("gRPC server names = %v", names)
	}
	if probe := mc.path("readinessProbe", "httpGet", "path"); probe != "/api/v1/health" {
		t.Errorf("mgmt readiness path = %v", probe)
	}
	if init := mgmt.path("spec", "template", "spec", "initContainers").([]any)[0].(map[string]any); init["args"].([]any)[0] != "migrate" {
		t.Errorf("migrate init container = %v", init)
	}
	if lb := find(t, kw, "Service", "nexora-mgmt-lb"); lb.path("spec", "loadBalancerIP") != "192.168.10.135" || len(lb.path("spec", "ports").([]any)) != 1 {
		t.Errorf("gRPC load balancer = %v", lb["spec"])
	}
	find(t, kw, "Service", "nexora-mgmt-grpc")
	ing := find(t, kw, "Ingress", "nexora")
	if ing.path("spec", "ingressClassName") != "nginx" || ing.path("metadata", "annotations", "cert-manager.io/cluster-issuer") != "cluster-ca" ||
		ing.path("spec", "tls").([]any)[0].(map[string]any)["secretName"] != "nexora-ingress-tls" {
		t.Errorf("ingress = %v", ing)
	}

	for _, g := range []struct{ workload, service, ip, policy, prefix, token, op string }{
		{"nexora-engine", "nexora-dns", "192.168.10.136", "Local", "", "nexora-join-token", "NotIn"},
		{"nexora-engine-edge-b", "nexora-dns-edge-b", "192.168.10.137", "Cluster", "edge-b-", "nexora-join-token-edge-b", "In"},
	} {
		ds := find(t, kw, "DaemonSet", g.workload)
		spec := obj(ds.path("spec", "template", "spec").(map[string]any))
		term := spec.path("affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)[0].(map[string]any)
		expr := term["matchExpressions"].([]any)[0].(map[string]any)
		if expr["key"] != "nexora.io/engine-group" || expr["operator"] != g.op {
			t.Errorf("%s node affinity = %v", g.workload, expr)
		}
		ec := container(t, ds, "engine")
		if nn := env(ec, "NEXORA_ENGINE_NODE_NAME"); nn == nil || nn["value"] != g.prefix+"$(K8S_NODE_NAME)" {
			t.Errorf("%s node name env = %v", g.workload, nn)
		}
		var hostPath, tokenSecret any
		for _, v := range spec["volumes"].([]any) {
			m := obj(v.(map[string]any))
			switch m["name"] {
			case "state":
				hostPath = m.path("hostPath", "path")
			case "join":
				tokenSecret = m.path("secret", "secretName")
			}
		}
		if hostPath != "/var/lib/nexora/"+g.workload || tokenSecret != g.token {
			t.Errorf("%s state %v join secret %v", g.workload, hostPath, tokenSecret)
		}
		svc := find(t, kw, "Service", g.service)
		if svc.path("spec", "loadBalancerIP") != g.ip || svc.path("spec", "externalTrafficPolicy") != g.policy {
			t.Errorf("%s = %v", g.service, svc["spec"])
		}
		if len(svc.path("spec", "ports").([]any)) != 5 {
			t.Errorf("%s ports = %v, want dns-udp, dns-tcp, dot, doq, doh", g.service, svc.path("spec", "ports"))
		}
	}
	// The second DNS server address serves the same default group and keeps client addresses.
	second := find(t, kw, "Service", "nexora-dns-2")
	if second.path("spec", "loadBalancerIP") != "192.168.10.139" || second.path("spec", "externalTrafficPolicy") != "Local" ||
		second.path("spec", "selector", "nexora.io/engine-group") != "default" || len(second.path("spec", "ports").([]any)) != 5 {
		t.Errorf("nexora-dns-2 = %v", second["spec"])
	}
	find(t, kw, "Service", "nexora-engine-metrics")
	if has(kw, "Cluster") {
		t.Error("kw uses its existing CNPG cluster (external database mode)")
	}
	sm := find(t, kw, "ServiceMonitor", "nexora")
	if sm.path("metadata", "namespace") != "monitoring" || sm.path("metadata", "labels", "release") != "kps" {
		t.Errorf("service monitor metadata = %v", sm["metadata"])
	}
	rule, _ := yaml.Marshal(find(t, kw, "PrometheusRule", "nexora"))
	for _, want := range []string{"max(nexora_mgmt_engines_disconnected", `nexora_mgmt_rollouts{state="halted",namespace="nexora"}`, "NexoraManagementPlaneDown"} {
		if !strings.Contains(string(rule), want) {
			t.Errorf("prometheus rule lacks %q:\n%s", want, rule)
		}
	}

	cnpg := render(t, "--set", "mgmt.ca.existingSecret=ca", "--api-versions", "postgresql.cnpg.io/v1",
		"--set", "engine.kind=Deployment", "--set-json", `engine.groups=[{"name":"default","replicas":2,"joinTokenSecret":"jt"}]`)
	if c := find(t, cnpg, "Cluster", "nexora-db"); c.path("spec", "instances") != 2 {
		t.Errorf("cnpg cluster = %v", c["spec"])
	}
	if d := find(t, cnpg, "Deployment", "nexora-engine-default"); d.path("spec", "replicas") != 2 {
		t.Errorf("engine deployment replicas = %v", d.path("spec", "replicas"))
	}
	if has(cnpg, "ServiceMonitor") || has(cnpg, "Ingress") {
		t.Error("disabled monitoring and ingress must not render")
	}
	// The secret volumes are mode 0440, so the mgmt pod must run with the group that fsGroup grants.
	if sc := find(t, kw, "Deployment", "nexora-mgmt").path("spec", "template", "spec", "securityContext").(map[string]any); sc["runAsGroup"] != 65532 || sc["fsGroup"] != 65532 {
		t.Errorf("mgmt pod security context = %v", sc)
	}
	if ds := find(t, kw, "DaemonSet", "nexora-engine"); ds.path("spec", "template", "spec", "hostNetwork") != nil {
		t.Errorf("hostNetwork must be off by default: %v", ds.path("spec", "template", "spec", "hostNetwork"))
	}
	if env(container(t, find(t, cnpg, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_OTLP_ENDPOINT") != nil {
		t.Error("without a collector or mgmt.otlpEndpoint, mgmt must not get an OTLP endpoint")
	}
	for _, d := range cnpg {
		if d.path("metadata", "name") == "nexora-otelcol" {
			t.Errorf("the collector is optional and off by default, rendered %v", d["kind"])
		}
	}

	extra := render(t, "--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg",
		"--set", "engine.hostNetwork=true", "--set", "otelCollector.enabled=true", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`)
	hn := obj(find(t, extra, "DaemonSet", "nexora-engine-default").path("spec", "template", "spec").(map[string]any))
	if hn["hostNetwork"] != true || hn["dnsPolicy"] != "ClusterFirstWithHostNet" || hn.path("securityContext", "sysctls") != nil {
		t.Errorf("hostNetwork engine pod spec = %v", hn)
	}
	if ref := env(container(t, find(t, extra, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_DATABASE_URL").path("valueFrom", "secretKeyRef"); ref == nil || ref.(map[string]any)["name"] != "pg" || ref.(map[string]any)["key"] != "uri" {
		t.Errorf("external database env = %v", ref)
	}
	find(t, extra, "Deployment", "nexora-otelcol")
	if conf, ok := find(t, extra, "ConfigMap", "nexora-otelcol").path("data", "config.yaml").(string); !ok || !strings.Contains(conf, "0.0.0.0:4317") {
		t.Errorf("collector config must be a string holding the OTLP receiver, got %v", conf)
	}
	find(t, extra, "Service", "nexora-otelcol")
	if e := env(container(t, find(t, extra, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_OTLP_ENDPOINT"); e == nil || e["value"] != "http://nexora-otelcol.nexora.svc.cluster.local:4317" {
		t.Errorf("mgmt OTLP endpoint with the bundled collector = %v", e)
	}

	for _, bad := range []struct {
		args []string
		want string
	}{
		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "mgmt.replicas=0"}, "replicas"},
		{[]string{"--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}, "mgmt.ca.existingSecret"},
		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "engine.kind=StatefulSet"}, "kind"},
		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}, "postgresql.cnpg.io/v1"},
		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg"}, "joinTokenSecret is required"},
	} {
		out, err := helm(append([]string{"template", "nexora", chartDir}, bad.args...)...)
		if err == nil || !strings.Contains(out, bad.want) {
			t.Errorf("helm template %v: err=%v, want failure mentioning %q; output:\n%s", bad.args, err, bad.want, out)
		}
	}
}
