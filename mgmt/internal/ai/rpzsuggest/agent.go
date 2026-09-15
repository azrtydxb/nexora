package rpzsuggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Name is the agent, proposal source and feature label.
const Name = "rpz_suggestions"

const (
	maxRules = proposal.MaxRPZRules
	// maxReason and maxCategory bound the free text of one rule; the zone file truncates the reason to 200.
	maxReason   = 500
	maxCategory = 64
	// highConfidence and mediumConfidence map a rule's confidence to the proposal priority.
	highConfidence   = 0.9
	mediumConfidence = 0.7
)

// rule is one suggested RPZ record of the model output.
type rule struct {
	Record     string  `json:"record"`
	Policy     string  `json:"policy"` // nxdomain|nodata|drop|passthru
	Category   string  `json:"category"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

type answer struct {
	Rules []rule `json:"rules"`
}

// Agent is the rpz_suggestions background agent. It never writes the zone: every rule it accepts becomes
// one open proposal an operator has to apply.
type Agent struct {
	Store     *store.Store
	Service   *ai.Service
	QueryLog  querylog.Backend
	Validator *proposal.Validator
	Now       func() time.Time
}

// Name implements ai.Agent.
func (a *Agent) Name() string { return Name }

const system = `You review candidate DNS names selected from 24 hours of traffic and suggest RPZ rules that block the harmful ones. Suggest a rule only for a name the evidence condemns; suggest nothing when nothing is worth blocking.
Answer only JSON: {"rules":[{"record","policy","category","reason","confidence"}]}, at most 50 rules.
- record must be one of the names given to you, copied exactly (a name starting with *. blocks that whole subtree).
- policy is nxdomain (answer the name does not exist, the usual choice), nodata (answer with no records), drop (do not answer at all) or passthru (never block this name).
- category is a short label such as malware, phishing, tunnelling, typosquatting or spam.
- reason explains the evidence in at most 500 characters; confidence is between 0 and 1.
Kinds of candidate: threat (a cached threat verdict), candidate (a name of an open finding), family (many random-looking names under one parent, suggested as a wildcard) and lookalike (a near-miss of a hosted zone, typical of typo-squatting).`

// Run collects the candidate names, asks the model for rules and stores every rule as its own proposal.
func (a *Agent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	inputs, err := Collect(ctx, a.Store, a.QueryLog, now)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		run.Outcome, run.Detail = "no_change", map[string]any{"inputs": 0}
		return nil
	}
	byName := map[string]Input{}
	for _, in := range inputs {
		byName[in.Name] = in
	}
	var drafts []proposal.Draft
	res, err := ai.Generate(ctx, a.Service, ai.Request[answer]{
		Feature: Name, Priority: ai.Background, System: system,
		Prompt: "Candidate names:\n" + ai.DataBlock(map[string]any{"candidates": inputs}),
		Validate: func(ans *answer) error {
			d, err := a.drafts(ctx, byName, ans.Rules)
			if err == nil {
				drafts = d
			}
			return err
		},
	})
	run.Usage = res.Usage
	if errors.Is(err, ai.ErrBudgetExhausted) {
		run.Outcome = "skipped_budget"
		return nil
	}
	if err != nil {
		return err
	}
	created, refreshed, suppressed := 0, 0, 0
	for _, d := range drafts {
		id, isNew, err := proposal.Upsert(ctx, a.Store, d)
		switch {
		case err != nil:
			return fmt.Errorf("store proposal %q: %w", d.Title, err)
		case id == uuid.Nil:
			suppressed++
		case isNew:
			created++
		default:
			refreshed++
		}
	}
	run.Detail = map[string]any{"inputs": len(inputs), "rules": len(drafts), "created": created, "refreshed": refreshed,
		"suppressed_dismissed": suppressed}
	if created == 0 && refreshed == 0 {
		run.Outcome = "no_change"
	}
	return nil
}

// drafts turns the suggested rules into one validated proposal draft each; the first problem is returned
// so the model can correct its answer.
func (a *Agent) drafts(ctx context.Context, byName map[string]Input, rules []rule) ([]proposal.Draft, error) {
	if len(rules) > maxRules {
		return nil, fmt.Errorf("at most %d rules, got %d", maxRules, len(rules))
	}
	var out []proposal.Draft
	seen := map[string]bool{}
	for i, r := range rules {
		d, err := a.draft(ctx, byName, r)
		if err != nil {
			return nil, fmt.Errorf("rules[%d] (%s): %w", i, r.Record, err)
		}
		record := normalName(r.Record)
		if seen[record] {
			return nil, fmt.Errorf("rules[%d]: record %s is suggested twice", i, record)
		}
		seen[record] = true
		out = append(out, d)
	}
	return out, nil
}

func (a *Agent) draft(ctx context.Context, byName map[string]Input, r rule) (proposal.Draft, error) {
	record := normalName(r.Record)
	in, ok := byName[record]
	switch {
	case !ok:
		return proposal.Draft{}, errors.New("record is not one of the candidate names")
	case r.Confidence < 0 || r.Confidence > 1:
		return proposal.Draft{}, fmt.Errorf("confidence %v must be between 0 and 1", r.Confidence)
	case strings.TrimSpace(r.Category) == "" || utf8.RuneCountInString(r.Category) > maxCategory:
		return proposal.Draft{}, fmt.Errorf("category needs 1 to %d characters", maxCategory)
	case strings.TrimSpace(r.Reason) == "" || utf8.RuneCountInString(r.Reason) > maxReason:
		return proposal.Draft{}, fmt.Errorf("reason needs 1 to %d characters", maxReason)
	}
	body, err := json.Marshal(map[string]any{"rules": []proposal.RPZRule{{Record: record, Policy: r.Policy,
		Category: strings.TrimSpace(r.Category), Reason: strings.TrimSpace(r.Reason), Confidence: r.Confidence}}})
	if err != nil {
		return proposal.Draft{}, err
	}
	// debt: the reason and the confidence are the model's words, so they are part of the proposal
	// fingerprint and a reworded answer can raise a dismissed record again; revisit when a dismissal is
	// keyed on the record instead of the fingerprint.
	actions := []proposal.Action{{OperationID: proposal.OpAppendAiRpzRules, PathParams: map[string]string{}, Body: body}}
	if err := a.Validator.Validate(ctx, actions); err != nil {
		return proposal.Draft{}, err
	}
	return proposal.Draft{Source: Name, Title: "Block " + record, Description: strings.TrimSpace(r.Reason),
		Priority: priority(r.Confidence),
		Impact:   map[string]any{"queries_in_window": in.Queries, "clients_in_window": in.Clients},
		Evidence: map[string]any{"kind": in.Kind, "name": in.Name, "evidence": in.Evidence, "policy": r.Policy,
			"category": strings.TrimSpace(r.Category), "confidence": r.Confidence},
		Actions: actions}, nil
}

func priority(confidence float64) string {
	switch {
	case confidence >= highConfidence:
		return "high"
	case confidence >= mediumConfidence:
		return "medium"
	default:
		return "low"
	}
}
