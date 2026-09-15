package insight_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/ai/insight"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func answer(candidateID string) map[string]any {
	return map[string]any{"insights": []map[string]any{{
		"candidate_id": candidateID, "related_candidates": []string{"upstream_degraded:fx"}, "severity": "critical", "confidence": 0.8,
		"title": "SERVFAIL on edge-a follows upstream fx latency", "description": "edge-a fails queries because fx is slow",
		"possible_causes":     []map[string]any{{"cause": "upstream fx degraded", "confidence": 0.7, "supporting_candidates": []string{"upstream_degraded:fx"}}},
		"recommended_actions": []string{"Check upstream fx"},
	}}}
}

func TestDashboardInsightCorrelation(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Now()
	seedSpike(t, st, now)

	m := aifake.Model(aifake.JSON(answer("servfail_spike:unknown")), aifake.JSON(answer("servfail_spike:edge-a")))
	clock := now
	agent := &insight.Agent{Store: st, Service: aifake.Service(t, st, m, nil), MinLLMInterval: 5 * time.Minute, Now: func() time.Time { return clock }}
	if agent.Name() != "dashboard_insights" {
		t.Fatalf("name %q", agent.Name())
	}

	run := &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	calls := m.RecordedCalls()
	if len(calls) != 2 || run.Outcome != "ok" || run.Detail["llm_at"] == nil {
		t.Fatalf("an unknown candidate id must be re-asked once: %d calls, outcome %s, detail %v", len(calls), run.Outcome, run.Detail)
	}
	if last := calls[1].Messages[len(calls[1].Messages)-1]; !strings.Contains(text(last.Content), "servfail_spike:unknown") {
		t.Fatalf("the unknown id is not fed back: %+v", last)
	}
	list, err := finding.List(ctx, st.Pool, finding.Filter{Kind: "insight", Status: "open", Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("open insights = %d %v", len(list), err)
	}
	var spike finding.Finding
	for _, f := range list {
		if f.CandidateID == "servfail_spike:edge-a" {
			spike = f
		}
	}
	var detail struct {
		Related []string `json:"related_candidates"`
		Engines []string `json:"engines"`
		Causes  []struct {
			Cause      string   `json:"cause"`
			Supporting []string `json:"supporting_candidates"`
		} `json:"possible_causes"`
		Actions []string `json:"recommended_actions"`
	}
	if err := json.Unmarshal(spike.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if !spike.Explained || spike.Description != "edge-a fails queries because fx is slow" || len(detail.Causes) != 1 ||
		detail.Causes[0].Supporting[0] != "upstream_degraded:fx" || detail.Related[0] != "upstream_degraded:fx" ||
		len(detail.Engines) != 1 || detail.Engines[0] != "edge-a" || detail.Actions[0] != "Check upstream fx" {
		t.Fatalf("explained insight = %+v detail %s", spike, spike.Detail)
	}

	// The same candidates 30 s later: no model call.
	clock = now.Add(30 * time.Second)
	run = &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil || run.Outcome != "no_change" || len(m.RecordedCalls()) != 2 {
		t.Fatalf("unchanged run: %v outcome %s, %d calls", err, run.Outcome, len(m.RecordedCalls()))
	}

	// A new agent (another instance) with a failing model: the minimum interval survives in the run
	// detail, and a new candidate within it is stored unexplained without a call.
	if _, err := st.Pool.Exec(ctx, "insert into ai_agent_runs (agent, instance_id, started_at, outcome, detail) values ('dashboard_insights', 'i1', $1, 'ok', $2)",
		now, map[string]any{"llm_at": now}); err != nil {
		t.Fatal(err)
	}
	failing := aifake.Model()
	failing.Err = errors.New("model unavailable")
	other := &insight.Agent{Store: st, Service: aifake.Service(t, st, failing, nil), MinLLMInterval: 5 * time.Minute, Now: func() time.Time { return clock }}
	if _, err := st.Pool.Exec(ctx, "update engines set connected_instance = null where node_name = 'edge-b'"); err != nil {
		t.Fatal(err)
	}
	run = &ai.Run{Agent: other.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := other.Run(ctx, run); err != nil || run.Outcome != "ok" || len(failing.RecordedCalls()) != 0 {
		t.Fatalf("changed run within the interval: %v outcome %s, %d calls", err, run.Outcome, len(failing.RecordedCalls()))
	}
	// After the interval the pending change is explained; a failing model leaves it unexplained, outcome ok.
	clock = now.Add(6 * time.Minute)
	run = &ai.Run{Agent: other.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := other.Run(ctx, run); err != nil || run.Outcome != "ok" || len(failing.RecordedCalls()) == 0 {
		t.Fatalf("pending change after the interval: %v outcome %s, %d calls", err, run.Outcome, len(failing.RecordedCalls()))
	}
	list, err = finding.List(ctx, st.Pool, finding.Filter{Kind: "insight", Status: "open", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range list {
		if f.CandidateID == "engine_disconnected:edge-b" {
			found = true
			if f.Explained || f.Severity != "critical" {
				t.Fatalf("unexplained disconnect = %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("engine_disconnected:edge-b not stored: %+v", list)
	}
}

func text(parts any) string {
	b, _ := json.Marshal(parts)
	return string(b)
}
