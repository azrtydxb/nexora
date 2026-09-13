package querylog_test

import (
	"context"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func request(names ...string) *collogspb.ExportLogsServiceRequest {
	var recs []*logspb.LogRecord
	for i, n := range names {
		recs = append(recs, &logspb.LogRecord{TimeUnixNano: uint64(1_700_000_000_000_000_000 + i), Attributes: []*commonpb.KeyValue{
			str("client.address", "10.0.0.1"), str("dns.question.name", n), str("dns.question.type", "A"), str("dns.response.code", "NOERROR"),
			str("nexora.cache", "miss"), str("nexora.filter", "none"), str("nexora.upstream", "fx"), str("nexora.transport", "udp"),
			str("nexora.engine.id", "e1"), {Key: "nexora.duration_us", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 420}}},
		}})
	}
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}}}}}
}

func TestBuiltinRingSearchAndCapacity(t *testing.T) {
	b := querylog.NewBuiltin(3)
	b.Ingest("e1", request("a.example.", "b.example.", "c.example.", "d.example."))
	page, err := b.Search(context.Background(), querylog.Query{Limit: 10})
	if err != nil || len(page.Records) != 3 || page.Records[0].Name != "d.example." {
		t.Fatalf("ring: %+v %v", page, err)
	}
	page, _ = b.Search(context.Background(), querylog.Query{Name: "b.example", Limit: 10})
	if len(page.Records) != 1 || page.Records[0].DurationUS != 420 || page.Records[0].Client != "10.0.0.1" {
		t.Fatalf("filter by name: %+v", page)
	}
	first, _ := b.Search(context.Background(), querylog.Query{Limit: 2})
	second, _ := b.Search(context.Background(), querylog.Query{Limit: 2, Cursor: first.NextCursor})
	if len(first.Records) != 2 || len(second.Records) != 1 {
		t.Fatalf("paging: %d then %d", len(first.Records), len(second.Records))
	}
}
