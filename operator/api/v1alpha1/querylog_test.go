package v1alpha1_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/envtestutil"
)

func querylogFixtures() map[string]string {
	return map[string]string{
		"clickhouse": `{"backend":"clickhouse","builtinCapacity":321,"opensearch":{"url":"https://search.example","index":"old-*"},"clickhouse":{"url":"https://clickhouse.example:8443","database":"analytics","table":"queries","username":"reader","passwordSecret":{"name":"clickhouse-auth","key":"credential"}}}`,
		"loki":       `{"backend":"loki","loki":{"url":"https://loki.example","selector":"{app=\"dns\"}","tenant":"team","username":"reader","passwordSecret":{"name":"loki-auth","key":"credential"},"lookback":"24h"}}`,
	}
}

func TestQuerylogStrictDecoding(t *testing.T) {
	for name, fixture := range querylogFixtures() {
		t.Run(name, func(t *testing.T) {
			var got v1alpha1.QuerylogSpec
			if err := yaml.UnmarshalStrict([]byte(fixture), &got); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(got.DeepCopy())
			if err != nil {
				t.Fatal(err)
			}
			var want, actual map[string]any
			if err := json.Unmarshal([]byte(fixture), &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &actual); err != nil {
				t.Fatal(err)
			}
			// Value structs serialize absent blocks as empty objects.
			var prune func(map[string]any)
			prune = func(m map[string]any) {
				for k, v := range m {
					if child, ok := v.(map[string]any); ok {
						prune(child)
						if len(child) == 0 {
							delete(m, k)
						}
					}
				}
			}
			prune(actual)
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("round trip: got %s, want %s", b, fixture)
			}
		})
	}
	for _, backend := range []string{"clickhouse", "loki"} {
		for _, field := range []string{"password", "passwordFile", "unknown"} {
			var got v1alpha1.QuerylogSpec
			if err := yaml.UnmarshalStrict([]byte(`{"`+backend+`":{"`+field+`":"forbidden"}}`), &got); err == nil {
				t.Errorf("accepted %s.%s", backend, field)
			}
		}
	}
}

func TestQuerylogCRDSchema(t *testing.T) {
	root := envtestutil.RepoRoot(t)
	var previous []byte
	for _, dir := range []string{"deploy/operator/crds", "deploy/helm/nexora-operator/crds"} {
		b, err := os.ReadFile(filepath.Join(root, dir, "nexora.io_nexorainstallations.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && string(previous) != string(b) {
			t.Fatal("CRD copies differ")
		}
		previous = b
		var crd map[string]any
		if err := yaml.Unmarshal(b, &crd); err != nil {
			t.Fatal(err)
		}
		schema := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
		for _, key := range []string{"spec", "mgmt", "querylog"} {
			schema = schema["properties"].(map[string]any)[key].(map[string]any)
		}
		props := schema["properties"].(map[string]any)
		enum := props["backend"].(map[string]any)["enum"]
		if !reflect.DeepEqual(enum, []any{"builtin", "opensearch", "clickhouse", "loki"}) {
			t.Fatalf("enum: %v", enum)
		}
		for backend, fields := range map[string][]string{
			"clickhouse": {"url", "database", "table", "username", "passwordSecret"},
			"loki":       {"url", "selector", "tenant", "username", "passwordSecret", "lookback"},
		} {
			backendProperties := props[backend].(map[string]any)["properties"].(map[string]any)
			if len(backendProperties) != len(fields) {
				t.Errorf("%s properties = %v", backend, backendProperties)
			}
			for _, field := range fields {
				if _, ok := backendProperties[field]; !ok {
					t.Errorf("missing %s.%s schema", backend, field)
				}
			}
			secret := backendProperties["passwordSecret"].(map[string]any)["properties"].(map[string]any)
			if len(secret) != 2 || secret["name"] == nil || secret["key"] == nil {
				t.Errorf("credential reference schema: %v", secret)
			}
			var check func(map[string]any)
			check = func(s map[string]any) {
				if _, ok := s["default"]; ok {
					t.Errorf("new %s setting has CRD default", backend)
				}
				if p, ok := s["properties"].(map[string]any); ok {
					for _, v := range p {
						check(v.(map[string]any))
					}
				}
			}
			check(props[backend].(map[string]any))
		}
	}
}

func TestQuerylogCRDPersistence(t *testing.T) {
	_, c := envtestutil.Start(t)
	ctx := context.Background()
	fixtures := querylogFixtures()
	fixtures["omitted"] = `{}`
	fixtures["builtin"] = `{"backend":"builtin"}`
	fixtures["opensearch"] = `{"backend":"opensearch","opensearch":{"url":"https://search.example"}}`
	fixtures["clickhouse-defaults"] = `{"backend":"clickhouse","clickhouse":{"url":"https://clickhouse.example","passwordSecret":{"name":"auth"}}}`
	fixtures["loki-defaults"] = `{"backend":"loki","loki":{"url":"https://loki.example"}}`
	for name, fixture := range fixtures {
		in := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		if err := yaml.UnmarshalStrict([]byte(fixture), &in.Spec.Mgmt.Querylog); err != nil {
			t.Fatal(err)
		}
		want := in.Spec.Mgmt.Querylog.DeepCopy()
		if err := c.Create(ctx, in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got v1alpha1.NexoraInstallation
		if err := c.Get(ctx, client.ObjectKeyFromObject(in), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*want, got.Spec.Mgmt.Querylog) {
			t.Errorf("%s: persisted %+v, want %+v", name, got.Spec.Mgmt.Querylog, *want)
		}
	}
	for i, tc := range []struct{ fixture, want string }{
		{`{"backend":"unknown"}`, "Unsupported value"},
		{`{"backend":"clickhouse"}`, "mgmt.querylog.clickhouse.url is required"},
		{`{"backend":"loki"}`, "mgmt.querylog.loki.url is required"},
		{`{"backend":"clickhouse","clickhouse":{"url":""}}`, "mgmt.querylog.clickhouse.url is required"},
		{`{"backend":"loki","loki":{"url":""}}`, "mgmt.querylog.loki.url is required"},
		{`{"clickhouse":{"database":""}}`, "database"},
		{`{"clickhouse":{"table":""}}`, "table"},
		{`{"clickhouse":{"passwordSecret":{"key":""}}}`, "key"},
		{`{"loki":{"selector":""}}`, "selector"},
		{`{"loki":{"lookback":""}}`, "lookback"},
		{`{"loki":{"passwordSecret":{"key":""}}}`, "key"},
	} {
		// Raw objects retain explicit empty strings and truly absent backend blocks.
		// Typed omitempty serialization would otherwise hide these validation cases.
		var querylog map[string]any
		if err := json.Unmarshal([]byte(tc.fixture), &querylog); err != nil {
			t.Fatal(err)
		}
		in := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(), "kind": "NexoraInstallation",
			"metadata": map[string]any{"name": "invalid-" + string(rune('a'+i)), "namespace": "default"},
			"spec":     map[string]any{"mgmt": map[string]any{"querylog": querylog}},
		}}
		err := c.Create(ctx, in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %s", tc.fixture, err, tc.want)
		}
	}
}
