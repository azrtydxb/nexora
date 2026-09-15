package rolloutrisk

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
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// maxAnalysis bounds the model's analysis text.
const maxAnalysis = 1200

// dueBatch is how many unassessed rollouts one run takes, and dueWindow how old they may be.
const (
	dueBatch  = 5
	dueWindow = 24 * time.Hour
)

const system = `You assess how risky one configuration rollout to a group of DNS engines is, from features that code computed
from the audit log, the fleet and the group's past rollouts. Do not recompute the features; interpret them.

Answer with only this JSON object:
{"risk_score": 1 to 10, "risk_level": "low|medium|high", "analysis": "why this rollout carries that risk, citing the features",
 "historical_patterns": [{"config_version": <a config_version listed in similar_past_rollouts>}],
 "recommendation": {"strategy": "all_at_once|canary", "canary_count": <1 to engines-1>, "min_health_queries": <at least 0>,
   "max_servfail_ratio": <0 to 1>, "reasoning": "why these rollout settings suit this change"}}

The score bands are fixed: 1 to 3 is low, 4 to 7 is medium, 8 to 10 is high. Cite only config_versions that appear in
similar_past_rollouts. Give a recommendation only when the group's rollout_params are not already right for this change;
otherwise answer "recommendation": null.`

// Recommendation is the rollout configuration the model suggests for this change.
type Recommendation struct {
	Strategy         string  `json:"strategy"`
	CanaryCount      int     `json:"canary_count"`
	MinHealthQueries int     `json:"min_health_queries"`
	MaxServfailRatio float64 `json:"max_servfail_ratio"`
	Reasoning        string  `json:"reasoning"`
}

// Assessment is the model's validated answer.
type Assessment struct {
	RiskScore          int             `json:"risk_score"`
	RiskLevel          string          `json:"risk_level"`
	Analysis           string          `json:"analysis"`
	HistoricalPatterns []History       `json:"historical_patterns"`
	Recommendation     *Recommendation `json:"recommendation"`
}

// band is the level a score must carry.
func band(score int) string {
	switch {
	case score <= 3:
		return "low"
	case score <= 7:
		return "medium"
	default:
		return "high"
	}
}

// ValidateAssessment rejects an answer the features do not support. A cited historical pattern is
// replaced by the computed History of that version, so the stored patterns are never the model's
// numbers.
func ValidateAssessment(a *Assessment, f Features) error {
	a.Analysis = strings.TrimSpace(a.Analysis)
	if a.RiskScore < 1 || a.RiskScore > 10 {
		return fmt.Errorf("risk_score must be between 1 and 10, got %d", a.RiskScore)
	}
	if want := band(a.RiskScore); a.RiskLevel != want {
		return fmt.Errorf("risk_level of score %d must be %s, got %q", a.RiskScore, want, a.RiskLevel)
	}
	if a.Analysis == "" || len(a.Analysis) > maxAnalysis {
		return fmt.Errorf("analysis must be 1 to %d characters", maxAnalysis)
	}
	known := map[int64]History{}
	for _, h := range f.Similar {
		known[h.Version] = h
	}
	for i, h := range a.HistoricalPatterns {
		computed, ok := known[h.Version]
		if !ok {
			return fmt.Errorf("historical_patterns[%d]: config_version %d is not one of the similar past rollouts", i, h.Version)
		}
		a.HistoricalPatterns[i] = computed
	}
	if a.HistoricalPatterns == nil {
		a.HistoricalPatterns = []History{}
	}
	if r := a.Recommendation; r != nil {
		r.Reasoning = strings.TrimSpace(r.Reasoning)
		maxCanary := max(1, f.Engines-1)
		switch {
		case r.Strategy != "all_at_once" && r.Strategy != "canary":
			return fmt.Errorf("recommendation.strategy must be all_at_once or canary, got %q", r.Strategy)
		case r.CanaryCount < 1 || r.CanaryCount > maxCanary:
			return fmt.Errorf("recommendation.canary_count must be between 1 and %d, got %d", maxCanary, r.CanaryCount)
		case r.MinHealthQueries < 0:
			return fmt.Errorf("recommendation.min_health_queries must be at least 0, got %d", r.MinHealthQueries)
		case r.MaxServfailRatio < 0 || r.MaxServfailRatio > 1:
			return fmt.Errorf("recommendation.max_servfail_ratio must be between 0 and 1, got %v", r.MaxServfailRatio)
		case r.Reasoning == "":
			return errors.New("recommendation.reasoning is required")
		}
	}
	return nil
}

// Agent is the rollout risk assessment agent (M11 S-6). It never delays a rollout: the controller
// rolls out while the agent scores, and the assessment is suggest-only.
type Agent struct {
	Store     *store.Store
	Service   *ai.Service
	Validator *proposal.Validator
	Now       func() time.Time
}

// Name returns "rollout_risk".
func (a *Agent) Name() string { return Name }

// due is one rollout waiting for its risk row.
type due struct {
	id               uuid.UUID
	kind, strategy   string
	contentUnchanged bool
}

// Run assesses up to dueBatch rollouts of the last 24 hours that have no risk row yet, oldest first.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	rollouts, err := a.due(ctx, now)
	if err != nil {
		return err
	}
	var failed []error
	assessed, skipped, proposals := 0, 0, 0
	for _, r := range rollouts {
		// A rollback, a republish or a rollout that serves the content the engines already have is
		// not a configuration change an operator can weigh: there is nothing to assess.
		if r.kind != "change" || (r.strategy == "all_at_once" && r.contentUnchanged) {
			if err := a.put(ctx, risk{RolloutID: r.id, Status: "skipped"}); err != nil {
				return err
			}
			skipped++
			continue
		}
		f, err := Collect(ctx, a.Store.Pool, r.id, now)
		if err != nil {
			return err
		}
		ass, err := a.assess(ctx, run, f)
		if errors.Is(err, ai.ErrBudgetExhausted) {
			run.Outcome = "skipped_budget"
			break
		}
		if err != nil {
			code := ai.Code(err)
			if code == "" {
				code = "error"
			}
			if putErr := a.put(ctx, risk{RolloutID: r.id, Status: "failed", Error: code}); putErr != nil {
				return putErr
			}
			failed = append(failed, fmt.Errorf("rollout %s: %w", r.id, err))
			continue
		}
		proposalID, err := a.propose(ctx, f, ass)
		if err != nil {
			return err
		}
		if proposalID != nil {
			proposals++
		}
		detail, err := json.Marshal(map[string]any{"historical_patterns": ass.HistoricalPatterns,
			"recommendation": ass.Recommendation})
		if err != nil {
			return err
		}
		at := now
		if err := a.put(ctx, risk{RolloutID: r.id, Status: "assessed", RiskScore: &ass.RiskScore, RiskLevel: &ass.RiskLevel,
			Analysis: ass.Analysis, Detail: detail, ProposalID: proposalID, AssessedAt: &at}); err != nil {
			return err
		}
		assessed++
	}
	if assessed == 0 && skipped == 0 && len(failed) == 0 {
		run.Outcome = "no_change"
	}
	run.Detail["assessed"], run.Detail["skipped"], run.Detail["proposals"] = assessed, skipped, proposals
	return errors.Join(failed...)
}

// due lists the rollouts to assess. contentUnchanged says the group's engines already serve this
// content: the snapshot's digest equals that of the group's previous snapshot.
func (a *Agent) due(ctx context.Context, now time.Time) ([]due, error) {
	rows, err := a.Store.Pool.Query(ctx, `select r.id, r.kind, r.strategy,
		coalesce((select gs.content_sha256 from group_snapshots gs
		          where gs.engine_group_id = r.engine_group_id and gs.version = r.version)
		       = (select gs.content_sha256 from group_snapshots gs
		          where gs.engine_group_id = r.engine_group_id
		            and gs.version = (select max(version) from group_snapshots
		                              where engine_group_id = r.engine_group_id and version < r.version)), false)
		from rollouts r left join ai_rollout_risks x on x.rollout_id = r.id
		where x.rollout_id is null and r.created_at > $1::timestamptz - $2::interval
		order by r.created_at, r.version limit $3`, now, dueWindow.String(), dueBatch)
	if err != nil {
		return nil, store.MapError(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		err := r.Scan(&d.id, &d.kind, &d.strategy, &d.contentUnchanged)
		return d, err
	})
	return out, store.MapError(err)
}

// assess asks the model for one rollout; the answer is validated against the computed features, so a
// fabricated history or an off-band score is re-asked and never stored.
func (a *Agent) assess(ctx context.Context, run *ai.Run, f Features) (Assessment, error) {
	res, err := ai.Generate(ctx, a.Service, ai.Request[Assessment]{
		Feature: Name, Priority: ai.Background, System: system,
		Prompt:   "Assess this rollout.\n" + ai.DataBlock(f),
		Validate: func(x *Assessment) error { return ValidateAssessment(x, f) },
	})
	run.Usage.InputTokens += res.Usage.InputTokens
	run.Usage.OutputTokens += res.Usage.OutputTokens
	run.Usage.TotalTokens += res.Usage.TotalTokens
	run.Usage.ReasoningTokens += res.Usage.ReasoningTokens
	if err != nil {
		return Assessment{}, err
	}
	return res.Value, nil
}

// engineGroup is the updateEngineGroup body of one group, read from the group itself.
type engineGroup struct {
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	UpstreamMode        string   `json:"upstream_mode"`
	ExtraACLCIDRs       []string `json:"extra_acl_cidrs"`
	OTLPEndpoint        string   `json:"otlp_endpoint"`
	RolloutStrategy     string   `json:"rollout_strategy"`
	CanaryCount         int      `json:"canary_count"`
	CanaryPercent       int      `json:"canary_percent"`
	AckTimeoutSeconds   int      `json:"ack_timeout_seconds"`
	HealthWindowSeconds int      `json:"health_window_seconds"`
	MaxServfailRatio    float64  `json:"max_servfail_ratio"`
	MinHealthQueries    int      `json:"min_health_queries"`
	FilterIndexMaxBytes int64    `json:"filter_index_max_bytes"`
	Revision            int64    `json:"revision"`
}

// propose suggests the recommended rollout settings for the engine group when the assessment is not
// low and the settings really differ. It returns nil when there is nothing to propose.
func (a *Agent) propose(ctx context.Context, f Features, ass Assessment) (*uuid.UUID, error) {
	r := ass.Recommendation
	if r == nil || ass.RiskLevel == "low" {
		return nil, nil
	}
	var g engineGroup
	err := a.Store.Pool.QueryRow(ctx, `select name, description, upstream_mode, extra_acl_cidrs::text[], otlp_endpoint,
		rollout_strategy, canary_count, canary_percent, ack_timeout_seconds, health_window_seconds, max_servfail_ratio,
		min_health_queries, filter_index_max_bytes, revision from engine_groups where id = $1`, f.EngineGroupID).
		Scan(&g.Name, &g.Description, &g.UpstreamMode, &g.ExtraACLCIDRs, &g.OTLPEndpoint, &g.RolloutStrategy, &g.CanaryCount,
			&g.CanaryPercent, &g.AckTimeoutSeconds, &g.HealthWindowSeconds, &g.MaxServfailRatio, &g.MinHealthQueries,
			&g.FilterIndexMaxBytes, &g.Revision)
	if err != nil {
		return nil, store.MapError(err)
	}
	if g.RolloutStrategy == r.Strategy && g.CanaryCount == r.CanaryCount && g.MinHealthQueries == r.MinHealthQueries &&
		g.MaxServfailRatio == r.MaxServfailRatio {
		return nil, nil
	}
	if g.ExtraACLCIDRs == nil {
		g.ExtraACLCIDRs = []string{}
	}
	g.RolloutStrategy, g.CanaryCount = r.Strategy, r.CanaryCount
	g.MinHealthQueries, g.MaxServfailRatio = r.MinHealthQueries, r.MaxServfailRatio
	body, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	actions := []proposal.Action{{OperationID: "updateEngineGroup", PathParams: map[string]string{"id": f.EngineGroupID.String()},
		Body: body, Explanation: r.Reasoning}}
	if err := a.Validator.Validate(ctx, actions); err != nil {
		return nil, err
	}
	priority := "medium"
	if ass.RiskLevel == "high" {
		priority = "high"
	}
	id, _, err := proposal.Upsert(ctx, a.Store, proposal.Draft{Source: Name,
		Title:       fmt.Sprintf("Roll out engine group %s with the %s strategy", g.Name, r.Strategy),
		Description: r.Reasoning, Priority: priority,
		Impact:   map[string]any{"engine_group": g.Name, "risk_level": ass.RiskLevel, "risk_score": ass.RiskScore},
		Evidence: map[string]any{"config_version": f.Version, "changes": f.Changes, "analysis": ass.Analysis},
		Actions:  actions})
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, nil
	}
	return &id, nil
}

// risk is one ai_rollout_risks row.
type risk struct {
	RolloutID  uuid.UUID
	Status     string
	RiskScore  *int
	RiskLevel  *string
	Analysis   string
	Detail     json.RawMessage
	ProposalID *uuid.UUID
	AssessedAt *time.Time
	Error      string
}

// Risk is a stored rollout risk assessment; a rollout without a row is "pending".
type Risk = risk

// emptyDetail is the detail of a row with no assessment; historical_patterns is never null, so the
// API's required array is always present.
var emptyDetail = json.RawMessage(`{"historical_patterns":[],"recommendation":null}`)

func (a *Agent) put(ctx context.Context, r risk) error {
	if len(r.Detail) == 0 {
		r.Detail = emptyDetail
	}
	_, err := a.Store.Pool.Exec(ctx, `insert into ai_rollout_risks
		(rollout_id, status, risk_score, risk_level, analysis, detail, proposal_id, assessed_at, error)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		on conflict (rollout_id) do update set status = excluded.status, risk_score = excluded.risk_score,
			risk_level = excluded.risk_level, analysis = excluded.analysis, detail = excluded.detail,
			proposal_id = excluded.proposal_id, assessed_at = excluded.assessed_at, error = excluded.error`,
		r.RolloutID, r.Status, r.RiskScore, r.RiskLevel, r.Analysis, r.Detail, r.ProposalID, r.AssessedAt, r.Error)
	return store.MapError(err)
}

// Get returns rolloutID's assessment. A rollout that has no row yet is "pending"; store.ErrNotFound
// means the rollout itself does not exist.
func Get(ctx context.Context, q store.PolicyQuerier, rolloutID uuid.UUID) (Risk, error) {
	var r risk
	err := q.QueryRow(ctx, `select rollout_id, status, risk_score, risk_level, analysis, detail, proposal_id, assessed_at, error
		from ai_rollout_risks where rollout_id = $1`, rolloutID).
		Scan(&r.RolloutID, &r.Status, &r.RiskScore, &r.RiskLevel, &r.Analysis, &r.Detail, &r.ProposalID, &r.AssessedAt, &r.Error)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Risk{}, store.MapError(err)
	}
	var exists bool
	if err := q.QueryRow(ctx, `select true from rollouts where id = $1`, rolloutID).Scan(&exists); err != nil {
		return Risk{}, store.MapError(err)
	}
	return Risk{RolloutID: rolloutID, Status: "pending", Detail: emptyDetail}, nil
}
