package querylog_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest"
)

func builtinHarness(b *querylog.Builtin) querylogtest.Harness {
	return querylogtest.Harness{
		Backend: b,
		Ingest:  func(_ testing.TB, id string, req *collogspb.ExportLogsServiceRequest) { b.Ingest(id, req) },
		Visible: func(testing.TB, int) {},
	}
}

func TestQueryLogConformanceBuiltin(t *testing.T) {
	run := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	querylogtest.Run(t, run, base, builtinHarness(querylog.NewBuiltin(10_000)))
}

// lossy drops the last record of every non-final page: a paging bug the suite must catch. It keeps
// the builtin's Top, so the paging bug is the only divergence.
type lossy struct{ *querylog.Builtin }

func (l lossy) Search(ctx context.Context, q querylog.Query) (querylog.Page, error) {
	p, err := l.Builtin.Search(ctx, q)
	if err == nil && p.NextCursor != "" && len(p.Records) > 1 {
		p.Records = p.Records[:len(p.Records)-1]
	}
	return p, err
}

type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
func (r *recordingTB) Fatalf(string, ...any) { r.failed = true; panic(errStop) }
func (r *recordingTB) Helper()               {}

var errStop = new(struct{})

func TestConformanceDetectsDivergence(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	good := querylog.NewBuiltin(10_000)
	h := builtinHarness(good)
	querylogtest.Run(t, "pos", base, h) // positive path: the unmodified builtin passes
	bad := querylog.NewBuiltin(10_000)
	h = builtinHarness(bad)
	h.Backend = lossy{bad}
	rec := &recordingTB{TB: t}
	func() {
		defer func() { _ = recover() }()
		querylogtest.Run(rec, "neg", base, h)
	}()
	if !rec.failed {
		t.Fatal("conformance suite passed a backend that loses a record per page")
	}
}
