package e2e

import (
	"encoding/json"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedAIForecasts) }

// insertForecastSQL writes one forecast the way the upstream-prediction and capacity agents would;
// the GUI seed takes this route because no API creates forecasts. Both rows are three hours old and
// stay valid for six more, so 55-ai-forecasts.spec.ts reads a fresh freshness line.
const insertForecastSQL = `INSERT INTO ai_forecasts (kind, subject, detail, generated_at, valid_until)
VALUES ($1, $2, $3::jsonb, now() - interval '3 hours', now() + interval '6 hours')`

// seedAIForecasts gives 55-ai-forecasts.spec.ts one upstream prediction for the fixture upstream
// (degrading, with a switch_strategy recommendation) and one capacity forecast for the recursor
// cache (12 days remaining), and tells the spec the fixture upstream's id.
//
// The capacity subject is recursor_cache, not filter_index: 50-ai-status.spec.ts runs the
// capacity_forecast agent, whose newer row would win Latest() for every resource it samples, and
// recursor_cache is the one resource it does not sample.
// debt: when M7 Task 12 gives the agent recursor cache samples this seed must move to a subject the
// agent still does not write, or run after the agent.
func seedAIForecasts(s guiSeedEnv) {
	var ups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	s.Admin.Must("GET", "/upstreams", nil, &ups, 200)
	id := ""
	for _, u := range ups {
		if u.Name == "fixture" {
			id = u.ID
		}
	}
	if id == "" {
		s.T.Fatalf("no fixture upstream to forecast: %+v", ups)
	}

	upstream, err := json.Marshal(map[string]any{
		"upstream_id": id, "upstream_name": "fixture", "trend": "degrading", "confidence": 0.78,
		"current_rtt_p50_ms": 120.0, "current_rtt_p99_ms": 350.0, "slope_ms_per_hour": 4.5,
		"step_change": false, "periodic_hours": []int{}, "projected_time_to_threshold": nil,
		"data_points_analyzed": 288,
		"reasoning":            "The p99 of fixture rose from 90 ms to 350 ms over the last 24 hours.",
		"recommendation": map[string]any{"type": "switch_strategy",
			"description": "Switch the upstream strategy to fastest so the slow upstream stops deciding answers."},
	})
	if err != nil {
		s.T.Fatalf("marshal upstream forecast: %v", err)
	}
	harness.PGExec(s.T, s.PGURL, insertForecastSQL, "upstream", id, string(upstream))

	capacity, err := json.Marshal(map[string]any{
		"resource": "recursor_cache", "current_value": 18_500_000.0, "max_value": 22_700_000.0,
		"growth_per_day": 350_000.0, "growth_per_week": 2_450_000.0,
		"projected_exhaustion_date": nil, "days_remaining": 12, "trend": "growing", "confidence": 0.66,
		"recommendation":  "Raise the recursor cache budget before the working set grows past it.",
		"points_analyzed": 168,
	})
	if err != nil {
		s.T.Fatalf("marshal capacity forecast: %v", err)
	}
	harness.PGExec(s.T, s.PGURL, insertForecastSQL, "capacity", "recursor_cache", string(capacity))

	s.Vars["NEXORA_E2E_AI_UPSTREAM_FORECAST"] = id
}
