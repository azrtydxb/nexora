package v1alpha1_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/envtestutil"
)

func loadInstallation(t *testing.T, path string) *v1alpha1.NexoraInstallation {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var in v1alpha1.NexoraInstallation
	if err := yaml.UnmarshalStrict(b, &in); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &in
}

func i32(v int32) *int32 { return &v }
func yes() *bool         { b := true; return &b }

func TestInstallationCRDValidation(t *testing.T) {
	_, c := envtestutil.Start(t)
	ctx := context.Background()
	root := envtestutil.RepoRoot(t)
	for i, p := range []string{
		filepath.Join(root, "operator/config/samples/nexora_v1alpha1_nexorainstallation.yaml"),
		filepath.Join(root, "operator/internal/render/testdata/kw-installation.yaml"),
	} {
		in := loadInstallation(t, p)
		in.Namespace, in.Name = "default", "valid-"+string(rune('a'+i))
		want := in.DeepCopy()
		if err := c.Create(ctx, in); err != nil {
			t.Fatalf("valid %s refused: %v", p, err)
		}
		// Fetch persisted fixtures so schema pruning cannot silently discard chart settings.
		var got v1alpha1.NexoraInstallation
		if err := c.Get(ctx, client.ObjectKeyFromObject(in), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Spec.Mgmt.AI, want.Spec.Mgmt.AI) || !reflect.DeepEqual(got.Spec.Mgmt.MCP, want.Spec.Mgmt.MCP) {
			t.Fatalf("AI/MCP fields changed on persistence: got %+v/%+v, want %+v/%+v", got.Spec.Mgmt.AI, got.Spec.Mgmt.MCP, want.Spec.Mgmt.AI, want.Spec.Mgmt.MCP)
		}
	}
	for _, enabled := range []bool{false, true} {
		in := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "mcp-false"}}
		if enabled {
			in.Name = "mcp-true"
		}
		readOnly := false
		in.Spec.Mgmt.MCP = v1alpha1.MCPSpec{Enabled: &enabled, ReadOnly: &readOnly}
		want := in.Spec.Mgmt.MCP.DeepCopy()
		if err := c.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
		var got v1alpha1.NexoraInstallation
		if err := c.Get(ctx, client.ObjectKeyFromObject(in), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Spec.Mgmt.MCP, *want) {
			t.Fatalf("explicit MCP booleans changed: %+v", got.Spec.Mgmt.MCP)
		}
	}
	base := func(name string) *v1alpha1.NexoraInstallation {
		return &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
	}
	cases := []struct {
		name, want string
		mutate     func(*v1alpha1.NexoraInstallation)
	}{
		{"external-no-secret", "database.external.existingSecret is required", func(in *v1alpha1.NexoraInstallation) { in.Spec.Database.Mode = "external" }},
		{"instance-no-node", "node", func(in *v1alpha1.NexoraInstallation) {
			in.Spec.Engine.Groups = []v1alpha1.EngineGroupSpec{{Name: "default", Instances: []v1alpha1.EngineInstanceSpec{{Name: "a"}}}}
		}},
		{"duplicate-instance", "Duplicate value", func(in *v1alpha1.NexoraInstallation) {
			in.Spec.Engine.Groups = []v1alpha1.EngineGroupSpec{{Name: "default", Instances: []v1alpha1.EngineInstanceSpec{{Name: "a", Node: "n1"}, {Name: "a", Node: "n2"}}}}
		}},
		{"statefulset", "Unsupported value", func(in *v1alpha1.NexoraInstallation) { in.Spec.Engine.Kind = "StatefulSet" }},
		{"opensearch-no-url", "mgmt.querylog.opensearch.url is required", func(in *v1alpha1.NexoraInstallation) { in.Spec.Mgmt.Querylog.Backend = "opensearch" }},
		{"backup-no-path", "database.cnpg.backup needs destinationPath", func(in *v1alpha1.NexoraInstallation) { in.Spec.Database.CNPG.Backup.Enabled = yes() }},
	}
	for _, tc := range cases {
		in := base(tc.name)
		tc.mutate(in)
		err := c.Create(ctx, in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

func TestEngineGroupCRDValidation(t *testing.T) {
	_, c := envtestutil.Start(t)
	ctx := context.Background()
	d := func(s string) *metav1.Duration { v, _ := time.ParseDuration(s); return &metav1.Duration{Duration: v} }
	eg := func(name string) *v1alpha1.NexoraEngineGroup {
		return &v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
			Spec: v1alpha1.NexoraEngineGroupSpec{InstallationRef: v1alpha1.LocalRef{Name: "nexora"}, GroupName: name}}
	}
	good := eg("edge")
	if err := c.Create(ctx, good); err != nil {
		t.Fatalf("valid engine group refused: %v", err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*v1alpha1.NexoraEngineGroup)
	}{
		{"short-ttl", "joinToken.ttl must be between 1m and 8760h", func(g *v1alpha1.NexoraEngineGroup) { g.Spec.JoinToken.TTL = d("30s") }},
		{"renew-too-long", "joinToken.renewBefore must be less than joinToken.ttl", func(g *v1alpha1.NexoraEngineGroup) {
			g.Spec.JoinToken.TTL, g.Spec.JoinToken.RenewBefore = d("1h"), d("1h")
		}},
		{"bad-name", "spec.groupName", func(g *v1alpha1.NexoraEngineGroup) { g.Spec.GroupName = "Edge_B" }},
	} {
		g := eg(tc.name)
		tc.mutate(g)
		if err := c.Create(ctx, g); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	moved := good.DeepCopy()
	moved.Spec.GroupName = "edge2"
	if err := c.Update(ctx, moved); err == nil || !strings.Contains(err.Error(), "groupName is immutable") {
		t.Errorf("groupName change: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(good), good); err != nil {
		t.Fatal(err)
	}
	good.Spec.InstallationRef.Name = "other"
	if err := c.Update(ctx, good); err == nil || !strings.Contains(err.Error(), "installationRef is immutable") {
		t.Errorf("installationRef change: %v", err)
	}
}
