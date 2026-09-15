package anomaly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// AgentName is the agent and feature label.
const AgentName = "querylog_anomalies"

const (
	kind           = "anomaly"
	searchPage     = 1000 // the builtin backend's largest page
	baselinePeriod = 24 * time.Hour
	maxTitleRunes  = 120
	maxDescRunes   = 1000
	maxActions     = 10
	maxActionRunes = 500
	llmAtDetailKey = "llm_at"
	systemPrompt   = `You review DNS query-log anomaly candidates found by deterministic detectors on a home or enterprise DNS resolver.
For each candidate you can confirm from its evidence, return one anomaly with its candidate_id, a severity (info, warning or critical),
your confidence from 0 to 1, a title of at most 120 characters, a description of at most 1000 characters explaining what the traffic
suggests, and up to 10 short recommended actions for the operator. Leave out candidates the evidence does not support.
Answer with JSON only: {"anomalies":[{"candidate_id":"...","severity":"...","confidence":0.0,"title":"...","description":"...","recommended_actions":["..."]}]}`
)

// Agent is the query-log anomaly agent.
type Agent struct {
	Store          *store.Store
	Service        *ai.Service
	QueryLog       querylog.Backend
	Interval       time.Duration // the first window reaches back one interval
	MinLLMInterval time.Duration
	Now            func() time.Time

	mu      sync.Mutex
	lastLLM time.Time // zero: not loaded yet
	pending bool      // the candidate set changed since the last model call
}

// Name implements ai.Agent.
func (a *Agent) Name() string { return AgentName }

type anomalyAnswer struct {
	CandidateID        string   `json:"candidate_id"`
	Severity           string   `json:"severity"`
	Confidence         float64  `json:"confidence"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	RecommendedActions []string `json:"recommended_actions"`
}

type answer struct {
	Anomalies []anomalyAnswer `json:"anomalies"`
}

// Run detects candidates over the window since the last completed run, syncs them into ai_findings and,
// when the set changed and the last model call is at least MinLLMInterval old, asks the model to explain
// them. A failed model call leaves the detector's text (explained=false) and the run ok.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	if _, ok := a.QueryLog.(querylog.Noop); ok || a.QueryLog == nil {
		run.Outcome = "no_change"
		return nil
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.loadLLMAt(ctx); err != nil {
		return err
	}
	if !a.lastLLM.IsZero() {
		run.Detail[llmAtDetailKey] = a.lastLLM.Format(time.RFC3339Nano)
	}

	w, err := a.window(ctx, run.ID, now)
	if err != nil {
		return err
	}
	cands := Detect(w)
	changed, err := finding.Sync(ctx, a.Store, kind, cands, now)
	if err != nil {
		return fmt.Errorf("anomaly findings: %w", err)
	}
	run.Detail["records"], run.Detail["sampled"], run.Detail["candidates"] = len(w.Records), w.Sampled, len(cands)
	a.pending = (a.pending || changed) && len(cands) > 0
	if !a.pending || now.Sub(a.lastLLM) < a.MinLLMInterval {
		if !changed {
			run.Outcome = "no_change"
		}
		return nil
	}

	a.lastLLM = now
	run.Detail[llmAtDetailKey] = now.Format(time.RFC3339Nano)
	res, err := ai.Generate(ctx, a.Service, ai.Request[answer]{
		Feature: AgentName, Priority: ai.Background, System: systemPrompt,
		Prompt:   "Anomaly candidates of the window " + w.From.Format(time.RFC3339) + " to " + w.To.Format(time.RFC3339) + ":\n" + ai.DataBlock(promptCandidates(cands)),
		Validate: validator(cands),
	})
	run.Usage.InputTokens += res.Usage.InputTokens
	run.Usage.OutputTokens += res.Usage.OutputTokens
	switch {
	case errors.Is(err, ai.ErrBudgetExhausted):
		run.Outcome = "skipped_budget"
		return nil
	case err != nil && ctx.Err() != nil:
		return err
	case err != nil:
		run.Detail["llm_error"] = ai.Code(err)
		slog.Warn("AI anomaly explanation failed; findings keep the detector text", "agent", AgentName, "err", err)
		return nil
	}
	a.pending = false
	ex := make([]finding.Explanation, 0, len(res.Value.Anomalies))
	for _, an := range res.Value.Anomalies {
		ex = append(ex, finding.Explanation{CandidateID: an.CandidateID, Severity: an.Severity, Confidence: an.Confidence,
			Title: an.Title, Description: an.Description, Detail: map[string]any{"recommended_actions": an.RecommendedActions}})
	}
	if err := finding.Explain(ctx, a.Store, kind, ex); err != nil {
		return fmt.Errorf("anomaly explanations: %w", err)
	}
	run.Detail["explained"] = len(ex)
	return nil
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// loadLLMAt reads the last model call time from the newest run that recorded one, so another instance
// keeps the MinLLMInterval.
func (a *Agent) loadLLMAt(ctx context.Context) error {
	if !a.lastLLM.IsZero() {
		return nil
	}
	var at string
	err := a.Store.Pool.QueryRow(ctx, `select detail->>'llm_at' from ai_agent_runs where agent = $1 and detail->>'llm_at' is not null
		order by started_at desc limit 1`, AgentName).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return store.MapError(err)
	}
	if t, perr := time.Parse(time.RFC3339Nano, at); perr == nil {
		a.lastLLM = t
	}
	return nil
}

// window reads the records since the last completed run (or one interval back), the 24 h client
// baselines and the threat blocks of the 24 h before the window.
func (a *Agent) window(ctx context.Context, runID int64, now time.Time) (Window, error) {
	var last *time.Time
	if err := a.Store.Pool.QueryRow(ctx, `select max(started_at) from ai_agent_runs where agent = $1 and id <> $2
		and outcome in ('ok', 'no_change', 'skipped_budget')`, AgentName, runID).Scan(&last); err != nil {
		return Window{}, store.MapError(err)
	}
	w := Window{From: now.Add(-a.Interval), To: now}
	if last != nil && last.Before(now) {
		w.From = *last
	}
	var err error
	if w.Records, w.Sampled, err = a.search(ctx, querylog.Query{From: w.From, To: w.To}); err != nil {
		return Window{}, err
	}
	if topper, ok := a.QueryLog.(querylog.Topper); ok {
		// Clients outside the 5,000 busiest of the day get baseline 0; their window QPS alone decides.
		top, err := topper.Top(ctx, querylog.TopQuery{From: now.Add(-baselinePeriod), To: now, Field: querylog.TopClient, Limit: MaxRecords})
		if err != nil {
			return Window{}, err
		}
		w.Baseline = make(map[string]float64, len(top))
		for _, e := range top {
			w.Baseline[e.Key] = float64(e.Count) / baselinePeriod.Seconds()
		}
	}
	prior, _, err := a.search(ctx, querylog.Query{From: w.From.Add(-baselinePeriod), To: w.From.Add(-time.Nanosecond),
		Filters: []string{"blocked"}, Categories: ThreatCategories})
	if err != nil {
		return Window{}, err
	}
	w.PriorThreats = map[string]bool{}
	for _, r := range prior {
		w.PriorThreats["client:"+r.Client] = true
		if r.PolicyGroupID != "" {
			w.PriorThreats["group:"+r.PolicyGroupID] = true
		}
	}
	return w, nil
}

// search pages q newest first up to MaxRecords; sampled reports that more records matched.
func (a *Agent) search(ctx context.Context, q querylog.Query) (recs []querylog.Record, sampled bool, err error) {
	for {
		q.Limit = min(searchPage, MaxRecords-len(recs))
		page, err := a.QueryLog.Search(ctx, q)
		if err != nil {
			return nil, false, fmt.Errorf("anomaly window: %w", err)
		}
		recs = append(recs, page.Records...)
		if page.NextCursor == "" {
			return recs, false, nil
		}
		if len(recs) >= MaxRecords {
			return recs, true, nil
		}
		q.Cursor = page.NextCursor
	}
}

type promptCandidate struct {
	ID          string         `json:"candidate_id"`
	Type        string         `json:"type"`
	Severity    string         `json:"detector_severity"`
	Description string         `json:"detector_description"`
	Detail      map[string]any `json:"evidence"`
}

func promptCandidates(cands []finding.Candidate) []promptCandidate {
	out := make([]promptCandidate, len(cands))
	for i, c := range cands {
		out[i] = promptCandidate{ID: c.ID, Type: c.Type, Severity: c.Severity, Description: c.Description, Detail: c.Detail}
	}
	return out
}

// validator accepts only candidate ids from cands, each once, with a known severity, confidence in 0..1
// and bounded texts.
func validator(cands []finding.Candidate) func(*answer) error {
	return func(v *answer) error {
		seen := map[string]bool{}
		for i, an := range v.Anomalies {
			switch {
			case !slices.ContainsFunc(cands, func(c finding.Candidate) bool { return c.ID == an.CandidateID }):
				return fmt.Errorf("anomalies[%d].candidate_id %q is not one of the candidate ids", i, an.CandidateID)
			case seen[an.CandidateID]:
				return fmt.Errorf("anomalies[%d].candidate_id %q appears twice", i, an.CandidateID)
			case finding.SeverityRank[an.Severity] == 0:
				return fmt.Errorf("anomalies[%d].severity %q is not info, warning or critical", i, an.Severity)
			case an.Confidence < 0 || an.Confidence > 1:
				return fmt.Errorf("anomalies[%d].confidence %v is not between 0 and 1", i, an.Confidence)
			case an.Title == "" || utf8.RuneCountInString(an.Title) > maxTitleRunes:
				return fmt.Errorf("anomalies[%d].title must be 1 to %d characters", i, maxTitleRunes)
			case an.Description == "" || utf8.RuneCountInString(an.Description) > maxDescRunes:
				return fmt.Errorf("anomalies[%d].description must be 1 to %d characters", i, maxDescRunes)
			case len(an.RecommendedActions) > maxActions:
				return fmt.Errorf("anomalies[%d].recommended_actions has more than %d entries", i, maxActions)
			}
			for _, act := range an.RecommendedActions {
				if act == "" || utf8.RuneCountInString(act) > maxActionRunes {
					return fmt.Errorf("anomalies[%d].recommended_actions entries must be 1 to %d characters", i, maxActionRunes)
				}
			}
			seen[an.CandidateID] = true
		}
		return nil
	}
}
