package upstreampred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/forecast"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Name is the agent name, feature label and proposal source.
const Name = "upstream_prediction"

// maxReasoning bounds the model's reasoning text.
const maxReasoning = 2000

const system = `You assess the health trend of one DNS upstream from features that code computed from hourly RTT and failure
samples. Do not recompute the features; interpret them.

Answer with only this JSON object:
{"trend": "stable|degrading|improving|periodic|failing", "confidence": 0..1, "reasoning": "short explanation citing the features",
 "recommendation": {"type": "switch_strategy|reorder|disable|none", "description": "what to change and why",
  "strategy": "fastest|parallel (switch_strategy only)", "position": 0 (reorder only: the new position of this upstream)}}

Recommend a change only when the features clearly justify it:
- switch_strategy: change the resolver upstream strategy to fastest or parallel;
- reorder: move this upstream to another position (lower is tried first);
- disable: disable this upstream, only when another enabled upstream stays in its scope;
- none: no change.`

// Agent is the upstream health prediction agent (M11 S-8).
type Agent struct {
	Store     *store.Store
	Service   *ai.Service
	Validator *proposal.Validator
	Interval  time.Duration
	Now       func() time.Time
}

// Name returns "upstream_prediction".
func (a *Agent) Name() string { return Name }

type recommendation struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Strategy    string `json:"strategy,omitempty"`
	Position    *int   `json:"position,omitempty"`
}

type answer struct {
	Trend          string         `json:"trend"`
	Confidence     float64        `json:"confidence"`
	Reasoning      string         `json:"reasoning"`
	Recommendation recommendation `json:"recommendation"`
}

// detailRecommendation is the stored recommendation of the AiUpstreamPrediction shape.
type detailRecommendation struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// detail is the AiUpstreamPrediction forecast detail.
type detail struct {
	UpstreamID               string               `json:"upstream_id"`
	UpstreamName             string               `json:"upstream_name"`
	Trend                    string               `json:"trend"`
	Confidence               float64              `json:"confidence"`
	CurrentRttP50Ms          float64              `json:"current_rtt_p50_ms"`
	CurrentRttP99Ms          float64              `json:"current_rtt_p99_ms"`
	SlopeMsPerHour           float64              `json:"slope_ms_per_hour"`
	StepChange               bool                 `json:"step_change"`
	PeriodicHours            []int                `json:"periodic_hours"`
	ProjectedTimeToThreshold *time.Time           `json:"projected_time_to_threshold"`
	DataPointsAnalyzed       int                  `json:"data_points_analyzed"`
	Reasoning                string               `json:"reasoning"`
	Recommendation           detailRecommendation `json:"recommendation"`
}

// promptFeatures are the features the model sees.
type promptFeatures struct {
	UpstreamName             string     `json:"upstream_name"`
	Position                 int        `json:"position"`
	TimeoutMs                int        `json:"timeout_ms"`
	DataPoints               int        `json:"data_points_analyzed"`
	CurrentRttP50Ms          float64    `json:"current_rtt_p50_ms"`
	CurrentRttP99Ms          float64    `json:"current_rtt_p99_ms"`
	SlopeMsPerHour           float64    `json:"slope_ms_per_hour"`
	StepChange               bool       `json:"step_change"`
	PeriodicHours            []int      `json:"periodic_hours"`
	FailureSlopePerHour      float64    `json:"failure_ratio_slope_per_hour"`
	CurrentFailureRatio      float64    `json:"current_failure_ratio"`
	ProjectedTimeToThreshold *time.Time `json:"projected_time_to_threshold"`
	ResolverStrategy         string     `json:"resolver_strategy"`
	OtherEnabledInScope      int        `json:"other_enabled_upstreams_in_scope"`
}

type upstream struct {
	ID                                             uuid.UUID
	Name, Protocol, Address, TLSServerName, DoHURL string
	CAPEM                                          string
	TimeoutMs, Position                            int
	Enabled                                        bool
	Revision                                       int64
	EngineGroupID                                  *uuid.UUID
}

// Run predicts every enabled upstream: a forecast each, and a proposal for a recommended change. An
// exhausted budget ends the run as skipped_budget; another failed prediction does not stop the others.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	ups, err := a.upstreams(ctx)
	if err != nil {
		return err
	}
	var failed []error
	forecasts, proposals := 0, 0
	for _, u := range ups {
		points, err := HourlyPoints(ctx, a.Store.Pool, u.Name, now)
		if err != nil {
			return err
		}
		f := Compute(u.ID.String(), u.Name, u.TimeoutMs, points, now)
		d := detail{UpstreamID: f.UpstreamID, UpstreamName: f.Name, CurrentRttP50Ms: f.CurrentP50Ms, CurrentRttP99Ms: f.CurrentP99Ms,
			SlopeMsPerHour: f.SlopeMsPerHour, StepChange: f.StepChange, PeriodicHours: f.PeriodicHours,
			ProjectedTimeToThreshold: f.ProjectedTimeToThreshold, DataPointsAnalyzed: f.Points}
		var proposalID *uuid.UUID
		if f.Points < MinPoints {
			d.Trend, d.Reasoning = "insufficient_data", fmt.Sprintf("Only %d hourly points; at least %d are needed.", f.Points, MinPoints)
			d.Recommendation = detailRecommendation{Type: "none", Description: "Wait for more data."}
		} else {
			var lastFailure float64
			if len(points) > 0 {
				lastFailure = points[len(points)-1].FailureRatio
			}
			ans, actions, err := a.predict(ctx, run, u, f, lastFailure)
			if errors.Is(err, ai.ErrBudgetExhausted) {
				run.Outcome = "skipped_budget"
				break
			}
			if err != nil {
				failed = append(failed, fmt.Errorf("upstream %s: %w", u.Name, err))
				continue
			}
			d.Trend, d.Confidence, d.Reasoning = ans.Trend, ans.Confidence, ans.Reasoning
			d.Recommendation = detailRecommendation{Type: ans.Recommendation.Type, Description: ans.Recommendation.Description}
			if len(actions) > 0 {
				id, _, err := proposal.Upsert(ctx, a.Store, proposal.Draft{Source: Name, Title: title(u, ans.Recommendation),
					Description: ans.Recommendation.Description, Priority: priority(ans.Trend),
					Impact: map[string]any{"upstream": u.Name, "trend": ans.Trend}, Evidence: d, Actions: actions})
				if err != nil {
					return err
				}
				if id != uuid.Nil {
					proposalID = &id
					proposals++
				}
			}
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if err := forecast.Put(ctx, a.Store, forecast.Forecast{Kind: "upstream", Subject: f.UpstreamID, Detail: raw,
			ProposalID: proposalID, GeneratedAt: now, ValidUntil: now.Add(a.Interval)}); err != nil {
			return err
		}
		forecasts++
	}
	run.Detail["upstreams"], run.Detail["forecasts"], run.Detail["proposals"] = len(ups), forecasts, proposals
	return errors.Join(failed...)
}

// predict asks the model for one upstream. The validator builds and validates the recommended actions,
// so a rejected recommendation re-asks the model and is never stored.
func (a *Agent) predict(ctx context.Context, run *ai.Run, u upstream, f Features, failureRatio float64) (answer, []proposal.Action, error) {
	var strategy string
	if err := a.Store.Pool.QueryRow(ctx, `select strategy from resolver_settings`).Scan(&strategy); err != nil {
		return answer{}, nil, store.MapError(err)
	}
	others, err := a.otherEnabled(ctx, u)
	if err != nil {
		return answer{}, nil, err
	}
	pf := promptFeatures{UpstreamName: u.Name, Position: u.Position, TimeoutMs: u.TimeoutMs, DataPoints: f.Points,
		CurrentRttP50Ms: f.CurrentP50Ms, CurrentRttP99Ms: f.CurrentP99Ms, SlopeMsPerHour: f.SlopeMsPerHour, StepChange: f.StepChange,
		PeriodicHours: f.PeriodicHours, FailureSlopePerHour: f.FailureSlopePerHour, CurrentFailureRatio: failureRatio,
		ProjectedTimeToThreshold: f.ProjectedTimeToThreshold, ResolverStrategy: strategy, OtherEnabledInScope: others}
	var actions []proposal.Action
	res, err := ai.Generate(ctx, a.Service, ai.Request[answer]{
		Feature: Name, Priority: ai.Background, System: system,
		Prompt: "Assess this upstream.\n" + ai.DataBlock(pf),
		Validate: func(ans *answer) error {
			if err := validateAnswer(ans); err != nil {
				return err
			}
			acts, err := a.actions(ctx, u, ans.Recommendation)
			if err != nil {
				return err
			}
			if len(acts) > 0 {
				if err := a.Validator.Validate(ctx, acts); err != nil {
					return err
				}
			}
			actions = acts
			return nil
		},
	})
	run.Usage.InputTokens += res.Usage.InputTokens
	run.Usage.OutputTokens += res.Usage.OutputTokens
	run.Usage.TotalTokens += res.Usage.TotalTokens
	run.Usage.ReasoningTokens += res.Usage.ReasoningTokens
	if err != nil {
		return answer{}, nil, err
	}
	return res.Value, actions, nil
}

func validateAnswer(ans *answer) error {
	ans.Reasoning = strings.TrimSpace(ans.Reasoning)
	ans.Recommendation.Description = strings.TrimSpace(ans.Recommendation.Description)
	switch {
	case !map[string]bool{"stable": true, "degrading": true, "improving": true, "periodic": true, "failing": true}[ans.Trend]:
		return fmt.Errorf("trend must be stable, degrading, improving, periodic or failing, got %q", ans.Trend)
	case ans.Confidence < 0 || ans.Confidence > 1:
		return fmt.Errorf("confidence must be between 0 and 1, got %v", ans.Confidence)
	case ans.Reasoning == "" || len(ans.Reasoning) > maxReasoning:
		return fmt.Errorf("reasoning must be 1 to %d characters", maxReasoning)
	case ans.Recommendation.Description == "":
		return errors.New("recommendation.description is required")
	}
	return nil
}

// actions turns a recommendation into proposal actions built from the current configuration; none gives
// no action.
func (a *Agent) actions(ctx context.Context, u upstream, rec recommendation) ([]proposal.Action, error) {
	switch rec.Type {
	case "none":
		return nil, nil
	case "switch_strategy":
		return a.switchStrategy(ctx, rec)
	case "reorder":
		if rec.Position == nil || *rec.Position < 0 {
			return nil, errors.New("reorder needs a position of at least 0")
		}
		cur, err := a.upstream(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		if cur.Position == *rec.Position {
			return nil, fmt.Errorf("upstream %s is already at position %d", u.Name, cur.Position)
		}
		cur.Position = *rec.Position
		return updateUpstream(cur, rec.Description)
	case "disable":
		others, err := a.otherEnabled(ctx, u)
		if err != nil {
			return nil, err
		}
		if others < 1 {
			return nil, fmt.Errorf("disable rejected: upstream %s is the only enabled upstream in its scope; at least 2 enabled upstreams are needed", u.Name)
		}
		cur, err := a.upstream(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		cur.Enabled = false
		return updateUpstream(cur, rec.Description)
	default:
		return nil, fmt.Errorf("recommendation.type must be switch_strategy, reorder, disable or none, got %q", rec.Type)
	}
}

func (a *Agent) switchStrategy(ctx context.Context, rec recommendation) ([]proposal.Action, error) {
	if rec.Strategy != "fastest" && rec.Strategy != "parallel" {
		return nil, fmt.Errorf("switch_strategy needs strategy fastest or parallel, got %q", rec.Strategy)
	}
	var s struct {
		Strategy             string `json:"strategy"`
		ParallelMax          int    `json:"parallel_max"`
		CacheMaxBytes        int64  `json:"cache_max_bytes"`
		CacheMinTTL          int    `json:"cache_min_ttl"`
		CacheMaxTTL          int    `json:"cache_max_ttl"`
		CacheNegativeMaxTTL  int    `json:"cache_negative_max_ttl"`
		CacheStaleWindow     int    `json:"cache_stale_window"`
		BlockMode            string `json:"block_mode"`
		BlockTTL             int    `json:"block_ttl"`
		OTLPEndpoint         string `json:"otlp_endpoint"`
		TraceSampleOneIn     int    `json:"trace_sample_one_in"`
		TraceSlowThresholdUs int    `json:"trace_slow_threshold_us"`
		Revision             int64  `json:"revision"`
	}
	err := a.Store.Pool.QueryRow(ctx, `select strategy, parallel_max, cache_max_bytes, cache_min_ttl, cache_max_ttl,
		cache_negative_max_ttl, cache_stale_window, block_mode, block_ttl, otlp_endpoint, trace_sample_one_in,
		trace_slow_threshold_us, revision from resolver_settings`).Scan(&s.Strategy, &s.ParallelMax, &s.CacheMaxBytes, &s.CacheMinTTL,
		&s.CacheMaxTTL, &s.CacheNegativeMaxTTL, &s.CacheStaleWindow, &s.BlockMode, &s.BlockTTL, &s.OTLPEndpoint, &s.TraceSampleOneIn,
		&s.TraceSlowThresholdUs, &s.Revision)
	if err != nil {
		return nil, store.MapError(err)
	}
	if s.Strategy == rec.Strategy {
		return nil, fmt.Errorf("the resolver strategy is already %s", rec.Strategy)
	}
	s.Strategy = rec.Strategy
	body, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return []proposal.Action{{OperationID: "updateResolverSettings", PathParams: map[string]string{}, Body: body, Explanation: rec.Description}}, nil
}

func updateUpstream(u upstream, explanation string) ([]proposal.Action, error) {
	var group *string
	if u.EngineGroupID != nil {
		g := u.EngineGroupID.String()
		group = &g
	}
	body, err := json.Marshal(map[string]any{"name": u.Name, "protocol": u.Protocol, "address": u.Address,
		"tls_server_name": u.TLSServerName, "doh_url": u.DoHURL, "timeout_ms": u.TimeoutMs, "ca_certificate_pem": u.CAPEM,
		"position": u.Position, "enabled": u.Enabled, "revision": u.Revision, "engine_group_id": group})
	if err != nil {
		return nil, err
	}
	return []proposal.Action{{OperationID: "updateUpstream", PathParams: map[string]string{"id": u.ID.String()}, Body: body,
		Explanation: explanation}}, nil
}

// otherEnabled counts the other enabled upstreams of u's scope: the global upstreams, or those of its
// engine group.
func (a *Agent) otherEnabled(ctx context.Context, u upstream) (int, error) {
	var n int
	err := a.Store.Pool.QueryRow(ctx, `select count(*) from upstreams where enabled and id <> $1
		and engine_group_id is not distinct from $2`, u.ID, u.EngineGroupID).Scan(&n)
	return n, store.MapError(err)
}

const upstreamColumns = `id, name, protocol, address, tls_server_name, doh_url, ca_certificate_pem, timeout_ms, position, enabled,
	revision, engine_group_id`

func scanUpstream(row pgx.Row) (upstream, error) {
	var u upstream
	err := row.Scan(&u.ID, &u.Name, &u.Protocol, &u.Address, &u.TLSServerName, &u.DoHURL, &u.CAPEM, &u.TimeoutMs, &u.Position,
		&u.Enabled, &u.Revision, &u.EngineGroupID)
	return u, store.MapError(err)
}

func (a *Agent) upstream(ctx context.Context, id uuid.UUID) (upstream, error) {
	return scanUpstream(a.Store.Pool.QueryRow(ctx, `select `+upstreamColumns+` from upstreams where id = $1`, id))
}

func (a *Agent) upstreams(ctx context.Context) ([]upstream, error) {
	rows, err := a.Store.Pool.Query(ctx, `select `+upstreamColumns+` from upstreams where enabled order by position, name`)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (upstream, error) { return scanUpstream(r) })
	return out, store.MapError(err)
}

func title(u upstream, rec recommendation) string {
	switch rec.Type {
	case "switch_strategy":
		return fmt.Sprintf("Switch the upstream strategy to %s (upstream %s)", rec.Strategy, u.Name)
	case "reorder":
		return fmt.Sprintf("Move upstream %s to position %d", u.Name, *rec.Position)
	default:
		return fmt.Sprintf("Disable upstream %s", u.Name)
	}
}

func priority(trend string) string {
	switch trend {
	case "failing":
		return "high"
	case "degrading":
		return "medium"
	default:
		return "low"
	}
}
