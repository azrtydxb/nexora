package api

import "context"

// The M11 Task 9 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) GetAiStatus(context.Context, GetAiStatusRequestObject) (GetAiStatusResponseObject, error) {
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiStatus is not implemented yet"}
}

func (h *handlers) RunAiAgent(context.Context, RunAiAgentRequestObject) (RunAiAgentResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "runAiAgent is not implemented yet"}
}
