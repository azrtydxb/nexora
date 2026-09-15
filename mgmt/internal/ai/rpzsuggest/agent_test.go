package rpzsuggest_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/ai/rpzsuggest"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// emptyLog is a query log backend without records.
type emptyLog struct{}

func (emptyLog) Name() string { return "empty" }
func (emptyLog) Search(context.Context, querylog.Query) (querylog.Page, error) {
	return querylog.Page{}, nil
}

func rule(record string, confidence float64) map[string]any {
	return map[string]any{"record": record, "policy": "nxdomain", "category": "malware",
		"reason": "suggested for " + record, "confidence": confidence}
}

// TestRpzSuggestionAgent catches rules stored without the proposal validator (a record under a hosted zone
// would reach the zone file), a priority not taken from the confidence, and several rules merged into one
// proposal instead of one proposal per record.
func TestRpzSuggestionAgent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Now()
	b := seedTraffic(t, st, now)

	model := aifake.Model(
		aifake.JSON(map[string]any{"rules": []any{rule("bad.ars.test", 0.8), rule("*.fam.ars.test", 0.95), rule("www.corp.example", 0.99)}}),
		aifake.JSON(map[string]any{"rules": []any{rule("bad.ars.test", 0.8), rule("*.fam.ars.test", 0.95)}}),
	)
	agent := &rpzsuggest.Agent{Store: st, Service: aifake.Service(t, st, model, nil), QueryLog: b,
		Validator: &proposal.Validator{Store: st}, Now: func() time.Time { return now }}
	if agent.Name() != "rpz_suggestions" {
		t.Fatalf("name %q", agent.Name())
	}

	run := &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if n := len(model.RecordedCalls()); n != 2 {
		t.Fatalf("model calls = %d, want 2 (the rule under the hosted zone is re-asked)", n)
	}

	props, err := proposal.List(ctx, st, proposal.Filter{Source: "rpz_suggestions", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("open proposals = %d, want 2: %+v", len(props), props)
	}
	byRecord := map[string]string{}
	for _, p := range props {
		if len(p.Actions) != 1 || p.Actions[0].OperationID != proposal.OpAppendAiRpzRules {
			t.Fatalf("actions of %q = %+v", p.Title, p.Actions)
		}
		rules, err := proposal.DecodeRules(p.Actions[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != 1 {
			t.Fatalf("proposal %q carries %d rules, want 1", p.Title, len(rules))
		}
		if want := "Block " + rules[0].Record; p.Title != want {
			t.Fatalf("title %q, want %q", p.Title, want)
		}
		byRecord[rules[0].Record] = p.Priority
	}
	if byRecord["bad.ars.test"] != "medium" || byRecord["*.fam.ars.test"] != "high" {
		t.Fatalf("priorities %+v, want medium for 0.8 and high for 0.95", byRecord)
	}
	var evidence struct {
		Kind string `json:"kind"`
	}
	for _, p := range props {
		if err := json.Unmarshal(p.Evidence, &evidence); err != nil || evidence.Kind == "" {
			t.Fatalf("evidence of %q = %s (%v)", p.Title, p.Evidence, err)
		}
	}
	if run.Outcome != "ok" {
		t.Fatalf("outcome %q, detail %+v", run.Outcome, run.Detail)
	}
}

// TestRpzSuggestionAgentWithoutInputs catches a run that calls the model with nothing to suggest.
func TestRpzSuggestionAgentWithoutInputs(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	model := aifake.Model()
	agent := &rpzsuggest.Agent{Store: st, Service: aifake.Service(t, st, model, nil), QueryLog: emptyLog{},
		Validator: &proposal.Validator{Store: st}, Now: time.Now}
	run := &ai.Run{Agent: agent.Name(), Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "no_change" || len(model.RecordedCalls()) != 0 {
		t.Fatalf("outcome %q with %d model calls, want no_change and none", run.Outcome, len(model.RecordedCalls()))
	}
}
