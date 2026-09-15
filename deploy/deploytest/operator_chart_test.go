package deploytest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const operatorChartDir = "../helm/nexora-operator"

func renderOperator(t *testing.T, args ...string) []obj {
	t.Helper()
	return renderChartDocs(t, append([]string{"template", "nexora-operator", operatorChartDir, "--namespace", "nexora-operator"}, args...)...)
}

func renderChartDocs(t *testing.T, args ...string) []obj {
	t.Helper()
	out, err := helm(args...)
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", args, err, out)
	}
	return decodeDocs(t, out)
}

// decodeDocs splits helm template output into its YAML documents (the loop of render in helm_test.go).
func decodeDocs(t *testing.T, out string) []obj {
	t.Helper()
	var docs []obj
	dec := yaml.NewDecoder(bytes.NewBufferString(out))
	for {
		// Decode into the unnamed map type so nested mappings are map[string]any.
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

func findNS(t *testing.T, docs []obj, kind, ns, name string) obj {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && d.path("metadata", "name") == name && d.path("metadata", "namespace") == ns {
			return d
		}
	}
	t.Fatalf("%s %s/%s not rendered", kind, ns, name)
	return nil
}

func args(t *testing.T, d obj) string {
	c := container(t, d, "operator")
	var s []string
	for _, a := range c["args"].([]any) {
		s = append(s, a.(string))
	}
	return strings.Join(s, " ")
}

func TestOperatorChart(t *testing.T) {
	if out, err := helm("lint", operatorChartDir, "--strict"); err != nil {
		t.Fatalf("helm lint: %v\n%s", err, out)
	}
	cluster := renderOperator(t, "--set", "image.tag=sha-1234567")
	find(t, cluster, "ClusterRole", "nexora-operator")
	find(t, cluster, "ClusterRoleBinding", "nexora-operator")
	d := find(t, cluster, "Deployment", "nexora-operator")
	c := container(t, d, "operator")
	if c["image"] != "192.168.10.131/azrtydxb/nexora-operator:sha-1234567" {
		t.Errorf("image = %v", c["image"])
	}
	if a := args(t, d); !strings.Contains(a, "--chart-dir=/charts/nexora") || strings.Contains(a, "--watch-namespaces") {
		t.Errorf("cluster args = %s", a)
	}
	sc := obj(c["securityContext"].(map[string]any))
	if sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false || sc.path("capabilities", "drop").([]any)[0] != "ALL" {
		t.Errorf("container security = %v", sc)
	}
	if d.path("spec", "template", "spec", "securityContext", "runAsNonRoot") != true {
		t.Error("pod must run as non-root")
	}
	findNS(t, cluster, "Role", "nexora-operator", "nexora-operator-leader-election")
	if has(cluster, "Namespace") {
		t.Error("the Namespace renders without createNamespace")
	}

	scoped := renderOperator(t, "--set", "rbac.scope=namespace", "--set-json", `watchNamespaces=["a","b"]`)
	if has(scoped, "ClusterRole") || has(scoped, "ClusterRoleBinding") {
		t.Error("namespace scope renders cluster RBAC")
	}
	for _, ns := range []string{"a", "b"} {
		findNS(t, scoped, "Role", ns, "nexora-operator")
		rb := findNS(t, scoped, "RoleBinding", ns, "nexora-operator")
		if s := rb.path("subjects").([]any)[0].(map[string]any); s["namespace"] != "nexora-operator" || s["name"] != "nexora-operator" {
			t.Errorf("role binding subject in %s = %v", ns, s)
		}
	}
	if a := args(t, find(t, scoped, "Deployment", "nexora-operator")); !strings.Contains(a, "--watch-namespaces=a,b") {
		t.Errorf("namespace args = %s", a)
	}
	if out, err := helm("template", "x", operatorChartDir, "--set", "rbac.scope=namespace"); err == nil || !strings.Contains(out, "watchNamespaces") {
		t.Errorf("namespace scope without namespaces must fail: %v %s", err, out)
	}
}

func TestOperatorManifestsMatchChart(t *testing.T) {
	want, err := helm("template", "nexora-operator", operatorChartDir, "--namespace", "nexora-operator", "--set", "rbac.scope=cluster", "--set", "createNamespace=true")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, want)
	}
	got, err := os.ReadFile("../operator/operator.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Error("deploy/operator/operator.yaml is stale; run make operator-generate")
	}
	crds, _ := filepath.Glob("../operator/crds/*.yaml")
	if len(crds) != 2 {
		t.Fatalf("crds = %v", crds)
	}
	for _, p := range crds {
		a, _ := os.ReadFile(p)
		b, err := os.ReadFile(filepath.Join(operatorChartDir, "crds", filepath.Base(p)))
		if err != nil || string(a) != string(b) {
			t.Errorf("chart CRD %s differs from deploy/operator/crds: %v", filepath.Base(p), err)
		}
	}
}
