package querylog_test

import (
	"context"
	"sort"
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

func TestBuiltinCategoryAttribution(t *testing.T) {
	b := querylog.NewBuiltin(10)
	req := request("casino.example.", "clean.example.")
	attrs := &req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
	*attrs = append(*attrs, str("nexora.filter.list_id", "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1"), str("nexora.filter.category", "gambling"))
	b.Ingest("e1", req)
	page, err := b.Search(context.Background(), querylog.Query{Categories: []string{"gambling"}, Limit: 10})
	if err != nil || len(page.Records) != 1 || page.Records[0].Name != "casino.example." ||
		page.Records[0].ListID != "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1" || page.Records[0].Category != "gambling" {
		t.Fatalf("category filter: %+v %v", page, err)
	}
	all, _ := b.Search(context.Background(), querylog.Query{Limit: 10})
	if len(all.Records) != 2 {
		t.Fatalf("positive path: %d records", len(all.Records))
	}
	none, _ := b.Search(context.Background(), querylog.Query{Categories: []string{"adult"}, Limit: 10})
	if len(none.Records) != 0 {
		t.Fatalf("other category matched: %+v", none)
	}
}

func TestBuiltinPartialNamesAndMultiValues(t *testing.T) {
	b := querylog.NewBuiltin(10)
	req := request("www.you-1.tube.test.", "you-1.test.", "x*y-1.test.", "other.test.")
	lr := req.ResourceLogs[0].ScopeLogs[0].LogRecords
	lr[1].Attributes[3] = str("dns.response.code", "NXDOMAIN")
	lr[2].Attributes[2] = str("dns.question.type", "AAAA")
	lr[0].Attributes = append(lr[0].Attributes, str("nexora.filter.source", "allowlist"), str("nexora.filter.rule", "tube.test"), str("nexora.policy.group", "g1"))
	b.Ingest("e1", req)
	names := func(q querylog.Query) []string {
		q.Limit = 10
		p, err := b.Search(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range p.Records {
			out = append(out, r.Name)
		}
		sort.Strings(out)
		return out
	}
	if got := names(querylog.Query{Name: "YOU-1."}); len(got) != 2 {
		t.Fatalf("partial, case-insensitive, trailing dot: %v", got)
	}
	if got := names(querylog.Query{Name: "x*y"}); len(got) != 1 || got[0] != "x*y-1.test." {
		t.Fatalf("literal *: %v", got)
	}
	if got := names(querylog.Query{Name: "x?y"}); len(got) != 0 {
		t.Fatalf("? is literal: %v", got)
	}
	if got := names(querylog.Query{RCodes: []string{"NXDOMAIN", "NOERROR"}, QTypes: []string{"AAAA"}}); len(got) != 1 || got[0] != "x*y-1.test." {
		t.Fatalf("OR within, AND across: %v", got)
	}
	if got := names(querylog.Query{Sources: []string{"allowlist"}, PolicyGroups: []string{"g1"}}); len(got) != 1 {
		t.Fatalf("source and policy group: %v", got)
	}
	if got := names(querylog.Query{PolicyGroups: []string{"global"}}); len(got) != 3 {
		t.Fatalf("global policy group: %v", got)
	}
	p, _ := b.Search(context.Background(), querylog.Query{Sources: []string{"allowlist"}, Limit: 1})
	if p.Records[0].Rule != "tube.test" || p.Records[0].PolicyGroupID != "g1" {
		t.Fatalf("reason fields: %+v", p.Records[0])
	}
}
