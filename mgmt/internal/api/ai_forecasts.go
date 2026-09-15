package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
)

func (h *handlers) ListAiForecasts(ctx context.Context, req ListAiForecastsRequestObject) (ListAiForecastsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	kind := ""
	if k := req.Params.Kind; k != nil {
		if !k.Valid() {
			return nil, invalid("kind must be upstream or capacity")
		}
		kind = string(*k)
	}
	list, err := forecast.Latest(ctx, h.d.Store.Pool, kind)
	if err != nil {
		return nil, err
	}
	out := make(ListAiForecasts200JSONResponse, 0, len(list))
	for _, f := range list {
		a := AiForecast{Id: f.ID, Kind: AiForecastKind(f.Kind), Subject: f.Subject, ProposalId: f.ProposalID,
			GeneratedAt: f.GeneratedAt, ValidUntil: f.ValidUntil}
		// The stored detail is the kind's forecast object; it fills exactly one of upstream and capacity.
		var target any = &a.Capacity
		if f.Kind == "upstream" {
			target = &a.Upstream
		}
		if err := json.Unmarshal(f.Detail, target); err != nil {
			return nil, fmt.Errorf("forecast %s detail: %w", f.ID, err)
		}
		out = append(out, a)
	}
	return out, nil
}
