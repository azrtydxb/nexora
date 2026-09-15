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
