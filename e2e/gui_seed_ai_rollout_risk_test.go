package e2e

import (
	"net/http"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIRolloutRisk) }

// insertRolloutRiskSQL writes one assessment the way the rollout risk agent's persist step would;
// the GUI seed takes this route because no API creates assessments.
const insertRolloutRiskSQL = `INSERT INTO ai_rollout_risks
  (rollout_id, status, risk_score, risk_level, analysis, detail, assessed_at)
VALUES ($1, 'assessed', $2, $3, $4, $5::jsonb, now() - interval '3 minutes')
ON CONFLICT (rollout_id) DO UPDATE SET status = excluded.status, risk_score = excluded.risk_score,
  risk_level = excluded.risk_level, analysis = excluded.analysis, detail = excluded.detail,
  assessed_at = excluded.assessed_at`

// seedAIRolloutRisk gives 56-ai-rollout-risk.spec.ts the assessed rollout it reads: the newest
// rollout of the default engine group, scored 5/10 with one historical pattern and a recommendation.
func seedAIRolloutRisk(s guiSeedEnv) {
	var rollouts []harness.RolloutView
	s.Admin.Must(http.MethodGet, "/rollouts?limit=1&engine_group_id="+harness.DefaultEngineGroupID, nil, &rollouts, http.StatusOK)
	if len(rollouts) == 0 {
		s.T.Fatal("no rollout of the default engine group to assess")
	}
	detail := `{"historical_patterns":[{"config_version":3,"description":"Filter list swap of the same size",` +
		`"outcome":"halted","canary_rejected":true,"halt_reason":"canary rejected the snapshot",` +
		`"max_servfail_ratio":0.12,"similarity":0.81}],` +
		`"recommendation":{"strategy":"canary","canary_count":1,"min_health_queries":50,"max_servfail_ratio":0.05,` +
		`"reasoning":"One canary first, because the comparable change was rejected on a canary."}}`
	harness.PGExec(s.T, s.PGURL, insertRolloutRiskSQL, rollouts[0].ID, 5, "medium",
		"Similar changes halted once.", detail)
	s.Vars["NEXORA_E2E_AI_ROLLOUT_ID"] = rollouts[0].ID
}
