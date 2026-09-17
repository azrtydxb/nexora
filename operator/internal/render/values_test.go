package render_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/render"
)

func jsonNormal(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func dropMetricsNamespaces(m map[string]any) {
	metrics, _ := m["metrics"].(map[string]any)
	for _, k := range []string{"serviceMonitor", "prometheusRule"} {
		if sub, ok := metrics[k].(map[string]any); ok {
			delete(sub, "namespace")
		}
	}
}

func TestValuesFromKwEquivalentInstallation(t *testing.T) {
	raw, err := os.ReadFile("testdata/kw-installation.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var inst v1alpha1.NexoraInstallation
	if err := yaml.UnmarshalStrict(raw, &inst); err != nil {
		t.Fatal(err)
	}
	v, err := render.BuildValues(inst.Spec, render.Injected{Tag: "sha-0000000", CASecret: "nexora-ca", KEKSecret: "nexora-kek",
		JoinTokenSecrets: map[string]string{"default": "nexora-join-token"}})
	if err != nil || len(v.PendingGroups) != 0 {
		t.Fatalf("values: %v pending %v", err, v.PendingGroups)
	}
	kwRaw, err := os.ReadFile("../../../deploy/kw/values-kw.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var kw map[string]any
	if err := yaml.Unmarshal(kwRaw, &kw); err != nil {
		t.Fatal(err)
	}
	got, want := jsonNormal(t, v.Map), jsonNormal(t, kw)
	// Injected by the operator, absent from values-kw.yaml.
	delete(got["image"].(map[string]any), "tag")
	delete(got["mgmt"].(map[string]any), "bootstrapToken")
	dropMetricsNamespaces(got)
	dropMetricsNamespaces(want)
	if !reflect.DeepEqual(got, want) {
		gb, _ := yaml.Marshal(got)
		wb, _ := yaml.Marshal(want)
		t.Fatalf("values differ\n--- operator\n%s\n--- values-kw.yaml\n%s", gb, wb)
	}
}

func TestBuildValuesPendingGroupsAndTag(t *testing.T) {
	spec := v1alpha1.NexoraInstallationSpec{Engine: v1alpha1.EngineSpec{Groups: []v1alpha1.EngineGroupSpec{
		{Name: "default"}, {Name: "edge", EngineGroupRef: "edge-eg"}}}}
	v, err := render.BuildValues(spec, render.Injected{Tag: "sha-1", JoinTokenSecrets: map[string]string{"edge-eg": "edge-join-token"}})
	if err != nil {
		t.Fatal(err)
	}
	groups := v.Map["engine"].(map[string]any)["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["joinTokenSecret"] != "edge-join-token" || groups[0].(map[string]any)["engineGroupRef"] != nil {
		t.Fatalf("groups = %v", groups)
	}
	if !reflect.DeepEqual(v.PendingGroups, []string{"default"}) {
		t.Fatalf("pending = %v", v.PendingGroups)
	}
	none, _ := render.BuildValues(v1alpha1.NexoraInstallationSpec{}, render.Injected{Tag: "sha-1"})
	if g := none.Map["engine"].(map[string]any)["groups"].([]any); len(g) != 0 {
		t.Fatalf("no spec groups must render no chart default group: %v", g)
	}
	if _, err := render.BuildValues(spec, render.Injected{Tag: "dev"}); !errors.Is(err, render.ErrImageTagRequired) {
		t.Fatalf("dev operator without tag: %v", err)
	}
}

func TestAIAndMCPValuesDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, mgmt, enabled, readOnly, secret string
	}{
		{"omitted", "{}", "false", "true", ""},
		{"empty", "{ai: {}, mcp: {}}", "false", "true", ""},
		{"enabled-default-read-only", "{mcp: {enabled: true}}", "true", "true", ""},
		{"explicit-false", "{mcp: {enabled: false, readOnly: false}}", "false", "false", ""},
		{"enabled-writable", "{ai: {existingSecret: test-ai}, mcp: {enabled: true, readOnly: false}}", "true", "false", "test-ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var spec v1alpha1.NexoraInstallationSpec
			if err := yaml.UnmarshalStrict([]byte("mgmt: "+tc.mgmt), &spec); err != nil {
				t.Fatal(err)
			}
			copied := spec.DeepCopy()
			if spec.Mgmt.MCP.Enabled != nil && copied.Mgmt.MCP.Enabled == spec.Mgmt.MCP.Enabled {
				t.Fatal("deepcopy aliases enabled")
			}
			if spec.Mgmt.MCP.ReadOnly != nil && copied.Mgmt.MCP.ReadOnly == spec.Mgmt.MCP.ReadOnly {
				t.Fatal("deepcopy aliases readOnly")
			}
			vals, err := render.BuildValues(*copied, render.Injected{Tag: "test", CASecret: "ca"})
			if err != nil {
				t.Fatal(err)
			}
			mgmt := vals.Map["mgmt"].(map[string]any)
			mcp, _ := mgmt["mcp"].(map[string]any)
			for key, ptr := range map[string]*bool{"enabled": spec.Mgmt.MCP.Enabled, "readOnly": spec.Mgmt.MCP.ReadOnly} {
				value, present := mcp[key]
				if ptr == nil && present || ptr != nil && (!present || value != *ptr) {
					t.Fatalf("%s = %v (present %v), input %v", key, value, present, ptr)
				}
			}
			if tc.secret == "" {
				if _, ok := mgmt["ai"]; ok {
					t.Fatal("unset AI overrides chart default")
				}
			}
			objs, err := load(t).Render(target, vals.Map)
			if err != nil {
				t.Fatal(err)
			}
			var env []any
			for _, obj := range objs {
				if obj.GetKind() == "Deployment" && obj.GetName() == "nexora-mgmt" {
					containers := obj.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
					env = containers[0].(map[string]any)["env"].([]any)
				}
			}
			got := map[string]map[string]any{}
			for _, item := range env {
				e := item.(map[string]any)
				got[e["name"].(string)] = e
			}
			for name, want := range map[string]string{"NEXORA_MCP_ENABLED": tc.enabled, "NEXORA_MCP_READ_ONLY": tc.readOnly} {
				if got[name]["value"] != want {
					t.Errorf("%s = %v, want %s", name, got[name], want)
				}
			}
			for name, key := range map[string]string{"NEXORA_AI_BASE_URL": "base-url", "NEXORA_AI_MODEL": "model", "NEXORA_AI_API_KEY": "api-key"} {
				e, present := got[name]
				if tc.secret == "" {
					if present {
						t.Errorf("unexpected %s", name)
					}
					continue
				}
				want := map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": tc.secret, "key": key, "optional": true}}}
				if !reflect.DeepEqual(e, want) {
					t.Errorf("%s = %v, want %v", name, e, want)
				}
			}
		})
	}
}
