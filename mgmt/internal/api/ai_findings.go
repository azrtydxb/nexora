package api

import "context"

// The M11 Task 7 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) ListAiFindings(context.Context, ListAiFindingsRequestObject) (ListAiFindingsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "listAiFindings is not implemented yet"}
}

func (h *handlers) UpdateAiFinding(context.Context, UpdateAiFindingRequestObject) (UpdateAiFindingResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "updateAiFinding is not implemented yet"}
}
