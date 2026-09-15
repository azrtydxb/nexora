package deploytest

import (
	"os"
	"strings"
	"testing"
)

// TestM7DebtMarkersResolved fails while any v1 debt marker lifted by milestone M7 (#9-#29) is still in the tree.
func TestM7DebtMarkersResolved(t *testing.T) {
	for file, marker := range map[string]string{
		"../../e2e/fixtures/authhier/server.go":          "no wildcard synthesis",
		"../../e2e/harness/otelcol.go":                   "the port stays free while the collector is down",
		"../../engine/src/acl.rs":                        "switch to a prefix trie",
		"../../engine/src/authoritative/dispatch.rs":     "the whole transfer is built on the worker thread",
		"../../engine/src/control.rs":                    "is retried with backoff instead of discarded",
		"../../engine/src/recursor/dnssec/anchors.rs":    "a revocation is only accepted when the RRset also verifies",
		"../../engine/src/recursor/dnssec/nsec_cache.rs": "denial records kept per zone",
		"../../engine/src/recursor/infra.rs":             "read-modify-write is not atomic",
		"../../engine/src/recursor/iterate.rs":           "fixed capacities (entries)",
		"../../engine/src/recursor/rpz/transfer.rs":      "one string key per zone record per difference",
		"../../engine/src/server/doq.rs":                 "one stream buffer, answer buffer and frame per DoQ query",
		"../../engine/src/server/mod.rs":                 "answered as version 0 instead of",
		"../../engine/src/server/stream.rs":              "one query buffer, one 64 KiB answer buffer",
		"../../engine/src/telemetry/otlp.rs":             "one batch export at a time",
		"../../mgmt/internal/control/server.go":          "the whole blob is read into memory",
		"../../mgmt/internal/querylog/opensearch.go":     "@timestamp has millisecond resolution",
		"../../mgmt/internal/secrets/pkcs11.go":          "a session invalidated by the token",
		"../../mgmt/internal/zone/import.go":             "loads every record into memory",
		"../../mgmt/internal/zone/service.go":            "loads the whole zone per record edit",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		if strings.Contains(string(raw), marker) {
			t.Errorf("%s still carries the debt marker %q", file, marker)
		}
	}
	otlp, _ := os.ReadFile("../../engine/src/telemetry/otlp.rs")
	control, _ := os.ReadFile("../../engine/src/control.rs")
	for _, gone := range []struct{ text, marker string }{
		{string(otlp), "the index is resolved against the runtime current at drain time"},
		{string(control), "an identity that cannot be stored is re-enrolled"},
	} {
		if strings.Contains(gone.text, gone.marker) {
			t.Errorf("debt marker %q remains", gone.marker)
		}
	}
	build, _ := os.ReadFile("../../mgmt/internal/zone/build.go")
	if !strings.Contains(string(build), "debt: DNSSEC-signed zones still rebuild") {
		t.Error("the signed-zone remainder of #29 must be recorded as a debt marker in mgmt/internal/zone/build.go")
	}
}
