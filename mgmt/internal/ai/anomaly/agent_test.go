package anomaly_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/anomaly"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func attr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// ingest feeds recs into b over OTLP, as an engine does.
func ingest(b *querylog.Builtin, recs []querylog.Record) {
	var lrs []*logspb.LogRecord
	for _, r := range recs {
		lrs = append(lrs, &logspb.LogRecord{TimeUnixNano: uint64(r.Time.UnixNano()), Attributes: []*commonpb.KeyValue{
			attr("client.address", r.Client), attr("dns.question.name", r.Name), attr("dns.question.type", r.QType),
			attr("dns.response.code", r.RCode), attr("nexora.filter", r.Filter),
		}})
	}
	b.Ingest("e1", &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: lrs}}}}})
}

// tunnel returns n high-entropy queries from client spread over (from, from+span).
func tunnel(client, qtype, rcode string, n int, from time.Time, span time.Duration) []querylog.Record {
	var out []querylog.Record
	for i := 0; i < n; i++ {
		out = append(out, rec(client, randomLabel(i, 32)+".tunnel.bad.example.", qtype, rcode, from.Add(span*time.Duration(i+1)/time.Duration(n+1))))
	}
	return out
}

// runAt runs the agent as the scheduler does: a run row, the run, then its outcome.
func runAt(t *testing.T, st *store.Store, a *anomaly.Agent, now time.Time) (*ai.Run, error) {
	t.Helper()
	ctx := context.Background()
	run := &ai.Run{Agent: a.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := st.Pool.QueryRow(ctx, `insert into ai_agent_runs(agent, instance_id, started_at) values ($1, 'test', $2) returning id`,
		a.Name(), now).Scan(&run.ID); err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return now }
	err := a.Run(ctx, run)
	outcome := run.Outcome
	if err != nil {
		outcome = "failed"
	}
	if _, uerr := st.Pool.Exec(ctx, `update ai_agent_runs set finished_at = $2, outcome = $3, detail = $4 where id = $1`,
		run.ID, now, outcome, run.Detail); uerr != nil {
		t.Fatal(uerr)
	}
	return run, err
}

func findings(t *testing.T, st *store.Store, status string) map[string]finding.Finding {
	t.Helper()
	list, err := finding.List(context.Background(), st.Pool, finding.Filter{Kind: "anomaly", Status: status, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]finding.Finding{}
	for _, f := range list {
		out[f.CandidateID] = f
	}
	return out
}

func explanation(id, severity, description string) map[string]any {
	return map[string]any{"candidate_id": id, "severity": severity, "confidence": 0.8, "title": "t " + id,
		"description": description, "recommended_actions": []string{"Block tunnel.bad.example"}}
}

// TestAnomalyAgentLLMOnlyOnChange catches a model called on every tick, a candidate set change that is
// never explained, a lost finding when the model fails, and a finding that never resolves.
func TestAnomalyAgentLLMOnlyOnChange(t *testing.T) {
	st := storetest.New(t)
	b := querylog.NewBuiltin(10_000)
	t0 := time.Now().UTC().Truncate(time.Second)
	model := aifake.Model(
		aifake.JSON(map[string]any{"anomalies": []any{explanation("dns_tunneling:10.0.1.45", "critical", "model says tunnel")}}),
		aifake.JSON(map[string]any{"anomalies": []any{
			explanation("dns_tunneling:10.0.1.45", "critical", "model says tunnel again"),
			explanation("nxdomain_burst:10.0.1.45", "warning", "model says nxdomain"),
		}}),
	)
	svc := aifake.Service(t, st, model, nil)
	newAgent := func(s *ai.Service) *anomaly.Agent {
		return &anomaly.Agent{Store: st, Service: s, QueryLog: b, Interval: time.Minute, MinLLMInterval: 5 * time.Minute}
	}

	// Run 1: a new candidate set is explained by one model call.
	ingest(b, tunnel("10.0.1.45", "A", "NOERROR", 50, t0.Add(-time.Minute), time.Minute))
	a := newAgent(svc)
	if run, err := runAt(t, st, a, t0); err != nil || run.Outcome != "ok" {
		t.Fatalf("run 1: %+v %v", run, err)
	}
	if n := len(model.RecordedCalls()); n != 1 {
		t.Fatalf("run 1 model calls = %d, want 1", n)
	}
	if f := findings(t, st, "open")["dns_tunneling:10.0.1.45"]; !f.Explained || f.Description != "model says tunnel" {
		t.Fatalf("run 1 finding: %+v", f)
	}

	// Run 2, on another instance 30 s later: the same traffic gives the same set, and no model call.
	ingest(b, tunnel("10.0.1.45", "A", "NOERROR", 50, t0, 30*time.Second))
	if run, err := runAt(t, st, newAgent(svc), t0.Add(30*time.Second)); err != nil || run.Outcome != "no_change" {
		t.Fatalf("run 2: %+v %v", run, err)
	}
	if n := len(model.RecordedCalls()); n != 1 {
		t.Fatalf("run 2 model calls = %d, want still 1", n)
	}
	if f := findings(t, st, "open")["dns_tunneling:10.0.1.45"]; !f.LastSeen.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("run 2 did not see the candidate again: %+v", f)
	}

	// Run 3 at +2 min: 100 NXDOMAIN tunnelling queries add nxdomain_burst, but the last call is under
	// MinLLMInterval old, so the change waits.
	ingest(b, tunnel("10.0.1.45", "TXT", "NXDOMAIN", 100, t0.Add(30*time.Second), 90*time.Second))
	if run, err := runAt(t, st, a, t0.Add(2*time.Minute)); err != nil || run.Outcome != "ok" {
		t.Fatalf("run 3: %+v %v", run, err)
	}
	if n := len(model.RecordedCalls()); n != 1 {
		t.Fatalf("run 3 model calls = %d, want still 1 within MinLLMInterval", n)
	}
	if f := findings(t, st, "open")["nxdomain_burst:10.0.1.45"]; f.Explained || f.Severity != "warning" {
		t.Fatalf("run 3 nxdomain finding: %+v", f)
	}

	// Run 4 at +6 min: the same set again, and the pending change is explained by one call.
	ingest(b, tunnel("10.0.1.45", "TXT", "NXDOMAIN", 100, t0.Add(2*time.Minute), 4*time.Minute))
	if run, err := runAt(t, st, a, t0.Add(6*time.Minute)); err != nil || run.Outcome != "ok" || run.Detail["llm_at"] == nil {
		t.Fatalf("run 4: %+v %v", run, err)
	}
	if n := len(model.RecordedCalls()); n != 2 {
		t.Fatalf("run 4 model calls = %d, want 2", n)
	}
	open := findings(t, st, "open")
	if f := open["nxdomain_burst:10.0.1.45"]; !f.Explained || f.Description != "model says nxdomain" {
		t.Fatalf("run 4 nxdomain finding: %+v", f)
	}

	// Run 5 at +12 min: a new TXT client while the model fails; the finding keeps the detector's text. while the model fails; the finding keeps the detector's text.
	ingest(b, tunnel("10.0.1.99", "TXT", "NOERROR", 30, t0.Add(6*time.Minute), 5*time.Minute))
	failing := &aitest.MockModel{Err: errors.New("model down")}
	a.Service = aifake.Service(t, st, failing, nil)
	if run, err := runAt(t, st, a, t0.Add(12*time.Minute)); err != nil || run.Outcome != "ok" {
		t.Fatalf("run 5: %+v %v", run, err)
	}
	if len(failing.RecordedCalls()) == 0 {
		t.Fatal("run 5 did not call the model for a changed set")
	}
	if f := findings(t, st, "open")["dns_tunneling:10.0.1.99"]; f.Explained || f.Description == "" || f.Severity != "critical" {
		t.Fatalf("run 5 unexplained finding: %+v", f)
	}

	// Run 6 at +43 min: nothing detected for more than 30 min resolves every finding without a call.
	calls := len(failing.RecordedCalls())
	if run, err := runAt(t, st, a, t0.Add(43*time.Minute)); err != nil || run.Outcome != "no_change" {
		t.Fatalf("run 6: %+v %v", run, err)
	}
	if len(failing.RecordedCalls()) != calls {
		t.Fatal("run 6 called the model without candidates")
	}
	resolved := findings(t, st, "resolved")
	for _, id := range []string{"dns_tunneling:10.0.1.45", "nxdomain_burst:10.0.1.45", "dns_tunneling:10.0.1.99"} {
		if _, ok := resolved[id]; !ok {
			t.Fatalf("%s not resolved after 30 min: %+v", id, resolved)
		}
	}
	if len(findings(t, st, "open")) != 0 {
		t.Fatal("findings still open")
	}
}

type unavailable struct{ querylog.Noop }

func (unavailable) Name() string { return "opensearch" }
func (unavailable) Search(context.Context, querylog.Query) (querylog.Page, error) {
	return querylog.Page{}, querylog.ErrBackendUnavailable
}

// TestAnomalyAgentOutcomes catches a budget skip reported as ok or failed, work on a disabled query log,
// and an unavailable backend treated as a quiet window.
func TestAnomalyAgentOutcomes(t *testing.T) {
	st := storetest.New(t)
	b := querylog.NewBuiltin(1000)
	t0 := time.Now().UTC().Truncate(time.Second)
	ingest(b, tunnel("10.0.7.1", "A", "NOERROR", 50, t0.Add(-time.Minute), time.Minute))
	if _, err := st.Pool.Exec(context.Background(), `insert into ai_usage(day, feature, requests, input_tokens, output_tokens, reasoning_tokens)
		values ($1, 'querylog_anomalies', 1, 900, 100, 0)`, t0.Truncate(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	model := aifake.Model()
	exhausted := aifake.Service(t, st, model, func(c *config.AIConfig) { c.DailyTokenBudget = 1000 })
	a := &anomaly.Agent{Store: st, Service: exhausted, QueryLog: b, Interval: time.Minute, MinLLMInterval: 5 * time.Minute}
	if run, err := runAt(t, st, a, t0); err != nil || run.Outcome != "skipped_budget" {
		t.Fatalf("budget run: %+v %v", run, err)
	}
	if f := findings(t, st, "open")["dns_tunneling:10.0.7.1"]; f.Explained || f.CandidateID == "" {
		t.Fatalf("budget run did not store the unexplained finding: %+v", f)
	}
	if len(model.RecordedCalls()) != 0 {
		t.Fatal("exhausted budget still called the model")
	}

	a.QueryLog = querylog.Noop{}
	if run, err := runAt(t, st, a, t0.Add(time.Minute)); err != nil || run.Outcome != "no_change" {
		t.Fatalf("noop backend run: %+v %v", run, err)
	}
	a.QueryLog = unavailable{}
	if _, err := runAt(t, st, a, t0.Add(2*time.Minute)); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Fatalf("unavailable backend: err = %v", err)
	}
}
