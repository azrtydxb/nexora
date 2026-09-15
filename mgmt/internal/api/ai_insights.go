package api

import (
	"context"
	"fmt"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/ai/insight"
)

// maxActiveInsights bounds each status read. Insights are at most a few per engine and upstream.
const maxActiveInsights = 1000

// GetAiInsights returns the open and acknowledged insights, the score over the open ones, a one-line
// summary and the newest last_seen. Nothing here waits for a model call.
func (h *handlers) GetAiInsights(ctx context.Context, _ GetAiInsightsRequestObject) (GetAiInsightsResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	open, err := finding.List(ctx, h.d.Store.Pool, finding.Filter{Kind: insight.Kind, Status: "open", Limit: maxActiveInsights})
	if err != nil {
		return nil, err
	}
	acked, err := finding.List(ctx, h.d.Store.Pool, finding.Filter{Kind: insight.Kind, Status: "acknowledged", Limit: maxActiveInsights})
	if err != nil {
		return nil, err
	}
	out := GetAiInsights200JSONResponse{Insights: []AiFinding{}, Score: insight.Score(open), Summary: "No active insights."}
	var newest *time.Time
	for _, f := range append(open, acked...) {
		a, err := toAiFinding(f)
		if err != nil {
			return nil, err
		}
		out.Insights = append(out.Insights, a)
		if newest == nil || f.LastSeen.After(*newest) {
			newest = &f.LastSeen
		}
	}
	if n := len(out.Insights); n > 0 {
		out.Summary = fmt.Sprintf("%d active insight(s) across the fleet.", n)
	}
	out.GeneratedAt = newest
	return out, nil
}
