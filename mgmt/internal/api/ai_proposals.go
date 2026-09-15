package api

import "context"

// The M11 Task 6 handlers replace these stubs: 503 ai_disabled while AI is off, 501 otherwise.

func (h *handlers) ListAiProposals(context.Context, ListAiProposalsRequestObject) (ListAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "listAiProposals is not implemented yet"}
}

func (h *handlers) GetAiProposal(context.Context, GetAiProposalRequestObject) (GetAiProposalResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "getAiProposal is not implemented yet"}
}

func (h *handlers) ApplyAiProposals(context.Context, ApplyAiProposalsRequestObject) (ApplyAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "applyAiProposals is not implemented yet"}
}

func (h *handlers) DismissAiProposals(context.Context, DismissAiProposalsRequestObject) (DismissAiProposalsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	return nil, apiError{status: 501, code: "not_implemented", msg: "dismissAiProposals is not implemented yet"}
}
