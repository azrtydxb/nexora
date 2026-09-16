package deploytest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestHelmKwPairedRenderGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/kw-pairs-render.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := helm("template", "nexora", chartDir, "--namespace", "nexora", "-f", "../kw/values-kw.yaml", "-f", "../kw/values-pairs.yaml", "--api-versions", "monitoring.coreos.com/v1", "--set", "image.tag=golden")
	if err != nil || got != string(want) {
		t.Fatalf("paired raw rendering differs from golden: %v", err)
	}
}

func failoverGroup() map[string]any {
	return map[string]any{
		"name": "default", "workloadName": "nexora-engine", "joinTokenSecret": "nexora-join-token",
		"instances": []map[string]any{
			{"name": "a", "node": "master-12"},
			{"name": "b", "node": "master-13"},
			{"name": "c", "node": "master-11", "stateDirName": "nexora-engine-c", "nodeNamePrefix": "c-"},
			{"name": "d", "node": "master-11", "stateDirName": "nexora-engine-d", "nodeNamePrefix": "d-"},
		},
		"failoverPairs": []map[string]any{
			{"name": "dns136", "members": []string{"a", "c"}, "service": map[string]any{"name": "nexora-dns", "loadBalancerIP": "192.168.10.136"}},
			{"name": "dns139", "members": []string{"b", "d"}, "service": map[string]any{"name": "nexora-dns-2", "loadBalancerIP": "192.168.10.139"}},
		},
	}
}

func failoverArgs(t *testing.T, group map[string]any) []string {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{group})
	if err != nil {
		t.Fatal(err)
	}
	return []string{"-f", "../kw/values-kw.yaml", "--api-versions", "monitoring.coreos.com/v1", "--set", "engine.pairedRollout.enabled=true", "--set-json", "engine.groups=" + string(raw)}
}

func TestHelmFailoverPairs(t *testing.T) {
	args := failoverArgs(t, failoverGroup())
	for _, selected := range []string{"", "a", "b", "c", "d"} {
		t.Run("rolling-"+selected, func(t *testing.T) {
			workload := ""
			if selected != "" {
				workload = "nexora-engine-" + selected
			}
			docs := render(t, append(append([]string{}, args...), "--set", "engine.pairedRollout.workload="+workload)...)
			for _, member := range []struct{ name, pair, state, prefix string }{
				{"a", "dns136", "nexora-engine", ""}, {"b", "dns139", "nexora-engine", ""},
				{"c", "dns136", "nexora-engine-c", "c-"}, {"d", "dns139", "nexora-engine-d", "d-"},
			} {
				ds := find(t, docs, "DaemonSet", "nexora-engine-"+member.name)
				if got := ds.path("spec", "template", "metadata", "labels", "nexora.io/failover-pair"); got != member.pair {
					t.Errorf("%s pair = %v", member.name, got)
				}
				want := "OnDelete"
				if member.name == selected {
					want = "RollingUpdate"
				}
				if got := ds.path("spec", "updateStrategy", "type"); got != want {
					t.Errorf("%s update strategy = %v, want %s", member.name, got, want)
				}
				if got := env(container(t, ds, "engine"), "NEXORA_ENGINE_NODE_NAME")["value"]; got != member.prefix+"$(K8S_NODE_NAME)" {
					t.Errorf("%s engine name = %v", member.name, got)
				}
				for _, v := range ds.path("spec", "template", "spec", "volumes").([]any) {
					volume := obj(v.(map[string]any))
					if volume["name"] == "state" && volume.path("hostPath", "path") != "/var/lib/nexora/"+member.state {
						t.Errorf("%s state = %v", member.name, volume)
					}
				}
			}
			for _, p := range []struct{ name, service, ip string }{{"dns136", "nexora-dns", "192.168.10.136"}, {"dns139", "nexora-dns-2", "192.168.10.139"}} {
				svc := find(t, docs, "Service", p.service)
				if svc.path("spec", "loadBalancerIP") != p.ip || svc.path("spec", "externalTrafficPolicy") != "Local" || svc.path("spec", "selector", "nexora.io/failover-pair") != p.name || svc.path("spec", "selector", "nexora.io/engine-instance") != nil {
					t.Errorf("%s does not select only its pair with preserved source IPs: %v", p.service, svc)
				}
				budget := find(t, docs, "PodDisruptionBudget", "nexora-engine-"+p.name)
				if budget.path("spec", "minAvailable") != 1 || budget.path("spec", "selector", "matchLabels", "nexora.io/failover-pair") != p.name {
					t.Errorf("%s budget = %v", p.name, budget)
				}
			}
		})
	}
}

func TestHelmFailoverMigrationKeepsExistingSelectors(t *testing.T) {
	args := failoverArgs(t, failoverGroup())
	docs := render(t, append(args, "--set", "engine.pairedRollout.legacySelectors=true")...)
	for service, instance := range map[string]string{"nexora-dns": "a", "nexora-dns-2": "b"} {
		svc := find(t, docs, "Service", service)
		if svc.path("spec", "selector", "nexora.io/engine-instance") != instance || svc.path("spec", "selector", "nexora.io/failover-pair") != nil {
			t.Errorf("migration changed existing endpoint selection: %v", svc)
		}
	}
}

func TestHelmFailoverRolloutRejectsUnknownWorkload(t *testing.T) {
	args := append(failoverArgs(t, failoverGroup()), "--set", "engine.pairedRollout.workload=missing")
	out, err := helm(append([]string{"template", "nexora", chartDir}, args...)...)
	if err == nil || !strings.Contains(out, "unknown workload") {
		t.Fatalf("unknown rollout target: err=%v output=%s", err, out)
	}
}

func TestHelmFailoverPairsRejectUnsafeTopology(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown member", func(g map[string]any) { g["failoverPairs"].([]map[string]any)[0]["members"] = []string{"a", "missing"} }},
		{"duplicate member", func(g map[string]any) { g["failoverPairs"].([]map[string]any)[1]["members"] = []string{"a", "d"} }},
		{"same node", func(g map[string]any) { g["instances"].([]map[string]any)[2]["node"] = "master-12" }},
		{"shared state", func(g map[string]any) { g["instances"].([]map[string]any)[3]["stateDirName"] = "nexora-engine-c" }},
		{"unsafe state path", func(g map[string]any) { g["instances"].([]map[string]any)[2]["stateDirName"] = "../other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := failoverGroup()
			tc.edit(group)
			out, err := helm(append([]string{"template", "nexora", chartDir}, failoverArgs(t, group)...)...)
			if err == nil || (!strings.Contains(out, "failover") && !strings.Contains(out, "stateDirName")) {
				t.Fatalf("unsafe topology must fail with a failover diagnostic: err=%v output=%s", err, out)
			}
		})
	}
}
