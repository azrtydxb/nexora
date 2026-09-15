package rolloutrisk_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func assessment(score int, level string, version int64, rec any) any {
	m := map[string]any{"risk_score": score, "risk_level": level,
		"analysis":            "The policy group change repeats the change that halted the canary.",
		"historical_patterns": []any{map[string]any{"config_version": version}},
		"recommendation":      rec}
	return m
}

// TestRolloutRiskAgent catches a score whose level is not its band, a fabricated historical version,
// a rollback that reaches the model, an assessment that is not stored, and a recommendation that
// does not become an updateEngineGroup proposal.
func TestRolloutRiskAgent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	a := storetest.InsertEngine(t, st, "rra-a", store.DefaultEngineGroupID)
	storetest.InsertEngine(t, st, "rra-b", store.DefaultEngineGroupID)
	storetest.InsertEngine(t, st, "rra-c", store.DefaultEngineGroupID)

	phase, finished := old.Add(time.Hour), old.Add(70*time.Minute)
	insertRollout(t, st, rolloutSpec{version: 1, action: "updatePolicyGroup", targetType: "policy_group",
		state: "completed", strategy: "canary", createdAt: &old})
	insertRollout(t, st, rolloutSpec{version: 3, action: "updatePolicyGroup", targetType: "policy_group",
		state: "halted", strategy: "canary", haltReason: "canary servfail", canaries: []uuid.UUID{a},
		phaseStart: &phase, finished: &finished, createdAt: &old})
	changeAt := now.Add(-2 * time.Minute)
	change := insertRollout(t, st, rolloutSpec{version: 4, action: "updatePolicyGroup", targetType: "policy_group",
		state: "pending", strategy: "canary", createdAt: &changeAt})
	rollbackAt := now.Add(-time.Minute)
	rollback := insertRollout(t, st, rolloutSpec{version: 5, action: "rollbackEngineGroup", targetType: "engine_group",
		kind: "rollback", state: "completed", strategy: "all_at_once", createdAt: &rollbackAt})

	good := map[string]any{"strategy": "canary", "canary_count": 1, "min_health_queries": 200,
		"max_servfail_ratio": 0.03, "reasoning": "Verify this policy change on one engine first."}
	model := aifake.Model(
		aifake.JSON(assessment(8, "low", 3, good)),      // score 8 is high, not low
		aifake.JSON(assessment(5, "medium", 999, good)), // version 999 is not a similar rollout
		aifake.JSON(assessment(5, "medium", 3, good)),
	)
	agent := &rolloutrisk.Agent{Store: st, Service: aifake.Service(t, st, model, nil),
		Validator: &proposal.Validator{Store: st}, Now: func() time.Time { return now }}
	if agent.Name() != "rollout_risk" {
		t.Fatalf("name %q", agent.Name())
	}
	run := &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil || run.Outcome != "ok" {
		t.Fatalf("run: %v outcome %s", err, run.Outcome)
	}
	if calls := len(model.RecordedCalls()); calls != 3 {
		t.Fatalf("model calls = %d, want 3 (two rejected answers, then the valid one)", calls)
	}

	got, err := rolloutrisk.Get(ctx, st.Pool, change)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "assessed" || got.RiskScore == nil || *got.RiskScore != 5 || got.RiskLevel == nil || *got.RiskLevel != "medium" ||
		got.AssessedAt == nil || got.ProposalID == nil || got.Error != "" {
		t.Fatalf("change assessment = %+v", got)
	}
	var detail struct {
		HistoricalPatterns []struct {
			Version          int64   `json:"config_version"`
			Outcome          string  `json:"outcome"`
			CanaryRejected   bool    `json:"canary_rejected"`
			MaxServfailRatio float64 `json:"max_servfail_ratio"`
		} `json:"historical_patterns"`
		Recommendation *struct {
			Strategy         string  `json:"strategy"`
			CanaryCount      int     `json:"canary_count"`
			MinHealthQueries int     `json:"min_health_queries"`
			MaxServfailRatio float64 `json:"max_servfail_ratio"`
		} `json:"recommendation"`
	}
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.HistoricalPatterns) != 1 || detail.HistoricalPatterns[0].Version != 3 ||
		detail.HistoricalPatterns[0].Outcome != "halted" || !detail.HistoricalPatterns[0].CanaryRejected {
		t.Fatalf("stored historical patterns = %+v, want the computed history of version 3", detail.HistoricalPatterns)
	}
	if detail.Recommendation == nil || detail.Recommendation.Strategy != "canary" || detail.Recommendation.CanaryCount != 1 ||
		detail.Recommendation.MinHealthQueries != 200 || detail.Recommendation.MaxServfailRatio != 0.03 {
		t.Fatalf("stored recommendation = %+v", detail.Recommendation)
	}

	skipped, err := rolloutrisk.Get(ctx, st.Pool, rollback)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Status != "skipped" || skipped.RiskScore != nil || skipped.ProposalID != nil {
		t.Fatalf("rollback assessment = %+v, want skipped without a model call", skipped)
	}
	if _, err := rolloutrisk.Get(ctx, st.Pool, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get of an unknown rollout = %v, want store.ErrNotFound", err)
	}

	ps, err := proposal.List(ctx, st, proposal.Filter{Source: "rollout_risk"})
	if err != nil || len(ps) != 1 || len(ps[0].Actions) != 1 || ps[0].Actions[0].OperationID != "updateEngineGroup" {
		t.Fatalf("proposals = %+v %v", ps, err)
	}
	if ps[0].ID != *got.ProposalID {
		t.Fatalf("proposal %s is not the one the assessment names (%s)", ps[0].ID, *got.ProposalID)
	}
	var body map[string]any
	if err := json.Unmarshal(ps[0].Actions[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["rollout_strategy"] != "canary" || body["canary_count"] != float64(1) || body["min_health_queries"] != float64(200) ||
		body["max_servfail_ratio"] != 0.03 || body["revision"] != float64(1) || body["name"] != "default" {
		t.Fatalf("proposal body = %s", ps[0].Actions[0].Body)
	}

	// A second run has nothing left to do and never reaches the model.
	run2 := &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run2); err != nil || run2.Outcome != "no_change" {
		t.Fatalf("second run: %v outcome %s", err, run2.Outcome)
	}
	if calls := len(model.RecordedCalls()); calls != 3 {
		t.Fatalf("model calls after the second run = %d, want 3", calls)
	}
}
