package api

import "context"

// The M11 Task 14 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) GetAiInsights(context.Context, GetAiInsightsRequestObject) (GetAiInsightsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiInsights is not implemented yet"}
}
