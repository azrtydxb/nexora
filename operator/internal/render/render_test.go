package render_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/render"
)

const chartDir = "../../../deploy/helm/nexora"

var target = render.Target{Name: "nexora", Namespace: "nexora", KubeVersion: "v1.34.4",
	APIVersions: []string{"postgresql.cnpg.io/v1", "monitoring.coreos.com/v1"}}

func load(t *testing.T) *render.Chart {
	t.Helper()
	c, err := render.LoadChart(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func valuesFile(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			switch k {
			case "app.kubernetes.io/managed-by":
				out[k] = "X"
			case "nexora.io/installation", "ownerReferences":
			default:
				out[k] = normalize(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalize(x[i])
		}
		return out
	}
	return v
}

func byKey(t *testing.T, objs []map[string]any) map[string]any {
	out := map[string]any{}
	for _, o := range objs {
		md := o["metadata"].(map[string]any)
		md["namespace"] = "nexora" // helm template omits it for most templates
		out[o["kind"].(string)+"/"+md["name"].(string)] = normalize(o)
	}
	return out
}

func TestRenderMatchesHelmTemplate(t *testing.T) {
	c := load(t)
	for _, vf := range []string{"testdata/cnpg-backup-values.yaml", "testdata/hostnetwork-values.yaml", "../../../deploy/kw/values-kw.yaml"} {
		t.Run(filepath.Base(vf), func(t *testing.T) {
			vals := valuesFile(t, vf)
			objs, err := c.Render(target, vals)
			if err != nil {
				t.Fatal(err)
			}
			var mine []map[string]any
			for _, o := range objs {
				mine = append(mine, o.Object)
			}
			out, err := exec.Command("helm", "template", "nexora", chartDir, "--namespace", "nexora", "-f", vf,
				"--api-versions", "postgresql.cnpg.io/v1", "--api-versions", "monitoring.coreos.com/v1").CombinedOutput()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, out)
			}
			var theirs []map[string]any
			for _, doc := range strings.Split(string(out), "\n---\n") {
				var m map[string]any
				if err := yaml.Unmarshal([]byte(doc), &m); err == nil && m != nil {
					theirs = append(theirs, m)
				}
			}
			a, b := byKey(t, mine), byKey(t, theirs)
			if !reflect.DeepEqual(a, b) {
				for k := range b {
					if !reflect.DeepEqual(a[k], b[k]) {
						t.Errorf("%s differs from helm template", k)
					}
				}
				t.Fatalf("operator render has %d objects, helm template %d", len(a), len(b))
			}
		})
	}
}

func TestRenderAddsOwnershipExceptRetainedKinds(t *testing.T) {
	objs, err := load(t).Render(target, valuesFile(t, "testdata/cnpg-backup-values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	owner := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Name: "nexora", Namespace: "nexora", UID: "u-1"}}
	if err := render.Own(objs, owner, v1alpha1.GroupVersion.WithKind("NexoraInstallation")); err != nil {
		t.Fatal(err)
	}
	var sawCluster bool
	for _, o := range objs {
		if o.GetLabels()["nexora.io/installation"] != "nexora" {
			t.Errorf("%s/%s lacks the installation label", o.GetKind(), o.GetName())
		}
		refs := o.GetOwnerReferences()
		if render.Retained(o.GroupVersionKind()) {
			sawCluster = true
			if len(refs) != 0 {
				t.Errorf("retained %s has owners %v", o.GetName(), refs)
			}
			continue
		}
		if len(refs) != 1 || refs[0].UID != "u-1" || refs[0].Controller == nil || !*refs[0].Controller {
			t.Errorf("%s/%s owners = %v", o.GetKind(), o.GetName(), refs)
		}
	}
	if !sawCluster || !render.Retained(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}) {
		t.Fatal("the CNPG Cluster must be rendered and retained")
	}
}

func TestRenderRejectsForeignNamespace(t *testing.T) {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion("monitoring.coreos.com/v1")
	o.SetKind("ServiceMonitor")
	o.SetName("nexora")
	o.SetNamespace("monitoring")
	owner := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Name: "nexora", Namespace: "nexora"}}
	err := render.Own([]*unstructured.Unstructured{o}, owner, v1alpha1.GroupVersion.WithKind("NexoraInstallation"))
	if !errors.Is(err, render.ErrForeignNamespace) || !strings.Contains(err.Error(), "ServiceMonitor/nexora in monitoring") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderChartFailureIsReported(t *testing.T) {
	vals := valuesFile(t, "testdata/hostnetwork-values.yaml")
	vals["engine"].(map[string]any)["groups"] = []any{map[string]any{"name": "default"}}
	if _, err := load(t).Render(target, vals); err == nil || !strings.Contains(err.Error(), "joinTokenSecret is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderCapabilities(t *testing.T) {
	noCNPG := target
	noCNPG.APIVersions = nil
	if _, err := load(t).Render(noCNPG, valuesFile(t, "testdata/cnpg-backup-values.yaml")); err == nil ||
		!strings.Contains(err.Error(), "database.mode=cnpg needs the CloudNativePG operator") {
		t.Fatalf("err = %v", err)
	}
}
