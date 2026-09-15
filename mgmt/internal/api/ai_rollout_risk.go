package api

import "context"

// The M11 Task 18 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) GetAiRolloutRisk(context.Context, GetAiRolloutRiskRequestObject) (GetAiRolloutRiskResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiRolloutRisk is not implemented yet"}
}
