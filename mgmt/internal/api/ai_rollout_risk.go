package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk"
)

// GetAiRolloutRisk returns one rollout's risk assessment. A rollout the agent has not reached yet is
// "pending"; an unknown rollout is 404.
func (h *handlers) GetAiRolloutRisk(ctx context.Context, req GetAiRolloutRiskRequestObject) (GetAiRolloutRiskResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	r, err := rolloutrisk.Get(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	// The stored detail holds historical_patterns and recommendation under the response's own names.
	var out AiRolloutRisk
	if err := json.Unmarshal(r.Detail, &out); err != nil {
		return nil, fmt.Errorf("rollout risk %s detail: %w", r.RolloutID, err)
	}
	out.RolloutId, out.Status = r.RolloutID, AiRolloutRiskStatus(r.Status)
	out.Analysis, out.Error = r.Analysis, r.Error
	out.RiskScore, out.ProposalId, out.AssessedAt = r.RiskScore, r.ProposalID, r.AssessedAt
	if r.RiskLevel != nil {
		level := AiRolloutRiskRiskLevel(*r.RiskLevel)
		out.RiskLevel = &level
	}
	return GetAiRolloutRisk200JSONResponse(out), nil
}
