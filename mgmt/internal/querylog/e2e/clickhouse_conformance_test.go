package e2e

import (
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest"
)

// TestQueryLogConformanceClickHouse starts a local ClickHouse with deploy/clickhouse/querylog.sql,
// feeds the conformance dataset through a real collector's clickhouse exporter (create_schema:
// false, as nexora_writer) and runs the conformance suite against the adapter as nexora_reader.
func TestQueryLogConformanceClickHouse(t *testing.T) {
	requireE2E(t)
	env := harness.New(t)
	ch := env.StartClickHouse()
	col := env.StartOtelcol(harness.OtelcolConfig{ClickHouseNative: ch.NativeAddr, ClickHouseUser: ch.WriterUser, ClickHousePassword: ch.WriterPassword, DebugFile: env.Dir + "/otel.jsonl"})
	backend, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: ch.HTTPURL, Database: ch.Database, Table: ch.Table, Username: ch.ReaderUser, PasswordFile: ch.ReaderPasswordFile})
	if err != nil {
		t.Fatal(err)
	}
	run := strings.TrimSuffix(harness.UniqueName("conf"), ".")
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	querylogtest.Run(t, run, base, querylogtest.Harness{
		Backend: backend,
		Ingest: func(t testing.TB, _ string, req *collogspb.ExportLogsServiceRequest) {
			querylogtest.OTLPExport(t, col.OTLPGRPC, []querylogtest.EngineBatch{{Req: req}})
		},
		Visible: querylogtest.PollVisible(backend, base),
	})
}
