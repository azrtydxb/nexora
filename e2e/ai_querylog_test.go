package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestAIQueryLogSearch catches a natural-language search whose filters are not the exact searchQueryLog
// parameters, a search that is not run with the translated filters, and a summary that is not returned.
func TestAIQueryLogSearch(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{QueryLogBackend: "builtin", ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	eng := env.StartManagedEngine("ai-qlsearch-1", []string{mg.GRPCURL}, admin.CreateJoinToken())

	lists := env.StartHTTPFixture()
	lists.SetList(t, "aiq-block", "blocked.aiq.test\n")
	var fl map[string]any
	admin.Must(http.MethodPost, "/filter-lists", map[string]any{"name": "aiq-block", "kind": "block", "url": lists.URL("aiq-block"), "refresh_interval_seconds": 3600, "enabled": true}, &fl, http.StatusCreated)
	admin.Must(http.MethodPost, "/filter-lists/"+fl["id"].(string)+"/refresh", nil, nil, http.StatusOK)
	waitLatestApplied(t, admin, "ai-qlsearch-1")
	harness.MustQuery(t, eng.DNS, "blocked.aiq.test.", dns.TypeA, harness.QueryOpts{})

	type page struct {
		Records []struct {
			Name   string `json:"name"`
			Filter string `json:"filter"`
		} `json:"records"`
	}
	hasBlocked := func(query string) error {
		var p page
		admin.Must(http.MethodGet, "/query-log?"+query, nil, &p, http.StatusOK)
		for _, r := range p.Records {
			if strings.TrimSuffix(r.Name, ".") == "blocked.aiq.test" && r.Filter == "blocked" {
				return nil
			}
		}
		return fmt.Errorf("no blocked record for blocked.aiq.test in %q: %+v", query, p.Records)
	}
	harness.Eventually(t, 60*time.Second, func() error { return hasBlocked("name=blocked.aiq") })

	fx.ScriptJSON(t, "querylog_search",
		map[string]any{"filter": []string{"blocked"}, "name": "blocked.aiq", "explanation": "e"},
		map[string]any{"summary": "1 blocked query", "suggestions": []string{}})
	var task struct {
		ID string `json:"id"`
	}
	admin.Must(http.MethodPost, "/ai/query-log/search", map[string]string{"query": "blocked queries for blocked.aiq in the last hour"}, &task, http.StatusAccepted)
	result, ok := waitAITask(admin, task.ID, 60*time.Second)["result"].(map[string]any)
	if !ok || result["summary"] != "1 blocked query" || result["explanation"] != "e" || result["total_shown"].(float64) < 1 {
		t.Fatalf("task result = %+v", result)
	}
	filters, _ := result["filters"].(map[string]any)
	params := url.Values{}
	for k, v := range filters {
		switch v := v.(type) {
		case string:
			params.Add(k, v)
		case []any:
			for _, item := range v {
				params.Add(k, fmt.Sprint(item))
			}
		default:
			t.Fatalf("filter %s has type %T", k, v)
		}
	}
	if params.Get("name") != "blocked.aiq" || params.Get("filter") != "blocked" || params.Get("from") == "" || params.Get("to") == "" {
		t.Fatalf("filters = %+v", filters)
	}
	if err := hasBlocked(params.Encode()); err != nil {
		t.Fatal(err)
	}
}
