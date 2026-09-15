package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// GetAiTask reads a task; anyone but its requester or an admin gets 404, so task ids reveal nothing.
func (h *handlers) GetAiTask(ctx context.Context, req GetAiTaskRequestObject) (GetAiTaskResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	t, err := ai.GetTask(ctx, h.d.Store, req.Id)
	if err != nil {
		return nil, err
	}
	p := PrincipalFrom(ctx)
	if p.Role != auth.RoleAdmin && (t.RequesterKind != p.Kind || t.RequestedBy != requesterID(p)) {
		return nil, store.ErrNotFound
	}
	return GetAiTask200JSONResponse(taskOut(t)), nil
}

// requesterID is the ai_tasks.requested_by of p (ai.Tasks.Start records the same).
func requesterID(p auth.Principal) string {
	if p.Kind == "api_token" {
		return p.TokenID
	}
	return p.UserID
}

// startTask starts a task of kind for the caller and returns the 202 body; a kind without a registered
// implementation answers 503 feature_disabled.
func (h *handlers) startTask(ctx context.Context, kind ai.TaskKind, input any) (AiTask, error) {
	rt, err := h.aiRuntime()
	if err != nil {
		return AiTask{}, err
	}
	if !rt.TaskKinds[kind] {
		return AiTask{}, apiError{status: http.StatusServiceUnavailable, code: "feature_disabled", msg: "the AI feature " + string(kind) + " is disabled"}
	}
	t, err := rt.Tasks.Start(kind, PrincipalFrom(ctx), input)
	if err != nil {
		return AiTask{}, err
	}
	return taskOut(t), nil
}

func taskOut(t ai.Task) AiTask {
	out := AiTask{Id: t.ID, Kind: AiTaskKind(t.Kind), Status: AiTaskStatus(t.Status), ErrorCode: t.ErrorCode,
		ErrorMessage: t.ErrorMessage, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt}
	if len(t.Result) > 0 {
		var result map[string]any
		if err := json.Unmarshal(t.Result, &result); err == nil && result != nil {
			out.Result = &result
		}
	}
	return out
}
