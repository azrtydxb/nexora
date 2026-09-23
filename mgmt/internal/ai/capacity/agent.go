package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// AgentName is the agent and feature label.
const AgentName = "capacity_forecast"

const (
	// historyDays is how far back the projection reads samples (their retention).
	historyDays = 400
	// urgentDays: a cache proposal whose exhaustion is this close is high priority.
	urgentDays   = 30
	noDataAdvice = "Fewer than 3 daily samples: no projection yet."
	system       = `You review capacity projections of a Nexora DNS fleet. Code already computed each resource's current value,
limit, least-squares growth per day and week, trend, projected exhaustion date and days remaining; never recompute or contradict them.
For every resource in the data answer one object {"resource", "confidence", "recommendation"}:
- confidence: 0..1, how much the projection can be trusted, at most the resource's max_confidence;
- recommendation: one or two sentences of concrete advice for the operator.
Only for the resource "cache", when the limit will be reached, you may add "cache_max_bytes": a new resolver cache limit in bytes
above the current limit. For all other resources, give text advice only.
Answer with only JSON: {"forecasts":[...]}`
)

// SampleToday writes today's value of every resource with data into ai_capacity_samples, replacing an
// earlier sample of the same day. Engine resources are the maximum over engines of each engine's newest
// sample today, with that engine's limit.
func SampleToday(ctx context.Context, st *store.Store, now time.Time) error {
	now = now.UTC()
	dayStart := now.Truncate(24 * time.Hour)
	samples := sampleSet{}

	var cacheMax float64
	if err := st.Pool.QueryRow(ctx, `select cache_max_bytes from resolver_settings`).Scan(&cacheMax); err != nil {
		return store.MapError(err)
	}
	rows, err := st.Pool.Query(ctx, `select distinct on (s.engine_id) s.stats from engine_stats s
		join engines e on e.id = s.engine_id
		where e.deleted_at is null and s.at >= $1 and s.at <= $2 order by s.engine_id, s.at desc`, dayStart, now)
	if err != nil {
		return store.MapError(err)
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue // a corrupt sample is skipped, as on the dashboard
		}
		samples.addEngine(s, cacheMax)
	}
	if err := rows.Err(); err != nil {
		return store.MapError(err)
	}

	var entries float64
	if err := st.Pool.QueryRow(ctx, `select coalesce(sum(entry_count), 0) from filter_lists where enabled and kind = 'block'`).
		Scan(&entries); err != nil {
		return store.MapError(err)
	}
	samples["blocklist_entries"] = sampleRow{entries, nil}

	queries, ok, err := dailyQueries(ctx, st, now)
	if err != nil {
		return err
	}
	if ok {
		samples["query_volume"] = sampleRow{queries, nil}
	}

	day := dayStart.Format(time.DateOnly)
	if _, ok := samples["recursor_cache"]; !ok {
		// A later run may observe recursion disabled, an older binary, or a malformed
		// newest report. Do not leave today's earlier measurement looking current.
		if _, err := st.Pool.Exec(ctx, `delete from ai_capacity_samples where day = $1 and resource = 'recursor_cache'`, day); err != nil {
			return store.MapError(err)
		}
	}
	for resource, s := range samples {
		if _, err := st.Pool.Exec(ctx, `insert into ai_capacity_samples(day, resource, value, limit_value) values ($1, $2, $3, $4)
			on conflict (day, resource) do update set value = excluded.value, limit_value = excluded.limit_value`,
			day, resource, s.value, s.limit); err != nil {
			return store.MapError(err)
		}
	}
	return nil
}

// dailyQueries sums the query counter growth of every engine over the 24 h before now from the 5-minute
// rollup. A pair whose counter went down or whose start time changed (a restart) adds nothing. ok is
// false when no engine has two samples in the window.
func dailyQueries(ctx context.Context, st *store.Store, now time.Time) (float64, bool, error) {
	rows, err := st.Pool.Query(ctx, `select r.engine_id, r.stats from engine_stats_rollup r
		join engines e on e.id = r.engine_id
		where e.deleted_at is null and r.bucket > $1 and r.bucket <= $2 order by r.engine_id, r.bucket`, now.Add(-24*time.Hour), now)
	if err != nil {
		return 0, false, store.MapError(err)
	}
	defer rows.Close()
	var total uint64
	var ok bool
	var engine uuid.UUID
	var prev *controlv1.Stats
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, false, store.MapError(err)
		}
		s := &controlv1.Stats{}
		if proto.Unmarshal(raw, s) != nil {
			continue
		}
		if id != engine {
			engine, prev = id, nil
		}
		if prev != nil {
			ok = true
			if s.QueriesTotal >= prev.QueriesTotal && s.StartedUnixMs == prev.StartedUnixMs {
				total += s.QueriesTotal - prev.QueriesTotal
			}
		}
		prev = s
	}
	return float64(total), ok, store.MapError(rows.Err())
}

// Agent is the capacity_forecast background agent.
type Agent struct {
	Store     *store.Store
	Service   *ai.Service
	Validator *proposal.Validator
	Interval  time.Duration
	Now       func() time.Time
}

// Name is the agent name.
func (a *Agent) Name() string { return AgentName }

// Detail is the stored forecast (AiCapacityForecast in the API contract).
type Detail struct {
	Resource       string     `json:"resource"`
	CurrentValue   float64    `json:"current_value"`
	MaxValue       *float64   `json:"max_value"`
	GrowthPerDay   float64    `json:"growth_per_day"`
	GrowthPerWeek  float64    `json:"growth_per_week"`
	ExhaustionDate *time.Time `json:"projected_exhaustion_date"`
	DaysRemaining  *int       `json:"days_remaining"`
	Trend          string     `json:"trend"`
	Confidence     float64    `json:"confidence"`
	Recommendation string     `json:"recommendation"`
	PointsAnalyzed int        `json:"points_analyzed"`
}

type modelForecast struct {
	Resource       string  `json:"resource"`
	Confidence     float64 `json:"confidence"`
	Recommendation string  `json:"recommendation"`
	CacheMaxBytes  *int64  `json:"cache_max_bytes,omitempty"`
}

type modelAnswer struct {
	Forecasts []modelForecast `json:"forecasts"`
}

// Run samples today, projects every resource with samples and, when at least one has 3 points, asks the
// model for confidence and advice. A model failure still stores the projections, with confidence 0.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	if err := SampleToday(ctx, a.Store, now); err != nil {
		return err
	}
	projections, err := a.project(ctx, now)
	if err != nil {
		return err
	}
	run.Detail["resources"] = len(projections)
	byResource := map[string]Projection{}
	for _, p := range projections {
		if p.Trend != "insufficient_data" {
			byResource[p.Resource] = p
		}
	}
	if len(byResource) == 0 {
		return a.store(ctx, now, projections, nil, nil, 0)
	}

	var body map[string]json.RawMessage
	var raw []byte
	if err := a.Store.Pool.QueryRow(ctx, `select json_build_object('strategy', strategy, 'parallel_max', parallel_max,
			'cache_max_bytes', cache_max_bytes, 'cache_min_ttl', cache_min_ttl, 'cache_max_ttl', cache_max_ttl,
			'cache_negative_max_ttl', cache_negative_max_ttl, 'cache_stale_window', cache_stale_window, 'block_mode', block_mode,
			'block_ttl', block_ttl, 'otlp_endpoint', otlp_endpoint, 'trace_sample_one_in', trace_sample_one_in,
			'trace_slow_threshold_us', trace_slow_threshold_us, 'revision', revision) from resolver_settings`).Scan(&raw); err != nil {
		return store.MapError(err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	var currentCacheMax int64
	if err := json.Unmarshal(body["cache_max_bytes"], &currentCacheMax); err != nil {
		return err
	}

	input := make([]map[string]any, 0, len(byResource))
	for _, p := range projections {
		if _, ok := byResource[p.Resource]; ok {
			d := detail(p, 0, "")
			input = append(input, map[string]any{"projection": d, "max_confidence": p.MaxConfidence})
		}
	}
	var cacheAction *proposal.Action
	var newCacheMax int64
	res, genErr := ai.Generate(ctx, a.Service, ai.Request[modelAnswer]{
		Feature: ai.Feature(AgentName), Priority: ai.Background, System: system,
		Prompt: "Current resolver cache_max_bytes: " + fmt.Sprint(currentCacheMax) + "\n" + ai.DataBlock(input),
		Validate: func(ans *modelAnswer) error {
			cacheAction = nil
			seen := map[string]bool{}
			for i, f := range ans.Forecasts {
				p, ok := byResource[f.Resource]
				switch {
				case !ok:
					return fmt.Errorf("forecasts[%d]: resource %q is not one of the projected resources", i, f.Resource)
				case seen[f.Resource]:
					return fmt.Errorf("forecasts[%d]: resource %s appears twice", i, f.Resource)
				case f.Confidence < 0 || f.Confidence > p.MaxConfidence:
					return fmt.Errorf("forecasts[%d]: confidence for %s must be between 0 and %g (%d points)", i, f.Resource,
						p.MaxConfidence, p.Points)
				case strings.TrimSpace(f.Recommendation) == "":
					return fmt.Errorf("forecasts[%d]: recommendation is required", i)
				case f.CacheMaxBytes != nil && f.Resource != "cache":
					return fmt.Errorf("forecasts[%d]: cache_max_bytes is only allowed for the cache resource", i)
				case f.CacheMaxBytes != nil && *f.CacheMaxBytes <= currentCacheMax:
					return fmt.Errorf("forecasts[%d]: cache_max_bytes must be above the current %d", i, currentCacheMax)
				}
				seen[f.Resource] = true
				if f.CacheMaxBytes != nil {
					b := make(map[string]json.RawMessage, len(body))
					for k, v := range body {
						b[k] = v
					}
					b["cache_max_bytes"] = json.RawMessage(fmt.Sprint(*f.CacheMaxBytes))
					encoded, err := json.Marshal(b)
					if err != nil {
						return err
					}
					action := proposal.Action{OperationID: "updateResolverSettings", PathParams: map[string]string{}, Body: encoded,
						Explanation: f.Recommendation}
					if err := a.Validator.Validate(ctx, []proposal.Action{action}); err != nil {
						return fmt.Errorf("forecasts[%d]: %w", i, err)
					}
					cacheAction, newCacheMax = &action, *f.CacheMaxBytes
				}
			}
			for r := range byResource {
				if !seen[r] {
					return fmt.Errorf("resource %s has no forecast", r)
				}
			}
			return nil
		},
	})
	run.Usage.InputTokens += res.Usage.InputTokens
	run.Usage.OutputTokens += res.Usage.OutputTokens
	run.Usage.TotalTokens += res.Usage.TotalTokens
	run.Usage.ReasoningTokens += res.Usage.ReasoningTokens
	if genErr != nil {
		if ctx.Err() != nil {
			return genErr
		}
		if err := a.store(ctx, now, projections, nil, nil, 0); err != nil {
			return err
		}
		if errors.Is(genErr, ai.ErrBudgetExhausted) {
			run.Outcome = "skipped_budget"
			return nil
		}
		return genErr
	}
	answers := map[string]modelForecast{}
	for _, f := range res.Value.Forecasts {
		answers[f.Resource] = f
	}
	return a.store(ctx, now, projections, answers, cacheAction, newCacheMax)
}

// project reads the samples of the last 400 days and projects every resource that has one; the limit
// is the newest sample's.
func (a *Agent) project(ctx context.Context, now time.Time) ([]Projection, error) {
	rows, err := a.Store.Pool.Query(ctx, `select resource, day, value, limit_value from ai_capacity_samples
		where day > $1::date order by resource, day`, now.UTC().AddDate(0, 0, -historyDays).Format(time.DateOnly))
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	points := map[string][]Point{}
	limits := map[string]*float64{}
	for rows.Next() {
		var resource string
		var p Point
		var limit *float64
		if err := rows.Scan(&resource, &p.Day, &p.Value, &limit); err != nil {
			return nil, store.MapError(err)
		}
		points[resource] = append(points[resource], p)
		limits[resource] = limit
	}
	if err := rows.Err(); err != nil {
		return nil, store.MapError(err)
	}
	var out []Projection
	for _, r := range Resources {
		if len(points[r]) > 0 {
			out = append(out, Project(r, points[r], limits[r], now))
		}
	}
	return out, nil
}

func detail(p Projection, confidence float64, recommendation string) Detail {
	return Detail{Resource: p.Resource, CurrentValue: p.Current, MaxValue: p.Max, GrowthPerDay: p.GrowthPerDay,
		GrowthPerWeek: p.GrowthPerWeek, ExhaustionDate: p.ExhaustionDate, DaysRemaining: p.DaysRemaining, Trend: p.Trend,
		Confidence: confidence, Recommendation: recommendation, PointsAnalyzed: p.Points}
}

// store writes one forecast per projection, valid for one interval, and turns the cache action into a
// proposal raising cache_max_bytes to newCacheMax. answers is nil when the model was not asked or failed.
func (a *Agent) store(ctx context.Context, now time.Time, projections []Projection, answers map[string]modelForecast,
	cacheAction *proposal.Action, newCacheMax int64) error {
	for _, p := range projections {
		f := answers[p.Resource]
		if p.Trend == "insufficient_data" {
			f = modelForecast{Recommendation: noDataAdvice}
		}
		d := detail(p, f.Confidence, f.Recommendation)
		var proposalID *uuid.UUID
		if p.Resource == "cache" && cacheAction != nil {
			priority := "medium"
			if p.DaysRemaining != nil && *p.DaysRemaining <= urgentDays {
				priority = "high"
			}
			id, _, err := proposal.Upsert(ctx, a.Store, proposal.Draft{
				Source: AgentName, Title: fmt.Sprintf("Raise the resolver cache limit to %d MiB", newCacheMax>>20),
				Description: f.Recommendation, Priority: priority,
				Impact:   map[string]any{"resource": "cache", "days_remaining": p.DaysRemaining},
				Evidence: d, Actions: []proposal.Action{*cacheAction},
			})
			if err != nil {
				return err
			}
			if id != uuid.Nil {
				proposalID = &id
			}
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if err := forecast.Put(ctx, a.Store, forecast.Forecast{Kind: "capacity", Subject: p.Resource, Detail: raw,
			ProposalID: proposalID, GeneratedAt: now, ValidUntil: now.Add(a.Interval)}); err != nil {
			return err
		}
	}
	return nil
}
