package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

// riskView is the part of AiRolloutRisk this test inspects.
type riskView struct {
	RolloutID string `json:"rollout_id"`
	Status    string `json:"status"`
	RiskScore *int   `json:"risk_score"`
	Analysis  string `json:"analysis"`
	Error     string `json:"error"`
}

// rolloutRisk reads one rollout's assessment.
func rolloutRisk(t *testing.T, api *harness.API, rolloutID string) riskView {
	t.Helper()
	var r riskView
	api.Must(http.MethodGet, "/rollouts/"+rolloutID+"/ai-risk", nil, &r, http.StatusOK)
	return r
}

// waitRisksSettled waits until no existing rollout is still waiting for the agent, so the next
// scripted answer belongs to the rollout the test is about to create.
func waitRisksSettled(t *testing.T, api *harness.API) {
	t.Helper()
	harness.EventuallyTrue(t, 120*time.Second, func() bool {
		var rs []harness.RolloutView
		api.Must(http.MethodGet, "/rollouts?limit=100", nil, &rs, http.StatusOK)
		for _, r := range rs {
			if rolloutRisk(t, api, r.ID).Status == "pending" {
				return false
			}
		}
		return true
	}, "every rollout created before the test has a risk row")
}

// TestRolloutNotDelayedByAI catches a rollout risk assessment that runs inside the write path: the
// model answer takes two minutes, and neither the configuration write nor the rollout may wait for
// it. It also catches an assessment that never arrives afterwards.
func TestRolloutNotDelayedByAI(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx,
		"NEXORA_AI_ROLLOUT_RISK_ENABLED=true", "NEXORA_AI_AGENT_START_DELAY=0s")})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	env.StartManagedEngine("ai-rollout-1", []string{mg.GRPCURL}, admin.CreateJoinToken())
	waitLatestApplied(t, admin, "ai-rollout-1")

	var group struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	admin.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}},
		&group, http.StatusCreated)
	waitLatestApplied(t, admin, "ai-rollout-1")
	waitRisksSettled(t, admin)

	answer, err := json.Marshal(map[string]any{"risk_score": 5, "risk_level": "medium",
		"analysis": "A policy group change reaches every client of the group.", "historical_patterns": []any{},
		"recommendation": nil})
	if err != nil {
		t.Fatal(err)
	}
	fx.Script(t, "rollout_risk", harness.OpenAIResponse{Content: string(answer), DelayMS: 120000,
		PromptTokens: 120, CompletionTokens: 80})

	// The change must reach the engines: a rollout whose snapshot content equals the previous one is
	// skipped, so widening the group's CIDRs is what makes this an assessable configuration change.
	start := time.Now()
	admin.Must(http.MethodPut, "/policy-groups/"+group.ID, map[string]any{"name": "guests",
		"cidrs": []string{"10.9.0.0/16", "10.10.0.0/16"}, "description": "operator change", "revision": group.Revision},
		&group, http.StatusOK)
	if wrote := time.Since(start); wrote > 2*time.Second {
		t.Fatalf("the policy group write took %s: it waited for the AI assessment", wrote)
	}
	version := admin.LatestVersion()
	r := admin.WaitRollout(harness.DefaultEngineGroupID, version, 30*time.Second, "completed")

	if got := rolloutRisk(t, admin, r.ID); got.Status != "pending" {
		t.Fatalf("risk of the completed rollout = %+v, want pending while the model is still answering", got)
	}
	var assessed riskView
	harness.EventuallyTrue(t, 200*time.Second, func() bool {
		assessed = rolloutRisk(t, admin, r.ID)
		return assessed.Status != "pending"
	}, "the assessment arrives after the model answers")
	if assessed.Status != "assessed" || assessed.RiskScore == nil || *assessed.RiskScore != 5 || assessed.Analysis == "" {
		t.Fatalf("assessment = %+v (error %q)", assessed, assessed.Error)
	}
}
