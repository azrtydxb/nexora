package capacity_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/capacity"
	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

const cacheMax = 268435456 // resolver_settings.cache_max_bytes default

func sample(t *testing.T, st *store.Store, table, column string, engine any, at time.Time, s *controlv1.Stats) {
	t.Helper()
	raw, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(context.Background(), `insert into `+table+`(engine_id, `+column+`, stats) values ($1, $2, $3)`,
		engine, at, raw); err != nil {
		t.Fatal(err)
	}
}

func history(t *testing.T, st *store.Store, now time.Time, resource string, days int, value func(i int) float64, limit any) {
	t.Helper()
	for i := -days; i < 0; i++ {
		if _, err := st.Pool.Exec(context.Background(), `insert into ai_capacity_samples(day, resource, value, limit_value)
			values ($1, $2, $3, $4)`, now.AddDate(0, 0, i).Format(time.DateOnly), resource, value(i), limit); err != nil {
			t.Fatal(err)
		}
	}
}

type modelForecast struct {
	Resource       string  `json:"resource"`
	Confidence     float64 `json:"confidence"`
	Recommendation string  `json:"recommendation"`
	CacheMaxBytes  *int64  `json:"cache_max_bytes,omitempty"`
}

func answer(fs ...modelForecast) any { return map[string]any{"forecasts": fs} }

// TestCapacityForecastAgent catches duplicate daily samples, sampled values that are not the newest
// per engine or the fleet maximum, a limitless resource with an exhaustion date, a missing forecast
// for a resource with data, a cache_max_bytes proposal on another resource or not at all, and an
// accepted confidence above the cap of a resource with fewer than 7 points.
func TestCapacityForecastAgent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	a := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
	b := storetest.InsertEngine(t, st, "b", store.DefaultEngineGroupID)
	cacheToday := uint64(cacheMax / 10 * 9)
	index := func(bytes uint64) *controlv1.FilterIndexStats {
		return &controlv1.FilterIndexStats{Bytes: bytes, MaxBytes: 512 << 20}
	}
	// Engine a: an older, larger sample today and the newest one; engine b: smaller figures; yesterday's
	// sample is not today's.
	sample(t, st, "engine_stats", "at", a, now.AddDate(0, 0, -1), &controlv1.Stats{CacheBytes: cacheMax, FilterIndex: index(9e6)})
	sample(t, st, "engine_stats", "at", a, now.Add(-5*time.Hour), &controlv1.Stats{CacheBytes: 2 * cacheToday, FilterIndex: index(5e6)})
	sample(t, st, "engine_stats", "at", a, now.Add(-time.Hour), &controlv1.Stats{CacheBytes: cacheToday, FilterIndex: index(1e6),
		ProcessResidentBytes: 300 << 20, MemoryLimitBytes: 1 << 30})
	sample(t, st, "engine_stats", "at", b, now.Add(-time.Hour), &controlv1.Stats{CacheBytes: 1000, FilterIndex: index(1e5),
		ProcessResidentBytes: 100 << 20})
	// Query volume over 24 h: 4,000 queries, a restart (not counted), then 500 more.
	started := func(ms int64, queries uint64) *controlv1.Stats {
		return &controlv1.Stats{StartedUnixMs: ms, QueriesTotal: queries}
	}
	sample(t, st, "engine_stats_rollup", "bucket", a, now.Add(-30*time.Hour), started(1, 0))
	sample(t, st, "engine_stats_rollup", "bucket", a, now.Add(-20*time.Hour), started(1, 1000))
	sample(t, st, "engine_stats_rollup", "bucket", a, now.Add(-10*time.Hour), started(1, 5000))
	sample(t, st, "engine_stats_rollup", "bucket", a, now.Add(-5*time.Hour), started(2, 300))
	sample(t, st, "engine_stats_rollup", "bucket", a, now.Add(-2*time.Hour), started(2, 800))
	if _, err := st.Pool.Exec(ctx, `insert into filter_lists(name, kind, url, enabled, entry_count) values
		('ads', 'block', 'https://lists.example/ads', true, 150000), ('off', 'block', 'https://lists.example/off', false, 99),
		('ok', 'allow', 'https://lists.example/ok', true, 7)`); err != nil {
		t.Fatal(err)
	}

	history(t, st, now, "cache", 10, func(i int) float64 { return float64(cacheToday) + float64(i)*0.01*cacheMax }, cacheMax)
	history(t, st, now, "blocklist_entries", 10, func(i int) float64 { return 150000 + 100*float64(i) }, nil)
	history(t, st, now, "filter_index", 4, func(int) float64 { return 1e6 }, 512<<20)

	if err := capacity.SampleToday(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := capacity.SampleToday(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	today := map[string][2]*float64{}
	rows, err := st.Pool.Query(ctx, `select resource, value, limit_value from ai_capacity_samples where day = $1`, now.Format(time.DateOnly))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r string
		var v float64
		var l *float64
		if err := rows.Scan(&r, &v, &l); err != nil {
			t.Fatal(err)
		}
		if _, dup := today[r]; dup {
			t.Fatalf("duplicate sample for %s", r)
		}
		today[r] = [2]*float64{&v, l}
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	want := map[string][2]float64{"cache": {float64(cacheToday), cacheMax}, "filter_index": {1e6, 512 << 20},
		"engine_memory": {300 << 20, 1 << 30}, "blocklist_entries": {150000, -1}, "query_volume": {4500, -1}}
	if len(today) != len(want) {
		t.Fatalf("today's samples %v, want %v", today, want)
	}
	for r, w := range want {
		got := today[r]
		if got[0] == nil || *got[0] != w[0] || (w[1] < 0) != (got[1] == nil) || (got[1] != nil && *got[1] != w[1]) {
			t.Fatalf("sample %s = %v/%v, want %v", r, got[0], got[1], w)
		}
	}

	double := int64(2 * cacheMax)
	model := aifake.Model(
		// filter_index has 5 points: confidence 0.9 is above its 0.5 cap, so the model is asked again.
		aifake.JSON(answer(
			modelForecast{Resource: "cache", Confidence: 0.8, Recommendation: "Raise the cache limit.", CacheMaxBytes: &double},
			modelForecast{Resource: "filter_index", Confidence: 0.9, Recommendation: "Fine."},
			modelForecast{Resource: "blocklist_entries", Confidence: 0.7, Recommendation: "Stable."})),
		aifake.JSON(answer(
			modelForecast{Resource: "cache", Confidence: 0.8, Recommendation: "Raise the cache limit.", CacheMaxBytes: &double},
			modelForecast{Resource: "filter_index", Confidence: 0.4, Recommendation: "Fine."},
			modelForecast{Resource: "blocklist_entries", Confidence: 0.7, Recommendation: "Stable."})),
	)
	agent := &capacity.Agent{Store: st, Service: aifake.Service(t, st, model, nil), Validator: &proposal.Validator{Store: st},
		Interval: 24 * time.Hour, Now: func() time.Time { return now }}
	if agent.Name() != "capacity_forecast" {
		t.Fatalf("name %q", agent.Name())
	}
	run := &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil || run.Outcome != "ok" {
		t.Fatalf("run: %v outcome %s", err, run.Outcome)
	}
	if calls := len(model.RecordedCalls()); calls != 2 {
		t.Fatalf("model calls = %d, want 2 (one re-ask)", calls)
	}
	var samples int
	if err := st.Pool.QueryRow(ctx, `select count(*) from ai_capacity_samples where day = $1`, now.Format(time.DateOnly)).Scan(&samples); err != nil || samples != len(want) {
		t.Fatalf("today's samples after the run = %d %v", samples, err)
	}

	fs, err := forecast.Latest(ctx, st.Pool, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	type detail struct {
		Resource       string   `json:"resource"`
		CurrentValue   float64  `json:"current_value"`
		MaxValue       *float64 `json:"max_value"`
		ExhaustionDate *string  `json:"projected_exhaustion_date"`
		DaysRemaining  *int     `json:"days_remaining"`
		Trend          string   `json:"trend"`
		Confidence     float64  `json:"confidence"`
		Recommendation string   `json:"recommendation"`
		PointsAnalyzed int      `json:"points_analyzed"`
		GrowthPerDay   *float64 `json:"growth_per_day"`
		GrowthPerWeek  *float64 `json:"growth_per_week"`
	}
	got := map[string]detail{}
	for _, f := range fs {
		var d detail
		if err := json.Unmarshal(f.Detail, &d); err != nil {
			t.Fatal(err)
		}
		if d.Resource != f.Subject || !f.GeneratedAt.Equal(now) || !f.ValidUntil.Equal(now.Add(24*time.Hour)) ||
			d.GrowthPerDay == nil || d.GrowthPerWeek == nil {
			t.Fatalf("forecast %s: %+v %s", f.Subject, f, f.Detail)
		}
		if (f.ProposalID != nil) != (f.Subject == "cache") {
			t.Fatalf("forecast %s proposal %v", f.Subject, f.ProposalID)
		}
		got[f.Subject] = d
	}
	if len(got) != len(want) {
		t.Fatalf("forecasts for %v, want one per sampled resource %v", got, want)
	}
	if c := got["cache"]; c.Trend != "growing" || c.ExhaustionDate == nil || c.DaysRemaining == nil || *c.DaysRemaining != 10 ||
		c.Confidence != 0.8 || c.PointsAnalyzed != 11 || c.MaxValue == nil || *c.MaxValue != cacheMax {
		t.Fatalf("cache forecast %+v", c)
	}
	if bl := got["blocklist_entries"]; bl.ExhaustionDate != nil || bl.DaysRemaining != nil || bl.MaxValue != nil || bl.Trend != "stable" {
		t.Fatalf("blocklist_entries forecast %+v", bl)
	}
	if fi := got["filter_index"]; fi.Confidence != 0.4 || fi.PointsAnalyzed != 5 {
		t.Fatalf("filter_index forecast %+v", fi)
	}
	if m := got["engine_memory"]; m.Trend != "insufficient_data" || m.Confidence != 0 || m.PointsAnalyzed != 1 {
		t.Fatalf("engine_memory forecast %+v", m)
	}

	ps, err := proposal.List(ctx, st, proposal.Filter{Source: "capacity_forecast"})
	if err != nil || len(ps) != 1 || len(ps[0].Actions) != 1 || ps[0].Actions[0].OperationID != "updateResolverSettings" {
		t.Fatalf("proposals %+v %v", ps, err)
	}
	var body map[string]any
	if err := json.Unmarshal(ps[0].Actions[0].Body, &body); err != nil || body["cache_max_bytes"] != float64(double) ||
		body["revision"] != float64(1) || body["strategy"] != "ordered" {
		t.Fatalf("proposal body %s %v", ps[0].Actions[0].Body, err)
	}
}

// TestCapacityForecastInsufficientData catches a model call when no resource has 3 daily points.
func TestCapacityForecastInsufficientData(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	e := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
	sample(t, st, "engine_stats", "at", e, now.Add(-time.Minute), &controlv1.Stats{CacheBytes: 1 << 20})

	model := aifake.Model() // any call panics: no responses
	agent := &capacity.Agent{Store: st, Service: aifake.Service(t, st, model, nil), Validator: &proposal.Validator{Store: st},
		Interval: 24 * time.Hour, Now: func() time.Time { return now }}
	run := &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	fs, err := forecast.Latest(ctx, st.Pool, "capacity")
	if err != nil || len(fs) == 0 {
		t.Fatalf("forecasts %v %v", fs, err)
	}
	for _, f := range fs {
		var d struct{ Trend string }
		if err := json.Unmarshal(f.Detail, &d); err != nil || d.Trend != "insufficient_data" {
			t.Fatalf("forecast %s: %s", f.Subject, f.Detail)
		}
	}
	if calls := len(model.RecordedCalls()); calls != 0 {
		t.Fatalf("model calls = %d, want 0", calls)
	}
}
