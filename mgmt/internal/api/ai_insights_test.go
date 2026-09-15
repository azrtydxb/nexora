package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/api"
)

func TestGetAiInsights(t *testing.T) {
	_, viewer, env := roleClientsWith(t, func(d *api.Deps) { d.AI = &api.AIRuntime{} })
	ctx := context.Background()

	var got api.AiInsights
	if code := viewer.do(http.MethodGet, "/ai/insights", nil, &got); code != 200 || got.Score != 0 ||
		got.Summary != "No active insights." || len(got.Insights) != 0 || got.GeneratedAt != nil {
		t.Fatalf("empty insights = %d %+v", code, got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	cands := []finding.Candidate{
		{ID: "servfail_spike:edge-a", Kind: "insight", Type: "servfail_spike", Severity: "critical", Title: "SERVFAIL spike on edge-a", Description: "15%"},
		{ID: "upstream_degraded:fx", Kind: "insight", Type: "upstream_degraded", Severity: "warning", Title: "Upstream fx is slow", Description: "300 ms"},
	}
	if _, err := finding.Sync(ctx, env.st, "insight", cands, now); err != nil {
		t.Fatal(err)
	}
	anomaly := finding.Candidate{ID: "nxdomain_burst:10.0.0.1", Kind: "anomaly", Type: "nxdomain_burst", Severity: "critical", Title: "t", Description: "d"}
	if _, err := finding.Sync(ctx, env.st, "anomaly", []finding.Candidate{anomaly}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if code := viewer.do(http.MethodGet, "/ai/insights", nil, &got); code != 200 || got.Score != 4 ||
		got.Summary != "2 active insight(s) across the fleet." || len(got.Insights) != 2 || got.GeneratedAt == nil || !got.GeneratedAt.Equal(now) {
		t.Fatalf("insights = %d %+v", code, got)
	}

	// An acknowledged insight stays listed but no longer counts towards the score.
	if _, err := env.st.Pool.Exec(ctx, "update ai_findings set status = 'acknowledged' where candidate_id = 'servfail_spike:edge-a'"); err != nil {
		t.Fatal(err)
	}
	if code := viewer.do(http.MethodGet, "/ai/insights", nil, &got); code != 200 || got.Score != 1 || len(got.Insights) != 2 {
		t.Fatalf("with an acknowledged insight = %d %+v", code, got)
	}

	var e apiErr
	_, offViewer, _ := roleClientsWith(t, func(d *api.Deps) { d.AIDisabledReason = "NEXORA_AI_BASE_URL is not set" })
	if code := offViewer.do(http.MethodGet, "/ai/insights", nil, &e); code != 503 || e.Code != "ai_disabled" {
		t.Fatalf("insights without AI = %d %+v", code, e)
	}
}
