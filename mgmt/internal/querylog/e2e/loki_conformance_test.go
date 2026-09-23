package e2e

import (
	"context"
	"fmt"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest"
)

// lokiBackend starts a local Loki and a collector exporting to its OTLP endpoint through
// transform/loki, and returns the adapter (default selector and lookback) with an ingest function.
func lokiBackend(t *testing.T, o harness.LokiOptions) (*harness.Loki, *querylog.Loki, func(testing.TB, string, *collogspb.ExportLogsServiceRequest)) {
	t.Helper()
	env := harness.New(t)
	loki := env.StartLoki(o)
	col := env.StartOtelcol(harness.OtelcolConfig{LokiURL: loki.URL, DebugFile: env.Dir + "/otel.jsonl"})
	backend, err := querylog.NewLoki(config.LokiConfig{URL: loki.URL, Selector: `{service_name="nexora-engine"}`, Lookback: 168 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return loki, backend, func(t testing.TB, _ string, req *collogspb.ExportLogsServiceRequest) {
		querylogtest.OTLPExport(t, col.OTLPGRPC, []querylogtest.EngineBatch{{Req: req}})
	}
}

// TestQueryLogConformanceLoki feeds the conformance dataset through a real collector into a local
// Loki and runs the conformance suite against the adapter; top-partitioned proves the series-limit
// fallback against a Loki whose max_query_series the dataset exceeds.
func TestQueryLogConformanceLoki(t *testing.T) {
	requireE2E(t)
	_, backend, ingest := lokiBackend(t, harness.LokiOptions{})
	run := strings.TrimSuffix(harness.UniqueName("conf"), ".")
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	querylogtest.Run(t, run, base, querylogtest.Harness{Backend: backend, Ingest: ingest, Visible: querylogtest.PollVisible(backend, base)})

	t.Run("top-partitioned", func(t *testing.T) {
		loki, backend, ingest := lokiBackend(t, harness.LokiOptions{MaxQuerySeries: 5})
		run := strings.TrimSuffix(harness.UniqueName("part"), ".")
		batches := querylogtest.Dataset(run, base)
		ref := querylog.NewBuiltin(10_000)
		for _, b := range batches {
			ingest(t, b.EngineID, b.Req)
			ref.Ingest(b.EngineID, b.Req)
		}
		querylogtest.PollVisible(backend, base)(t, querylogtest.InWindow(batches, base))

		// Positive precondition: the dataset's 20 in-window names exceed max_query_series 5 in one
		// query. By first character they spread over partitions of at most 5 names (t: tie and the
		// four top-*; r: renamed, rpz, rw, race; x: the three x?y names; ...).
		resp, err := http.Get(loki.URL + "/loki/api/v1/query?" + url.Values{
			"query": {`sum by (dns_question_name) (count_over_time({service_name="nexora-engine"}[10m]))`},
		}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "maximum number of series") {
			t.Fatalf("unpartitioned query: HTTP %d %s, want 400 maximum number of series", resp.StatusCode, body)
		}

		from, to := querylogtest.Window(base)
		q := querylog.TopQuery{From: from, To: to, Field: querylog.TopName, Limit: 3}
		got, err := backend.Top(context.Background(), q)
		if err != nil {
			t.Fatalf("partitioned top: %v", err)
		}
		want, _ := ref.Top(context.Background(), q)
		if !slices.Equal(got, want) {
			t.Fatalf("partitioned top %v, reference %v", got, want)
		}
	})
}

// TestLokiTopNanosecondBoundaries verifies actual LogQL template execution, millisecond rounding,
// and inclusive endpoints without changing the M10 time contract.
func TestLokiTopNanosecondBoundaries(t *testing.T) {
	requireE2E(t)
	_, backend, ingest := lokiBackend(t, harness.LokiOptions{})
	from := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Minute).Add(time.Minute - time.Nanosecond)
	to := from.Add(1900 * time.Microsecond)
	seed := querylogtest.Dataset("boundary", from)[0]
	original := seed.Req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	seed.Req.ResourceLogs[0].ScopeLogs[0].LogRecords = nil
	times := []time.Time{from.Add(-time.Nanosecond), from, to, to.Add(time.Nanosecond), to.Add(500 * time.Microsecond)}
	for i, ts := range times {
		lr := proto.Clone(original).(*logspb.LogRecord)
		lr.TimeUnixNano = uint64(ts.UnixNano())
		lr.Attributes = append(lr.Attributes, &commonpb.KeyValue{Key: "nexora.filter.category", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "boundary"}}})
		// Unique lines prevent Loki's documented identical timestamp+line deduplication.
		for _, kv := range lr.Attributes {
			if kv.Key == "dns.question.name" {
				kv.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("edge-%d.test.", i)}}
			}
		}
		seed.Req.ResourceLogs[0].ScopeLogs[0].LogRecords = append(seed.Req.ResourceLogs[0].ScopeLogs[0].LogRecords, lr)
	}
	ingest(t, seed.EngineID, seed.Req)
	deadline := time.Now().Add(time.Minute)
	for {
		page, err := backend.Search(context.Background(), querylog.Query{From: from.Add(-time.Second), To: to.Add(time.Second), Name: "edge-", Limit: 100})
		if err == nil && len(page.Records) == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("boundary records not visible: %v %v", page, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ref := querylog.NewBuiltin(10)
	ref.Ingest(seed.EngineID, seed.Req)
	for _, window := range [][2]time.Time{{from, to}, {from, from}, {to, to}, {from.Add(time.Nanosecond), to.Add(-time.Nanosecond)}} {
		for _, field := range []querylog.TopField{querylog.TopName, querylog.TopClient, querylog.TopCategory} {
			q := querylog.TopQuery{From: window[0], To: window[1], Field: field, Limit: 10}
			got, err := backend.Top(context.Background(), q)
			want, _ := ref.Top(context.Background(), q)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("window %v field %s got %v %v want %v", window, field, got, err, want)
			}
		}
	}
}
