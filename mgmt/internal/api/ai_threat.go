package api

import (
	"context"
	"strings"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/threat"
)

// maxThreatDomain is the longest accepted domain name of a check.
const maxThreatDomain = 253

// StartAiThreatCheck starts a threat check for the caller. Cached verdicts answer without a model call;
// nothing is ever blocked by the check itself.
func (h *handlers) StartAiThreatCheck(ctx context.Context, req StartAiThreatCheckRequestObject) (StartAiThreatCheckResponseObject, error) {
	if len(req.Body.Domains) < 1 || len(req.Body.Domains) > threat.MaxDomains {
		return nil, invalid("a check takes 1 to %d domains", threat.MaxDomains)
	}
	domains := make([]string, 0, len(req.Body.Domains))
	for _, d := range req.Body.Domains {
		name := strings.TrimSpace(d)
		if name == "" || len(name) > maxThreatDomain || strings.ContainsAny(name, " \t\r\n") {
			return nil, invalid("%q is not a domain name", d)
		}
		domains = append(domains, name)
	}
	task, err := h.startTask(ctx, ai.TaskThreatCheck, threat.Input{Domains: domains})
	if err != nil {
		return nil, err
	}
	return StartAiThreatCheck202JSONResponse(task), nil
}

// GetAiFilterListClassification reports what the classification agent estimates a block list blocks.
func (h *handlers) GetAiFilterListClassification(ctx context.Context, req GetAiFilterListClassificationRequestObject) (GetAiFilterListClassificationResponseObject, error) {
	if _, err := h.aiRuntime(); err != nil {
		return nil, err
	}
	c, err := threat.GetClassification(ctx, h.d.Store.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out := AiListClassification{ListId: c.ListID, BlobSha256: c.BlobSHA256, SampleSize: c.SampleSize,
		EntryCount: c.EntryCount, ClassifiedAt: c.ClassifiedAt}
	for _, b := range c.Breakdown {
		out.Breakdown = append(out.Breakdown, struct {
			Category  string `json:"category"`
			Estimated int64  `json:"estimated"`
			Sampled   int    `json:"sampled"`
		}{Category: b.Category, Estimated: b.Estimated, Sampled: b.Sampled})
	}
	return GetAiFilterListClassification200JSONResponse(out), nil
}
