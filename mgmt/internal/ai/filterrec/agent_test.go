package filterrec_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/filterrec"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// TestFilterRecommendationAgent catches impact numbers taken from the model, a missing or wrong action per
// kind, duplicated proposals on a second run and a dismissed proposal coming back.
func TestFilterRecommendationAgent(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	g := seedGuest(t, st)
	now := time.Now()
	b := querylog.NewBuiltin(1000)
	ingest(b, now, "10.9.0.5", "miner.aif.test", 30)
	ingest(b, now, "10.9.0.5", "www.youtube.com", 20)
	ingest(b, now, "10.9.0.5", "ok.aif.test", 50)
	ingest(b, now, "10.9.0.5", "tracker.aif.test", 10)

	bogus := map[string]any{"additional_blocked_queries": 999999, "total_queries_analyzed": 999999, "coverage_percent": 99.9}
	answer := map[string]any{"recommendations": []map[string]any{
		{"kind": "enable_category", "policy_group_id": g.ID.String(), "category_key": "malware", "priority": "high",
			"title": "Block malware for guest", "description": "Guest clients query names on the malware lists.", "impact": bogus},
		{"kind": "block_domains", "policy_group_id": "", "domains": []string{"Tracker.aif.test."}, "priority": "medium",
			"title": "Block tracker.aif.test", "description": "A tracker queried by guest clients.", "impact": bogus},
		{"kind": "safe_search", "policy_group_id": g.ID.String(), "priority": "low", "title": "Strict YouTube for guest",
			"description": "Guest clients watch YouTube without restrictions.", "impact": bogus,
			"safe_search": map[string]any{"google": false, "bing": false, "duckduckgo": false, "youtube": "strict"}},
	}}
	m := aifake.Model(aifake.JSON(answer), aifake.JSON(answer), aifake.JSON(answer))
	agent := &filterrec.Agent{Store: st, Service: aifake.Service(t, st, m, nil), QueryLog: b, Catalog: loadCatalog(t),
		Validator: &proposal.Validator{Store: st}, Now: func() time.Time { return now }}
	if agent.Name() != "filter_recommendations" {
		t.Fatalf("name %q", agent.Name())
	}
	list := func(status string) []proposal.Proposal {
		t.Helper()
		ps, err := proposal.List(ctx, st, proposal.Filter{Source: "filter_recommendations", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return ps
	}
	runOnce := func() *ai.Run {
		t.Helper()
		run := &ai.Run{Agent: "filter_recommendations"}
		if err := agent.Run(ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}

	run := runOnce()
	open := list("open")
	if len(open) != 3 || run.Detail["created"] != 3 {
		t.Fatalf("first run: %d open proposals, detail %v", len(open), run.Detail)
	}
	byTitle := map[string]proposal.Proposal{}
	for _, p := range open {
		byTitle[p.Title] = p
	}
	enable, block, safe := byTitle["Block malware for guest"], byTitle["Block tracker.aif.test"], byTitle["Strict YouTube for guest"]
	var imp struct {
		Additional int64   `json:"additional_blocked_queries"`
		Total      int64   `json:"total_queries_analyzed"`
		Coverage   float64 `json:"coverage_percent"`
	}
	if err := json.Unmarshal(enable.Impact, &imp); err != nil || imp.Additional != 30 || imp.Total != 110 || imp.Coverage != 27.3 {
		t.Fatalf("enable impact %s (%v), want 30 of 110 = 27.3%%", enable.Impact, err)
	}
	var body struct {
		CategoryKeys []string           `json:"category_keys"`
		SafeSearch   map[string]any     `json:"safe_search"`
		Rules        []proposal.RPZRule `json:"rules"`
	}
	if len(enable.Actions) != 1 || enable.Actions[0].OperationID != "updatePolicyGroup" || enable.Actions[0].PathParams["id"] != g.ID.String() ||
		json.Unmarshal(enable.Actions[0].Body, &body) != nil || len(body.CategoryKeys) != 1 || body.CategoryKeys[0] != "malware" {
		t.Fatalf("enable actions %+v", enable.Actions)
	}
	if len(block.Actions) != 1 || block.Actions[0].OperationID != "appendAiRpzRules" {
		t.Fatalf("block actions %+v", block.Actions)
	}
	rules, err := proposal.DecodeRules(block.Actions[0])
	if err != nil || len(rules) != 1 || rules[0].Record != "tracker.aif.test" || rules[0].Policy != "nxdomain" {
		t.Fatalf("block rules %+v %v", rules, err)
	}
	if err := json.Unmarshal(block.Impact, &imp); err != nil || imp.Additional != 10 {
		t.Fatalf("block impact %s", block.Impact)
	}
	if len(safe.Actions) != 1 || safe.Actions[0].OperationID != "updatePolicyGroup" ||
		json.Unmarshal(safe.Actions[0].Body, &body) != nil || body.SafeSearch["youtube"] != "strict" {
		t.Fatalf("safe search actions %+v", safe.Actions)
	}
	if err := json.Unmarshal(safe.Impact, &imp); err != nil || imp.Additional != 20 {
		t.Fatalf("safe search impact %s", safe.Impact)
	}

	run = runOnce()
	if all := list(""); len(all) != 3 || run.Detail["refreshed"] != 3 || run.Detail["created"] != 0 {
		t.Fatalf("second run: %d proposals, detail %v", len(all), run.Detail)
	}

	if err := proposal.Dismiss(ctx, st, block.ID, "admin", "not now"); err != nil {
		t.Fatal(err)
	}
	run = runOnce()
	all := list("")
	if len(all) != 3 || len(list("open")) != 2 || run.Detail["suppressed_dismissed"] != 1 {
		t.Fatalf("third run: %d proposals, %d open, detail %v", len(all), len(list("open")), run.Detail)
	}
	if p, err := proposal.Get(ctx, st.Pool, block.ID); err != nil || p.Status != "dismissed" {
		t.Fatalf("dismissed proposal is %q (%v)", p.Status, err)
	}
	if n := len(m.RecordedCalls()); n != 3 {
		t.Fatalf("model calls %d, want one per run", n)
	}
}
