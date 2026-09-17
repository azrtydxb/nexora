// Package e2e runs the query-log conformance suite against external backends fed by a real
// OpenTelemetry Collector. It lives under mgmt/ because the adapters are internal packages the
// top-level e2e module path cannot import; `make e2e` runs it with NEXORA_E2E_BIN_DIR set.
package e2e

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest"
)

// requireE2E skips outside an e2e run (`make mgmt-test` and CI unit jobs have no collector or
// OpenSearch).
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("NEXORA_E2E_BIN_DIR") == "" {
		t.Skip("NEXORA_E2E_BIN_DIR is not set: run through make e2e")
	}
}

// TestQueryLogConformanceOpenSearch feeds the conformance dataset through a real collector into a
// private index of the shared OpenSearch and runs the conformance suite against the adapter.
func TestQueryLogConformanceOpenSearch(t *testing.T) {
	requireE2E(t)
	env := harness.New(t)
	run := strings.TrimSuffix(harness.UniqueName("conf"), ".")
	index := "nexora-conformance-" + strings.ToLower(strings.ReplaceAll(run, ".", "-"))
	osURL := harness.OpenSearchURL(t)
	t.Cleanup(func() {
		// The collector appends a date to the index name; run is unique, so the pattern matches only
		// this run's indices in the shared OpenSearch.
		req, _ := http.NewRequest(http.MethodDelete, osURL+"/"+index+"*", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	})
	col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: osURL, OpenSearchIndex: index, DebugFile: env.Dir + "/otel.jsonl"})
	backend, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: osURL, Index: index + "*"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	querylogtest.Run(t, run, base, querylogtest.Harness{
		Backend: backend,
		Ingest: func(t testing.TB, _ string, req *collogspb.ExportLogsServiceRequest) {
			querylogtest.OTLPExport(t, col.OTLPGRPC, []querylogtest.EngineBatch{{Req: req}})
		},
		Visible: querylogtest.PollVisible(backend, base),
	})
}
