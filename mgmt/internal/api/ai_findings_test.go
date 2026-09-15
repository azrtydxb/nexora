package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/api"
)

func TestFindingHandlers(t *testing.T) {
	op, viewer, env := roleClientsWith(t, func(d *api.Deps) { d.AI = &api.AIRuntime{} })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	c := finding.Candidate{ID: "dns_tunneling:10.0.1.45", Kind: "anomaly", Type: "dns_tunneling", Severity: "warning",
		Title: "DNS tunneling from 10.0.1.45", Description: "long random labels", Detail: map[string]any{"affected_clients": []string{"10.0.1.45"}}}
	if _, err := finding.Sync(ctx, env.st, "anomaly", []finding.Candidate{c}, now); err != nil {
		t.Fatal(err)
	}
	up := `{"upstream_id":"u1","upstream_name":"quad9","trend":"degrading","slope_ms_per_hour":2.5,"current_rtt_p50_ms":20,
		"current_rtt_p99_ms":90,"projected_time_to_threshold":null,"periodic_hours":[],"step_change":false,"confidence":0.8,
		"recommendation":{"type":"switch_strategy","description":"use parallel"},"reasoning":"rising","data_points_analyzed":288}`
	if err := forecast.Put(ctx, env.st, forecast.Forecast{Kind: "upstream", Subject: "u1", Detail: json.RawMessage(up),
		GeneratedAt: now, ValidUntil: now.Add(6 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var list []api.AiFinding
	if code := viewer.do(http.MethodGet, "/ai/findings?kind=anomaly", nil, &list); code != 200 || len(list) != 1 || list[0].CandidateId != c.ID {
		t.Fatalf("viewer list = %d %+v", code, list)
	}
	if code := viewer.do(http.MethodGet, "/ai/findings?kind=insight", nil, &list); code != 200 || len(list) != 0 {
		t.Fatalf("insight list = %d %+v", code, list)
	}
	path := "/ai/findings/" + list0(t, viewer).Id.String()
	var e apiErr
	if code := viewer.do(http.MethodPatch, path, map[string]string{"status": "acknowledged"}, &e); code != 403 {
		t.Fatalf("viewer patch = %d %+v", code, e)
	}
	var updated api.AiFinding
	if code := op.do(http.MethodPatch, path, map[string]string{"status": "acknowledged"}, &updated); code != 200 ||
		updated.Status != api.AiFindingStatusAcknowledged || updated.UpdatedBy != "opal" {
		t.Fatalf("operator acknowledge = %d %+v", code, updated)
	}
	if code := op.do(http.MethodPatch, path, map[string]string{"status": "open"}, &e); code != 400 || e.Code != "invalid_request" {
		t.Fatalf("patch to open = %d %+v", code, e)
	}
	if code := op.do(http.MethodPatch, "/ai/findings/00000000-0000-0000-0000-000000000001", map[string]string{"status": "dismissed"}, &e); code != 404 {
		t.Fatalf("patch of a missing finding = %d %+v", code, e)
	}

	var forecasts []api.AiForecast
	if code := viewer.do(http.MethodGet, "/ai/forecasts?kind=upstream", nil, &forecasts); code != 200 || len(forecasts) != 1 {
		t.Fatalf("forecasts = %d %+v", code, forecasts)
	}
	if f := forecasts[0]; f.Subject != "u1" || f.Capacity != nil || f.Upstream == nil || f.Upstream.Trend != "degrading" ||
		f.Upstream.Recommendation.Type != "switch_strategy" || f.Upstream.DataPointsAnalyzed != 288 {
		t.Fatalf("forecast = %+v upstream %+v", f, f.Upstream)
	}
	if code := viewer.do(http.MethodGet, "/ai/forecasts?kind=capacity", nil, &forecasts); code != 200 || len(forecasts) != 0 {
		t.Fatalf("capacity forecasts = %d %+v", code, forecasts)
	}

	// Without the AI runtime every one of these answers 503 ai_disabled.
	_, offViewer, _ := roleClientsWith(t, func(d *api.Deps) { d.AIDisabledReason = "NEXORA_AI_BASE_URL is not set" })
	for _, p := range []string{"/ai/findings", "/ai/forecasts"} {
		if code := offViewer.do(http.MethodGet, p, nil, &e); code != 503 || e.Code != "ai_disabled" {
			t.Fatalf("%s without AI = %d %+v", p, code, e)
		}
	}
}

func list0(t *testing.T, c *client) api.AiFinding {
	t.Helper()
	var list []api.AiFinding
	if code := c.do(http.MethodGet, "/ai/findings?kind=anomaly&status=open", nil, &list); code != 200 || len(list) != 1 {
		t.Fatalf("open anomalies = %d %+v", code, list)
	}
	return list[0]
}
