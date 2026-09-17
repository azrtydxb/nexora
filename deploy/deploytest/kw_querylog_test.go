package deploytest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// decodeFile decodes every YAML document of path.
func decodeFile(t *testing.T, path string) []obj {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the repository's own manifests
	if err != nil {
		t.Fatal(err)
	}
	var docs []obj
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var o map[string]any
		if err := dec.Decode(&o); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if o != nil {
			docs = append(docs, obj(o))
		}
	}
	return docs
}

func secretEnv(t *testing.T, c obj, name, secret, key string) {
	t.Helper()
	if _, ok := c["env"].([]any); !ok {
		t.Fatalf("container %s has no env", c["name"])
	}
	e := env(c, name)
	if e == nil {
		t.Fatalf("container %s has no env %s", c["name"], name)
	}
	if got := e.path("valueFrom", "secretKeyRef", "name"); got != secret {
		t.Errorf("env %s secret = %v, want %s", name, got, secret)
	}
	if got := e.path("valueFrom", "secretKeyRef", "key"); got != key {
		t.Errorf("env %s key = %v, want %s", name, got, key)
	}
}

func ports(list any) []int {
	var out []int
	for _, p := range list.([]any) {
		m := p.(map[string]any)
		for _, k := range []string{"containerPort", "port"} {
			if n, ok := m[k].(int); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// TestKwClickHouseManifest checks deploy/kw/clickhouse.yaml: the pinned image on a 20 Gi
// longhorn-single claim, the writer and reader passwords only from Secret nexora-clickhouse, the
// default user limited to loopback, and no password literal anywhere in the file.
func TestKwClickHouseManifest(t *testing.T) {
	const path = "../kw/clickhouse.yaml"
	docs := decodeFile(t, path)
	for _, d := range docs {
		if ns := d.path("metadata", "namespace"); ns != "nexora" {
			t.Errorf("%s/%s namespace = %v, want nexora", d["kind"], d.path("metadata", "name"), ns)
		}
	}

	sts := find(t, docs, "StatefulSet", "clickhouse")
	c := container(t, sts, "clickhouse")
	if img := c["image"]; img != "192.168.10.131/clickhouse/clickhouse-server:26.8.4.11" {
		t.Errorf("image = %v", img)
	}
	claims, _ := sts.path("spec", "volumeClaimTemplates").([]any)
	if len(claims) == 0 {
		t.Fatal("no volumeClaimTemplates")
	}
	claim := obj(claims[0].(map[string]any))
	if sc := claim.path("spec", "storageClassName"); sc != "longhorn-single" {
		t.Errorf("storageClassName = %v", sc)
	}
	if st := claim.path("spec", "resources", "requests", "storage"); st != "20Gi" {
		t.Errorf("storage = %v", st)
	}
	for _, k := range []string{"requests", "limits"} {
		if m, _ := c.path("resources", k).(map[string]any); len(m) == 0 {
			t.Errorf("resources.%s not set", k)
		}
	}
	secretEnv(t, c, "CLICKHOUSE_WRITER_PASSWORD", "nexora-clickhouse", "writer-password")
	secretEnv(t, c, "CLICKHOUSE_READER_PASSWORD", "nexora-clickhouse", "reader-password")
	if got := ports(c["ports"]); !reflect.DeepEqual(got, []int{8123, 9000}) {
		t.Errorf("container ports = %v, want [8123 9000]", got)
	}

	cm := find(t, docs, "ConfigMap", "clickhouse-config")
	users, _ := cm.path("data", "users.xml").(string)
	if users == "" {
		t.Fatal("ConfigMap clickhouse-config has no users.xml (mounted as users.d/nexora.xml)")
	}
	for _, want := range []string{`from_env="CLICKHOUSE_WRITER_PASSWORD"`, `from_env="CLICKHOUSE_READER_PASSWORD"`} {
		if !strings.Contains(users, want) {
			t.Errorf("users.d/nexora.xml lacks %s", want)
		}
	}
	def := regexp.MustCompile(`(?s)<default>.*?</default>`).FindString(users)
	if !strings.Contains(def, "<ip>::1</ip>") || !strings.Contains(def, "<ip>127.0.0.1</ip>") {
		t.Errorf("user default is not limited to loopback:\n%s", def)
	}
	if cfg, _ := cm.path("data", "config.xml").(string); !strings.Contains(cfg, "<listen_host>0.0.0.0</listen_host>") {
		t.Errorf("config.d/nexora.xml does not listen on 0.0.0.0:\n%s", cfg)
	}

	if got := ports(find(t, docs, "Service", "clickhouse").path("spec", "ports")); !reflect.DeepEqual(got, []int{8123, 9000}) {
		t.Errorf("Service ports = %v, want [8123 9000]", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`password>[^<]+<`).Find(raw); m != nil {
		t.Errorf("%s holds a password literal: %s", path, m)
	}
}

// TestKwCollectorFansOutQueryLogs checks that the kw collector keeps the OpenSearch pipeline and fans
// the same records out to ClickHouse (as nexora_writer, schema owned by Nexora) and the shared Loki,
// and that otelcol-contrib accepts the config.
func TestKwCollectorFansOutQueryLogs(t *testing.T) {
	docs := decodeFile(t, "../kw/otelcol.yaml")
	raw, _ := find(t, docs, "ConfigMap", "nexora-otelcol").path("data", "config.yaml").(string)
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	c := obj(cfg)
	for _, p := range []struct {
		name                  string
		processors, exporters []any
	}{
		{"logs", []any{"batch", "transform/querylog"}, []any{"opensearch"}},
		{"logs/clickhouse", []any{"batch"}, []any{"clickhouse"}},
		{"logs/loki", []any{"batch", "transform/loki"}, []any{"otlphttp/loki"}},
	} {
		pl := obj(nil)
		if m, ok := c.path("service", "pipelines", p.name).(map[string]any); ok {
			pl = obj(m)
		} else {
			t.Errorf("pipeline %s missing", p.name)
			continue
		}
		if !reflect.DeepEqual(pl["exporters"], p.exporters) {
			t.Errorf("%s exporters = %v, want %v", p.name, pl["exporters"], p.exporters)
		}
		if !reflect.DeepEqual(pl["processors"], p.processors) {
			t.Errorf("%s processors = %v, want %v", p.name, pl["processors"], p.processors)
		}
		if !reflect.DeepEqual(pl["receivers"], []any{"otlp"}) {
			t.Errorf("%s receivers = %v, want [otlp]", p.name, pl["receivers"])
		}
	}
	for key, want := range map[string]any{
		"create_schema":   false,
		"username":        "nexora_writer",
		"password":        "${env:CLICKHOUSE_WRITER_PASSWORD}",
		"endpoint":        "tcp://clickhouse.nexora.svc.cluster.local:9000",
		"database":        "nexora",
		"logs_table_name": "querylog",
	} {
		if got := c.path("exporters", "clickhouse", key); got != want {
			t.Errorf("exporters.clickhouse.%s = %v, want %v", key, got, want)
		}
	}
	if got := c.path("exporters", "otlphttp/loki", "endpoint"); got != "http://loki.monitoring.svc:3100/otlp" {
		t.Errorf("exporters[otlphttp/loki].endpoint = %v", got)
	}
	secretEnv(t, container(t, find(t, docs, "Deployment", "nexora-otelcol"), "otelcol"),
		"CLICKHOUSE_WRITER_PASSWORD", "nexora-clickhouse", "writer-password")

	if _, err := exec.LookPath("otelcol-contrib"); err != nil {
		t.Fatal("otelcol-contrib not found: run in the dev pod (scripts/dev-exec.sh)")
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("otelcol-contrib", "validate", "--config", file) // nosemgrep: dangerous-exec-command
	cmd.Env = append(os.Environ(), "CLICKHOUSE_WRITER_PASSWORD=validate-only")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("otelcol-contrib validate: %v\n%s", err, out)
	}
}
