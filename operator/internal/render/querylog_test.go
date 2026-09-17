package render_test

import (
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/render"
)

func querylogValues(t *testing.T, querylog string) map[string]any {
	t.Helper()
	var spec v1alpha1.NexoraInstallationSpec
	if err := yaml.UnmarshalStrict([]byte(`{"mgmt":{"querylog":`+querylog+`}}`), &spec); err != nil {
		t.Fatal(err)
	}
	v, err := render.BuildValues(spec, render.Injected{Tag: "test", CASecret: "ca", KEKSecret: "kek", BootstrapTokenSecret: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	return v.Map
}

func TestQuerylogValuesPreserveDefaults(t *testing.T) {
	for _, fixture := range []string{`{}`, `{"backend":"builtin","builtinCapacity":42,"opensearch":{"url":"https://search.example","index":"logs-*"}}`, `{"backend":"clickhouse","clickhouse":{"url":"https://clickhouse.example","passwordSecret":{"name":"auth"}}}`, `{"backend":"loki","loki":{"url":"https://loki.example","tenant":"team"}}`} {
		var want map[string]any
		if err := yaml.Unmarshal([]byte(fixture), &want); err != nil {
			t.Fatal(err)
		}
		got := querylogValues(t, fixture)["mgmt"].(map[string]any)["querylog"]
		if len(want) == 0 {
			if got != nil {
				t.Fatalf("unset querylog overrides chart defaults: %v", got)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

// This contract deliberately requires the M10 chart in the merged tree.
func TestQuerylogRenderContract(t *testing.T) {
	cases := []struct {
		name, fixture string
		env           map[string]string
		secret, key   string
	}{
		{"builtin", `{}`, map[string]string{"NEXORA_QUERYLOG_BACKEND": "builtin", "NEXORA_QUERYLOG_BUILTIN_CAPACITY": "200000"}, "", ""},
		{"opensearch", `{"backend":"opensearch","opensearch":{"url":"https://search.example"}}`, map[string]string{"NEXORA_QUERYLOG_BACKEND": "opensearch", "NEXORA_OPENSEARCH_URL": "https://search.example", "NEXORA_OPENSEARCH_INDEX": "nexora-querylog-*"}, "", ""},
		{"clickhouse-defaults", `{"backend":"clickhouse","clickhouse":{"url":"https://clickhouse.example","passwordSecret":{"name":"ch-auth"}}}`, map[string]string{"NEXORA_QUERYLOG_BACKEND": "clickhouse", "NEXORA_CLICKHOUSE_URL": "https://clickhouse.example", "NEXORA_CLICKHOUSE_DATABASE": "nexora", "NEXORA_CLICKHOUSE_TABLE": "querylog", "NEXORA_CLICKHOUSE_USERNAME": "default", "NEXORA_CLICKHOUSE_PASSWORD_FILE": "/etc/nexora/querylog/password"}, "ch-auth", "password"},
		{"clickhouse-custom", `{"backend":"clickhouse","clickhouse":{"url":"https://clickhouse.example","database":"analytics","table":"dns","username":"reader","passwordSecret":{"name":"ch-auth","key":"credential"}}}`, map[string]string{"NEXORA_CLICKHOUSE_URL": "https://clickhouse.example", "NEXORA_CLICKHOUSE_DATABASE": "analytics", "NEXORA_CLICKHOUSE_TABLE": "dns", "NEXORA_CLICKHOUSE_USERNAME": "reader", "NEXORA_CLICKHOUSE_PASSWORD_FILE": "/etc/nexora/querylog/password"}, "ch-auth", "credential"},
		{"loki-defaults", `{"backend":"loki","loki":{"url":"https://loki.example"}}`, map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki", "NEXORA_LOKI_URL": "https://loki.example", "NEXORA_LOKI_SELECTOR": `{service_name="nexora-engine"}`, "NEXORA_LOKI_TENANT": "", "NEXORA_LOKI_USERNAME": "", "NEXORA_LOKI_LOOKBACK": "168h"}, "", ""},
		{"loki-custom", `{"backend":"loki","loki":{"url":"https://loki.example","selector":"{app=\"dns\"}","tenant":"team","username":"reader","lookback":"24h","passwordSecret":{"name":"loki-auth","key":"credential"}}}`, map[string]string{"NEXORA_LOKI_URL": "https://loki.example", "NEXORA_LOKI_SELECTOR": `{app="dns"}`, "NEXORA_LOKI_TENANT": "team", "NEXORA_LOKI_USERNAME": "reader", "NEXORA_LOKI_LOOKBACK": "24h", "NEXORA_LOKI_PASSWORD_FILE": "/etc/nexora/querylog/password"}, "loki-auth", "credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs, err := load(t).Render(target, querylogValues(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, obj := range objs {
				if obj.GetKind() != "Deployment" {
					continue
				}
				var dep appsv1.Deployment
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &dep); err != nil {
					t.Fatal(err)
				}
				for _, container := range dep.Spec.Template.Spec.Containers {
					env := map[string]string{}
					for _, e := range container.Env {
						env[e.Name] = e.Value
					}
					if _, ok := env["NEXORA_QUERYLOG_BACKEND"]; !ok {
						continue
					}
					found = true
					for k, want := range tc.env {
						if got, ok := env[k]; !ok || got != want {
							t.Errorf("%s = %q (present %v), want %q", k, got, ok, want)
						}
					}
					for k := range env {
						if strings.HasSuffix(k, "_PASSWORD") && (strings.Contains(k, "CLICKHOUSE") || strings.Contains(k, "LOKI")) {
							t.Errorf("inline password env %s", k)
						}
					}
					mounts := 0
					for _, m := range container.VolumeMounts {
						if m.Name == "querylog-password" {
							mounts++
							if m.MountPath != "/etc/nexora/querylog" || !m.ReadOnly {
								t.Errorf("invalid credential mount: %+v", m)
							}
						}
					}
					volumes := 0
					for _, v := range dep.Spec.Template.Spec.Volumes {
						if v.Name == "querylog-password" {
							volumes++
							if v.Secret == nil || v.Secret.SecretName != tc.secret || len(v.Secret.Items) != 1 || v.Secret.Items[0].Key != tc.key || v.Secret.Items[0].Path != "password" {
								t.Errorf("invalid credential reference: %+v", v.Secret)
							}
						}
					}
					wantCount := 0
					if tc.secret != "" {
						wantCount = 1
					}
					if mounts != wantCount || volumes != wantCount {
						t.Errorf("credential mounts/volumes = %d/%d, want %d", mounts, volumes, wantCount)
					}
					if tc.secret == "" {
						for _, key := range []string{"NEXORA_CLICKHOUSE_PASSWORD_FILE", "NEXORA_LOKI_PASSWORD_FILE"} {
							if _, ok := env[key]; ok {
								t.Errorf("unexpected %s", key)
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("management deployment not found")
			}
		})
	}
}
