package api

import "context"

// The M11 Task 16 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) CreateAiAssistantSession(context.Context, CreateAiAssistantSessionRequestObject) (CreateAiAssistantSessionResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "createAiAssistantSession is not implemented yet"}
}

func (h *handlers) GetAiAssistantSession(context.Context, GetAiAssistantSessionRequestObject) (GetAiAssistantSessionResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiAssistantSession is not implemented yet"}
}

func (h *handlers) PostAiAssistantMessage(context.Context, PostAiAssistantMessageRequestObject) (PostAiAssistantMessageResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "postAiAssistantMessage is not implemented yet"}
}
