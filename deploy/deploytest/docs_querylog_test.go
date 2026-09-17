package deploytest

import (
	"bytes"
	"os"
	"regexp"
	"testing"
)

// TestOperationsDocNamesQueryLogSettings checks that the operations guide and the architecture name
// every ClickHouse and Loki variable the management plane reads, that the guide carries the
// collector and Loki settings an operator needs, and that the help topic names the backends.
func TestOperationsDocNamesQueryLogSettings(t *testing.T) {
	cfg, err := os.ReadFile("../../mgmt/internal/config/config.go")
	if err != nil {
		t.Fatal(err)
	}
	names := regexp.MustCompile(`"(NEXORA_(?:CLICKHOUSE|LOKI)_[A-Z_]+)"`).FindAllStringSubmatch(string(cfg), -1)
	if len(names) < 11 {
		t.Fatalf("expected at least 11 clickhouse/loki variables in config.go, found %d", len(names))
	}
	ops, _ := os.ReadFile("../../docs/operations.md")
	arch, _ := os.ReadFile("../../docs/architecture.md")
	for _, m := range names {
		for doc, body := range map[string][]byte{"docs/operations.md": ops, "docs/architecture.md": arch} {
			if !bytes.Contains(body, []byte(m[1])) {
				t.Errorf("%s does not name %s", doc, m[1])
			}
		}
	}
	for _, want := range []string{"deploy/clickhouse/querylog.sql", "create_schema: false", "/otlp", "allow_structured_metadata", "max_query_series"} {
		if !bytes.Contains(ops, []byte(want)) {
			t.Errorf("docs/operations.md lacks %q", want)
		}
	}
	help, _ := os.ReadFile("../../web/src/help/topics/observability.md")
	for _, want := range []string{"ClickHouse", "Loki", "OpenSearch"} {
		if !bytes.Contains(help, []byte(want)) {
			t.Errorf("observability help topic lacks %s", want)
		}
	}
}
