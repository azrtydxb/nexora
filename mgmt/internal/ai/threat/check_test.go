package threat_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/threat"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func attr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// ingest adds one record for name from client at at, with the given filter attribution.
func ingest(b *querylog.Builtin, at time.Time, client, name string, extra ...*commonpb.KeyValue) {
	attrs := []*commonpb.KeyValue{
		attr("client.address", client), attr("dns.question.name", name), attr("dns.question.type", "A"),
		attr("dns.response.code", "NOERROR"), attr("nexora.cache", "miss"), attr("nexora.filter", "none"),
	}
	attrs = append(attrs, extra...)
	b.Ingest("e1", &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{
		LogRecords: []*logspb.LogRecord{{TimeUnixNano: uint64(at.UnixNano()), Attributes: attrs}},
	}}}}})
}

func verdicts(vs ...map[string]any) map[string]any { return map[string]any{"verdicts": vs} }

func evilVerdict() map[string]any {
	return map[string]any{"name": "evil.ait.test", "is_threat": true, "categories": []string{"malware"},
		"confidence": 0.95, "reasoning": "listed by a malware feed and queried by two clients"}
}

func safeVerdict() map[string]any {
	return map[string]any{"name": "safe.ait.test", "is_threat": false, "categories": []string{},
		"confidence": 0.1, "reasoning": "ordinary name"}
}

func runCheck(t *testing.T, st *store.Store, svc *ai.Service, ql querylog.Backend, now time.Time, domains ...string) threat.Result {
	t.Helper()
	in, err := json.Marshal(map[string]any{"domains": domains})
	if err != nil {
		t.Fatal(err)
	}
	f := threat.NewCheckTask(st, svc, ql, func() time.Time { return now })
	out, err := f(context.Background(), ai.Task{Kind: ai.TaskThreatCheck, Input: in})
	if err != nil {
		t.Fatalf("check task: %v", err)
	}
	res, ok := out.(threat.Result)
	if !ok {
		t.Fatalf("result type %T", out)
	}
	return res
}

// TestThreatCheck catches a cached name that still costs a model call, query-log evidence that is not
// collected from the log (counts, clients, block reason), a verdict that is not cached for later reads,
// and an answer naming a domain that was never asked about being accepted.
func TestThreatCheck(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	b := querylog.NewBuiltin(100)
	for i := range 4 {
		client := "10.0.0.1"
		if i%2 == 1 {
			client = "10.0.0.2"
		}
		ingest(b, now.Add(-time.Duration(i+1)*time.Hour), client, "evil.ait.test.")
	}
	// The newest record is the blocked one, so the block reason comes from it.
	ingest(b, now.Add(-time.Minute), "10.0.0.1", "evil.ait.test.", attr("nexora.filter", "blocked"),
		attr("nexora.filter.source", "category"), attr("nexora.filter.category", "malware"))
	ingest(b, now.Add(-time.Hour), "10.0.0.1", "safe.ait.test.")

	if _, err := st.Pool.Exec(ctx, `insert into ai_domain_verdicts(name, is_threat, categories, confidence, reasoning,
		checked_at, expires_at) values ('cached.ait.test', true, '{phishing}', 0.8, 'seen before', $1, $2)`,
		now.Add(-24*time.Hour), now.Add(6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	m := aifake.Model(aifake.JSON(verdicts(evilVerdict(), safeVerdict())))
	svc := aifake.Service(t, st, m, nil)
	res := runCheck(t, st, svc, b, now, "EVIL.ait.test.", "safe.ait.test", "cached.ait.test")

	calls := m.RecordedCalls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls))
	}
	prompt, err := json.Marshal(calls[0].Messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prompt), "cached.ait.test") {
		t.Fatalf("the cached name was sent to the model: %s", prompt)
	}
	if len(res.Results) != 3 {
		t.Fatalf("results = %+v", res.Results)
	}
	byName := map[string]threat.Verdict{}
	for _, v := range res.Results {
		byName[v.Name] = v
	}
	evil := byName["evil.ait.test"]
	switch {
	case !evil.IsThreat || evil.Confidence != 0.95 || len(evil.Categories) != 1 || evil.Categories[0] != "malware":
		t.Fatalf("evil verdict = %+v", evil)
	case evil.QueryCount != 5 || evil.ClientCount != 2:
		t.Fatalf("evil counts = %d queries from %d clients, want 5 and 2", evil.QueryCount, evil.ClientCount)
	case !strings.Contains(evil.BlockedBy, "malware"):
		t.Fatalf("evil blocked_by = %q, want it to name the malware category", evil.BlockedBy)
	case evil.Cached:
		t.Fatal("a freshly checked verdict is marked cached")
	case evil.FirstSeen == nil || !evil.FirstSeen.Equal(now.Add(-4*time.Hour)) || evil.LastSeen == nil || !evil.LastSeen.Equal(now.Add(-time.Minute)):
		t.Fatalf("evil seen range = %v .. %v", evil.FirstSeen, evil.LastSeen)
	}
	if safe := byName["safe.ait.test"]; safe.IsThreat || safe.QueryCount != 1 || safe.BlockedBy != "" {
		t.Fatalf("safe verdict = %+v", safe)
	}
	if cached := byName["cached.ait.test"]; !cached.Cached || !cached.IsThreat || len(cached.Categories) != 1 {
		t.Fatalf("cached verdict = %+v", cached)
	}

	got, err := threat.CachedVerdicts(ctx, st.Pool, []string{"EVIL.ait.test.", "safe.ait.test", "cached.ait.test"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("cached verdicts after the check = %+v", got)
	}
	if v := got["evil.ait.test"]; !v.IsThreat || !v.Cached || !v.CheckedAt.Equal(now) {
		t.Fatalf("stored evil verdict = %+v", v)
	}
}

// TestThreatCheckRejectsUnknownName catches a verdict for a domain nobody asked about being returned
// instead of being fed back to the model.
func TestThreatCheckRejectsUnknownName(t *testing.T) {
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := querylog.NewBuiltin(10)
	ingest(b, now.Add(-time.Hour), "10.0.0.1", "safe.ait.test.")

	other := map[string]any{"name": "other.test", "is_threat": true, "categories": []string{"malware"},
		"confidence": 0.9, "reasoning": "made up"}
	m := aifake.Model(aifake.JSON(verdicts(other)), aifake.JSON(verdicts(safeVerdict())))
	svc := aifake.Service(t, st, m, nil)
	res := runCheck(t, st, svc, b, now, "safe.ait.test")

	if n := len(m.RecordedCalls()); n != 2 {
		t.Fatalf("model calls = %d, want the invalid answer to be re-asked once", n)
	}
	if len(res.Results) != 1 || res.Results[0].Name != "safe.ait.test" || res.Results[0].IsThreat {
		t.Fatalf("results = %+v", res.Results)
	}
}
