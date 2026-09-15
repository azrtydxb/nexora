package installation_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/internal/envtestutil"
	"github.com/piwi3910/nexora/operator/internal/render"
)

func manifestDocs(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(envtestutil.RepoRoot(t), "deploy/operator/operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	var out []*unstructured.Unstructured
	for {
		doc, err := r.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &u.Object); err != nil || len(u.Object) == 0 {
			continue
		}
		out = append(out, u)
	}
	return out
}

func TestOperatorRBACCoversManagedKinds(t *testing.T) {
	var role rbacv1.ClusterRole
	for _, u := range manifestDocs(t) {
		if u.GetKind() == "ClusterRole" && u.GetName() == "nexora-operator" {
			b, _ := yaml.Marshal(u.Object)
			if err := yaml.Unmarshal(b, &role); err != nil {
				t.Fatal(err)
			}
		}
	}
	allows := func(group, resource string, verbs ...string) bool {
		have := map[string]bool{}
		for _, rule := range role.Rules {
			for _, g := range rule.APIGroups {
				for _, res := range rule.Resources {
					if g == group && res == resource {
						for _, v := range rule.Verbs {
							have[v] = true
						}
					}
				}
			}
		}
		for _, v := range verbs {
			if !have[v] {
				return false
			}
		}
		return true
	}
	for _, gvk := range render.ManagedKinds {
		res := strings.ToLower(gvk.Kind) + "s"
		if gvk.Kind == "Ingress" {
			res = "ingresses"
		}
		if !allows(gvk.Group, res, "get", "list", "watch", "create", "patch", "delete") {
			t.Errorf("RBAC lacks %s/%s", gvk.Group, res)
		}
	}
	for _, want := range [][2]string{{"", "secrets"}, {"nexora.io", "nexorainstallations/status"}, {"nexora.io", "nexoraenginegroups/finalizers"}} {
		if !allows(want[0], want[1], "update") {
			t.Errorf("RBAC lacks update on %s/%s", want[0], want[1])
		}
	}
	if !allows("", "events", "create", "patch") {
		t.Error("RBAC lacks events")
	}
}

func TestOperatorManifestsApplyToAPIServer(t *testing.T) {
	_, c := envtestutil.Start(t)
	ctx := context.Background()
	for _, u := range manifestDocs(t) {
		if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner("test"), client.ForceOwnership); err != nil {
			t.Errorf("apply %s/%s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}
