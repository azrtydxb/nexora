package insight

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Name is the agent name.
const Name = "dashboard_insights"

const (
	maxTitle, maxDescription = 120, 1000
	maxInsights, maxCauses   = 50, 10
)

const system = `You are the dashboard insight analyst of a DNS resolver fleet. The data block lists the current
candidates found by deterministic detectors, each with an id, type, severity, title, description and figures.
Correlate them (for example a SERVFAIL spike on an engine together with a slow upstream) and explain them for an
operator. Answer only with JSON of this shape:
{"insights":[{"candidate_id":"<id>","related_candidates":["<id>"],"severity":"info|warning|critical","confidence":0.0,
"title":"<at most 120 characters>","description":"<at most 1000 characters>",
"possible_causes":[{"cause":"<text>","confidence":0.0,"supporting_candidates":["<id>"]}],"recommended_actions":["<text>"]}]}
Every id you use must be a candidate id from the data block. Give at most one insight per candidate_id; confidences
are between 0 and 1. Recommended actions are suggestions for a human; never claim to have changed anything.`

// Agent is the dashboard insight agent. The model is asked only when the candidate set or a severity
// changed since its last answer, and at most once per MinLLMInterval.
type Agent struct {
	Store          *store.Store
	Service        *ai.Service
	MinLLMInterval time.Duration
	Now            func() time.Time // nil: time.Now

	mu      sync.Mutex
	lastLLM time.Time
	pending bool // a change the model has not explained yet
}

// Name returns "dashboard_insights".
func (a *Agent) Name() string { return Name }

type cause struct {
	Cause                string   `json:"cause"`
	Confidence           float64  `json:"confidence"`
	SupportingCandidates []string `json:"supporting_candidates"`
}

type insightAnswer struct {
	CandidateID        string   `json:"candidate_id"`
	RelatedCandidates  []string `json:"related_candidates"`
	Severity           string   `json:"severity"`
	Confidence         float64  `json:"confidence"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	PossibleCauses     []cause  `json:"possible_causes"`
	RecommendedActions []string `json:"recommended_actions"`
}

type output struct {
	Insights []insightAnswer `json:"insights"`
}

// Run detects, syncs the findings and, when due, asks the model to explain them.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	cands, err := Detect(ctx, a.Store.Pool, now)
	if err != nil {
		return err
	}
	changed, err := finding.Sync(ctx, a.Store, Kind, cands, now)
	if err != nil {
		return err
	}
	run.Detail["candidates"] = len(cands)
	a.pending = (a.pending || changed) && len(cands) > 0
	if !changed {
		run.Outcome = "no_change"
	}
	if !a.pending {
		return nil
	}
	if a.lastLLM.IsZero() {
		if a.lastLLM, err = lastLLMAt(ctx, a.Store); err != nil {
			return err
		}
	}
	if now.Sub(a.lastLLM) < a.MinLLMInterval {
		run.Detail["llm_deferred"] = true
		return nil
	}
	a.lastLLM = now
	run.Detail["llm_at"] = now
	run.Outcome = "ok"

	res, err := ai.Generate(ctx, a.Service, ai.Request[output]{
		Feature: Name, Priority: ai.Background, System: system, Prompt: prompt(cands),
		Validate: func(o *output) error { return validate(o, cands) },
	})
	run.Usage = res.Usage
	switch {
	case errors.Is(err, ai.ErrBudgetExhausted):
		run.Outcome = "skipped_budget"
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		// The detector's findings stay stored unexplained; the change is retried after the interval.
		run.Detail["llm_error"] = ai.Code(err)
		return nil
	}
	if err := finding.Explain(ctx, a.Store, Kind, explanations(res.Value, cands)); err != nil {
		return err
	}
	a.pending = false
	return nil
}

// lastLLMAt reads the newest model call time recorded by any instance's run.
func lastLLMAt(ctx context.Context, st *store.Store) (time.Time, error) {
	var at time.Time
	err := st.Pool.QueryRow(ctx, `select (detail->>'llm_at')::timestamptz from ai_agent_runs
		where agent = $1 and detail ? 'llm_at' order by started_at desc limit 1`, Name).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return at, store.MapError(err)
}

func prompt(cands []finding.Candidate) string {
	type item struct {
		ID          string         `json:"id"`
		Type        string         `json:"type"`
		Severity    string         `json:"severity"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Detail      map[string]any `json:"detail"`
	}
	items := make([]item, len(cands))
	for i, c := range cands {
		items[i] = item{c.ID, c.Type, c.Severity, c.Title, c.Description, c.Detail}
	}
	return "Explain and correlate these candidates.\n" + ai.DataBlock(map[string]any{"candidates": items})
}

func validate(o *output, cands []finding.Candidate) error {
	known := func(id string) bool {
		return slices.ContainsFunc(cands, func(c finding.Candidate) bool { return c.ID == id })
	}
	if len(o.Insights) > maxInsights {
		return fmt.Errorf("at most %d insights", maxInsights)
	}
	seen := map[string]bool{}
	for i, in := range o.Insights {
		at := fmt.Sprintf("insights[%d]", i)
		ids := slices.Concat([]string{in.CandidateID}, in.RelatedCandidates)
		for _, c := range in.PossibleCauses {
			ids = append(ids, c.SupportingCandidates...)
		}
		for _, id := range ids {
			if !known(id) {
				return fmt.Errorf("%s: %q is not a candidate id from the data block", at, id)
			}
		}
		switch {
		case seen[in.CandidateID]:
			return fmt.Errorf("%s: candidate_id %q is used twice", at, in.CandidateID)
		case finding.SeverityRank[in.Severity] == 0:
			return fmt.Errorf("%s: severity must be info, warning or critical", at)
		case in.Confidence < 0 || in.Confidence > 1:
			return fmt.Errorf("%s: confidence must be between 0 and 1", at)
		case in.Title == "" || len([]rune(in.Title)) > maxTitle:
			return fmt.Errorf("%s: title must have 1 to %d characters", at, maxTitle)
		case len([]rune(in.Description)) > maxDescription:
			return fmt.Errorf("%s: description must have at most %d characters", at, maxDescription)
		case len(in.PossibleCauses) > maxCauses:
			return fmt.Errorf("%s: at most %d possible_causes", at, maxCauses)
		}
		for _, c := range in.PossibleCauses {
			if c.Confidence < 0 || c.Confidence > 1 {
				return fmt.Errorf("%s: possible cause confidence must be between 0 and 1", at)
			}
		}
		seen[in.CandidateID] = true
	}
	return nil
}

// explanations converts a validated answer; engines are taken from the cited candidates' detail.
func explanations(o output, cands []finding.Candidate) []finding.Explanation {
	engineOf := map[string]string{}
	for _, c := range cands {
		if e, ok := c.Detail["engine"].(string); ok {
			engineOf[c.ID] = e
		}
	}
	out := make([]finding.Explanation, 0, len(o.Insights))
	for _, in := range o.Insights {
		engines := []string{}
		for _, id := range slices.Concat([]string{in.CandidateID}, in.RelatedCandidates) {
			if e := engineOf[id]; e != "" && !slices.Contains(engines, e) {
				engines = append(engines, e)
			}
		}
		related, actions, causes := in.RelatedCandidates, in.RecommendedActions, in.PossibleCauses
		if related == nil {
			related = []string{}
		}
		if actions == nil {
			actions = []string{}
		}
		if causes == nil {
			causes = []cause{}
		}
		out = append(out, finding.Explanation{
			CandidateID: in.CandidateID, Severity: in.Severity, Confidence: in.Confidence, Title: in.Title, Description: in.Description,
			Detail: map[string]any{"related_candidates": related, "possible_causes": causes, "recommended_actions": actions, "engines": engines},
		})
	}
	return out
}
