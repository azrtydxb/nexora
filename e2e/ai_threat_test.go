package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestAIThreatLabelsInQueryLog catches a threat check whose verdict never reaches the query log, and a
// query-log read that calls the model instead of reading the cached verdicts.
func TestAIThreatLabelsInQueryLog(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{QueryLogBackend: "builtin", ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	eng := env.StartManagedEngine("ai-threat-1", []string{mg.GRPCURL}, admin.CreateJoinToken())

	lists := env.StartHTTPFixture()
	lists.SetList(t, "ait-block", "evil.ait.test\n")
	var fl map[string]any
	admin.Must(http.MethodPost, "/filter-lists", map[string]any{"name": "ait-block", "kind": "block",
		"url": lists.URL("ait-block"), "refresh_interval_seconds": 3600, "enabled": true}, &fl, http.StatusCreated)
	admin.Must(http.MethodPost, "/filter-lists/"+fl["id"].(string)+"/refresh", nil, nil, http.StatusOK)
	waitLatestApplied(t, admin, "ai-threat-1")
	harness.MustQuery(t, eng.DNS, "evil.ait.test.", dns.TypeA, harness.QueryOpts{})

	type record struct {
		Name   string `json:"name"`
		Threat *struct {
			IsThreat   bool     `json:"is_threat"`
			Categories []string `json:"categories"`
			Confidence float64  `json:"confidence"`
		} `json:"threat"`
	}
	type page struct {
		Records []record `json:"records"`
	}
	logged := func() error {
		var p page
		admin.Must(http.MethodGet, "/query-log?name=evil.ait", nil, &p, http.StatusOK)
		if len(p.Records) == 0 {
			return fmt.Errorf("no record for evil.ait.test yet")
		}
		return nil
	}
	harness.Eventually(t, 60*time.Second, logged)

	fx.ScriptJSON(t, "threat_check", map[string]any{"verdicts": []any{map[string]any{
		"name": "evil.ait.test", "is_threat": true, "categories": []string{"malware"},
		"confidence": 0.93, "reasoning": "blocked by a malware list and queried by a client",
	}}})
	var task struct {
		ID string `json:"id"`
	}
	admin.Must(http.MethodPost, "/ai/threat-check", map[string]any{"domains": []string{"evil.ait.test"}}, &task, http.StatusAccepted)
	result, ok := waitAITask(admin, task.ID, 60*time.Second)["result"].(map[string]any)
	if !ok {
		t.Fatalf("task result = %v", result)
	}
	results, _ := result["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("threat check results = %v", result["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["is_threat"] != true || first["blocked_by"] == "" || first["query_count"].(float64) < 1 {
		t.Fatalf("verdict = %+v", first)
	}

	// The query-log read answers from the cache: the fixture sees no call at all.
	fx.Reset(t)
	var p page
	admin.Must(http.MethodGet, "/query-log?name=evil.ait", nil, &p, http.StatusOK)
	labelled := false
	for _, r := range p.Records {
		if r.Threat != nil && r.Threat.IsThreat && len(r.Threat.Categories) == 1 && r.Threat.Categories[0] == "malware" {
			labelled = true
		}
	}
	if !labelled {
		t.Fatalf("no threat label on the query-log records: %+v", p.Records)
	}
	if reqs := fx.Requests(t); len(reqs) != 0 {
		t.Fatalf("the query-log read called the model %d time(s): %+v", len(reqs), reqs)
	}
}
