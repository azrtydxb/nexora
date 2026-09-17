package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOtelcolConfigValidates(t *testing.T) {
	cfg := OtelcolConfig{OpenSearchURL: "http://127.0.0.1:9200", OpenSearchIndex: "nexora-conformance-1",
		ClickHouseNative: "127.0.0.1:9000", ClickHouseUser: "nexora_writer", ClickHousePassword: "pw",
		LokiURL: "http://127.0.0.1:3100"}
	raw, err := RenderOtelcolConfig(cfg, "127.0.0.1:4317")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`logs_index: "nexora-conformance-1"`,
		"clickhouse:", `endpoint: "tcp://127.0.0.1:9000"`, "create_schema: false", `logs_table_name: "querylog"`, `database: "nexora"`,
		"logs/clickhouse:", "otlphttp/loki:", `endpoint: "http://127.0.0.1:3100/otlp"`, "transform/loki:", "logs/loki:",
		`set(log.body, Concat([log.attributes["client.address"], log.attributes["dns.question.name"], log.attributes["dns.question.type"], log.attributes["nexora.transport"], log.attributes["nexora.engine.id"]], " "))`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %s:\n%s", want, s)
		}
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("otelcol-contrib", "validate", "--config", p).CombinedOutput(); err != nil {
		t.Fatalf("otelcol-contrib validate: %v\n%s", err, out)
	}
	bare, _ := RenderOtelcolConfig(OtelcolConfig{}, "127.0.0.1:4317")
	if strings.Contains(string(bare), "clickhouse") || strings.Contains(string(bare), "loki") {
		t.Fatalf("exporters must be omitted when unset:\n%s", bare)
	}
}
