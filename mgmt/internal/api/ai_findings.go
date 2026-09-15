package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
)

func (h *handlers) ListAiFindings(ctx context.Context, req ListAiFindingsRequestObject) (ListAiFindingsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	limit, err := limitParam(req.Params.Limit, 100, 500)
	if err != nil {
		return nil, err
	}
	f := finding.Filter{Limit: limit}
	if k := req.Params.Kind; k != nil {
		if !k.Valid() {
			return nil, invalid("kind must be anomaly or insight")
		}
		f.Kind = string(*k)
	}
	if s := req.Params.Status; s != nil {
		if !s.Valid() {
			return nil, invalid("status must be open, acknowledged, dismissed or resolved")
		}
		f.Status = string(*s)
	}
	list, err := finding.List(ctx, h.d.Store.Pool, f)
	if err != nil {
		return nil, err
	}
	out := make(ListAiFindings200JSONResponse, 0, len(list))
	for _, x := range list {
		a, err := toAiFinding(x)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (h *handlers) UpdateAiFinding(ctx context.Context, req UpdateAiFindingRequestObject) (UpdateAiFindingResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	if req.Body == nil || !req.Body.Status.Valid() {
		return nil, invalid("status must be acknowledged or dismissed")
	}
	f, err := finding.Update(ctx, h.d.Store, req.Id, string(req.Body.Status), PrincipalFrom(ctx).Actor())
	if err != nil {
		return nil, err
	}
	a, err := toAiFinding(f)
	if err != nil {
		return nil, err
	}
	return UpdateAiFinding200JSONResponse(a), nil
}

func toAiFinding(f finding.Finding) (AiFinding, error) {
	detail := map[string]any{}
	if err := json.Unmarshal(f.Detail, &detail); err != nil {
		return AiFinding{}, fmt.Errorf("finding %s detail: %w", f.ID, err)
	}
	return AiFinding{
		Id: f.ID, Kind: AiFindingKind(f.Kind), CandidateId: f.CandidateID, Type: f.Type, Status: AiFindingStatus(f.Status),
		Severity: AiFindingSeverity(f.Severity), Confidence: float32(f.Confidence), Title: f.Title, Description: f.Description,
		Detail: detail, Explained: f.Explained, FirstSeen: f.FirstSeen, LastSeen: f.LastSeen, UpdatedAt: f.UpdatedAt, UpdatedBy: f.UpdatedBy,
	}, nil
}
