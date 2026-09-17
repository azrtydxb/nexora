package e2e

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

// queryLogMgmtOptions returns the management options for backend, starting what that backend reads
// from: an OpenTelemetry Collector that exports to OpenSearch, a local ClickHouse or a local Loki.
// Builtin needs nothing beyond the backend name.
func queryLogMgmtOptions(t *testing.T, env *harness.Env, backend string) harness.MgmtOptions {
	t.Helper()
	opts := harness.MgmtOptions{QueryLogBackend: backend}
	switch backend {
	case "builtin":
	case "opensearch":
		// A private index: the shared OpenSearch also holds live kw traffic and earlier runs, which
		// crowd a fresh name out of the dashboard top list and shadow fixed names.
		osURL := harness.OpenSearchURL(t)
		index := "nexora-e2e-" + strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(harness.UniqueName("ql"), "."), ".", "-"))
		t.Cleanup(func() {
			// Runs after the collector's stop below (cleanups run last-in first-out), so no late export
			// re-creates a deleted index; the name is unique, so the pattern matches only this run's.
			openSearchRequest(t, http.MethodDelete, osURL+"/"+index+"*", "", http.StatusOK)
		})
		// The collector appends the UTC day (logs_index_time_format yyyy.MM.dd). The day's index is
		// created up front, with the @timestamp mapping the adapter sorts on: a search that races the
		// exporter's auto-creation fails with "all shards failed". The next hour's day covers a run
		// that crosses midnight.
		now := time.Now().UTC()
		for _, day := range slices.Compact([]string{now.Format("2006.01.02"), now.Add(time.Hour).Format("2006.01.02")}) {
			openSearchRequest(t, http.MethodPut, osURL+"/"+index+"-"+day, `{"mappings":{"properties":{"@timestamp":{"type":"date"}}}}`, http.StatusOK)
		}
		col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: osURL, OpenSearchIndex: index, DebugFile: env.Dir + "/otel.jsonl"})
		t.Cleanup(col.Proc.Stop)
		opts.OpenSearchURL = osURL
		opts.OTLPEndpoint = "http://" + col.OTLPGRPC
		opts.ExtraEnv = append(opts.ExtraEnv, "NEXORA_OPENSEARCH_INDEX="+index+"*")
	case "clickhouse":
		ch := env.StartClickHouse()
		col := env.StartOtelcol(harness.OtelcolConfig{ClickHouseNative: ch.NativeAddr, ClickHouseUser: ch.WriterUser,
			ClickHousePassword: ch.WriterPassword, DebugFile: env.Dir + "/otel.jsonl"})
		opts.ClickHouseURL = ch.HTTPURL
		opts.ClickHousePasswordFile = ch.ReaderPasswordFile
		opts.OTLPEndpoint = "http://" + col.OTLPGRPC
	case "loki":
		lk := env.StartLoki(harness.LokiOptions{})
		col := env.StartOtelcol(harness.OtelcolConfig{LokiURL: lk.URL, DebugFile: env.Dir + "/otel.jsonl"})
		opts.LokiURL = lk.URL
		opts.OTLPEndpoint = "http://" + col.OTLPGRPC
	default:
		t.Fatalf("unknown query-log backend %q", backend)
	}
	return opts
}

// openSearchRequest sends a request with an optional JSON body to the shared OpenSearch and fails the
// test unless it answers want.
func openSearchRequest(t *testing.T, method, url, body string, want int) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d, want %d: %s", method, url, resp.StatusCode, want, body)
	}
}
