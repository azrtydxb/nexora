package api

import "context"

// The M11 Task 19 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) StartAiThreatCheck(context.Context, StartAiThreatCheckRequestObject) (StartAiThreatCheckResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "startAiThreatCheck is not implemented yet"}
}

func (h *handlers) GetAiFilterListClassification(context.Context, GetAiFilterListClassificationRequestObject) (GetAiFilterListClassificationResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiFilterListClassification is not implemented yet"}
}
