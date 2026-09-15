package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// aiStatusView is the part of getAiStatus the AI tests inspect.
type aiStatusView struct {
	Enabled  bool   `json:"enabled"`
	Reason   string `json:"reason"`
	Features struct {
		QuerylogSearch bool `json:"querylog_search"`
	} `json:"features"`
	Agents []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"agents"`
}

func aiStatus(a *harness.API) (aiStatusView, string) {
	a.T.Helper()
	var raw json.RawMessage
	a.Must(http.MethodGet, "/ai/status", nil, &raw, http.StatusOK)
	var s aiStatusView
	if err := json.Unmarshal(raw, &s); err != nil {
		a.T.Fatal(err)
	}
	return s, string(raw)
}

// taskKindRegistered reports whether the instance implements the task kind (status features).
func taskKindRegistered(a *harness.API, kind string) bool {
	s, _ := aiStatus(a)
	return kind == "querylog_search" && s.Features.QuerylogSearch
}

// agentRegistered reports whether the instance runs the agent (status reports only registered agents
// as enabled).
func agentRegistered(a *harness.API, agent string) bool {
	s, _ := aiStatus(a)
	for _, st := range s.Agents {
		if st.Name == agent {
			return st.Enabled
		}
	}
	return false
}

// mgmtMetricsText is the instance's /metrics exposition.
func mgmtMetricsText(t *testing.T, mg *harness.Mgmt) string {
	t.Helper()
	resp, err := http.Get(mg.BaseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape /metrics: %d %v", resp.StatusCode, err)
	}
	return string(b)
}

// pgInt runs a query returning one integer.
func pgInt(t *testing.T, url, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	return n
}

// waitAITask polls getAiTask until the task finishes and fails the test unless it succeeded.
func waitAITask(a *harness.API, id string, timeout time.Duration) map[string]any {
	a.T.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var task map[string]any
		a.Must(http.MethodGet, "/ai/tasks/"+id, nil, &task, http.StatusOK)
		switch task["status"] {
		case "succeeded":
			return task
		case "failed":
			a.T.Fatalf("task %s failed: %v", id, task)
		}
		if time.Now().After(deadline) {
			a.T.Fatalf("task %s not finished within %s: %v", id, timeout, task)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestAIOpenAICompatibleWire catches a wrong OpenAI wire format (auth header, model, response_format,
// max_tokens), lost reasoning-token accounting, a status that leaks the key, and a prompt-mode
// structured output that still sends response_format or cannot read a fenced answer.
func TestAIOpenAICompatibleWire(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	if !taskKindRegistered(admin, "querylog_search") {
		t.Skip("needs M11 Task 12")
	}

	search := func(a *harness.API, fenced bool) {
		t.Helper()
		now := time.Now().UTC()
		filters := fmt.Sprintf(`{"from":%q,"to":%q,"explanation":"e"}`, now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		summary := `{"summary":"quiet hour","suggestions":[]}`
		if fenced {
			filters, summary = "```json\n"+filters+"\n```", "```json\n"+summary+"\n```"
		}
		fx.Script(t, "querylog_search",
			harness.OpenAIResponse{Content: filters, Reasoning: "thinking", PromptTokens: 100, CompletionTokens: 50, ReasoningTokens: 900},
			harness.OpenAIResponse{Content: summary, PromptTokens: 100, CompletionTokens: 20})
		var task struct {
			ID string `json:"id"`
		}
		a.Must(http.MethodPost, "/ai/query-log/search", map[string]string{"query": "everything in the last hour"}, &task, http.StatusAccepted)
		waitAITask(a, task.ID, 60*time.Second)
	}

	search(admin, false)
	reqs := fx.Requests(t)
	if len(reqs) != 2 {
		t.Fatalf("fixture requests = %+v, want 2", reqs)
	}
	for _, r := range reqs {
		if r.Feature != "querylog_search" || r.Model != "fake-qwen" || !r.Bearer || r.ResponseFormat != "json_schema" || r.MaxTokens != 16384 {
			t.Fatalf("wire request = %+v", r)
		}
	}
	if got := mg.Metric(t, "nexora_mgmt_ai_tokens_total", map[string]string{"feature": "querylog_search", "kind": "reasoning"}); got != 900 {
		t.Fatalf("reasoning tokens = %v, want 900", got)
	}
	if _, raw := aiStatus(admin); strings.Contains(raw, "test-key-not-secret") {
		t.Fatalf("status leaks the API key: %s", raw)
	}

	mg.Proc.Stop()
	fx.Reset(t)
	mg2 := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx, "NEXORA_AI_STRUCTURED_OUTPUT=prompt")})
	admin2 := env.NewAPI(mg2.BaseURL)
	admin2.Bearer = admin.Bearer
	search(admin2, true)
	reqs = fx.Requests(t)
	if len(reqs) != 2 {
		t.Fatalf("prompt-mode fixture requests = %+v, want 2", reqs)
	}
	for _, r := range reqs {
		if r.ResponseFormat != "" {
			t.Fatalf("prompt mode sent response_format: %+v", r)
		}
	}
}

// TestAIDisabledChangesNothing catches an unconfigured instance that contacts an AI endpoint, runs an
// agent, exposes AI state metrics or serves an AI operation.
func TestAIDisabledChangesNothing(t *testing.T) {
	env := harness.New(t)
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	dnsFx := env.StartDNSFixture()
	dnsFx.SetRecords(t, "ai.example.test. 300 IN A 203.0.113.20")

	// traffic bootstraps mg and sends at least queries queries, for at least d, through its managed engine.
	traffic := func(mg *harness.Mgmt, node string, d time.Duration, queries int) (*harness.API, *harness.Engine) {
		t.Helper()
		admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
		admin.DisableForwardedValidation()
		admin.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": dnsFx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
		eng := env.StartManagedEngine(node, []string{mg.GRPCURL}, admin.CreateJoinToken())
		waitLatestApplied(t, admin, node)
		c := &dns.Client{Timeout: time.Second}
		deadline := time.Now().Add(d)
		for i := 0; i < queries || time.Now().Before(deadline); i++ {
			_, _, _ = c.Exchange(question("ai.example.test.", dns.TypeA), eng.DNS)
			if i >= queries {
				time.Sleep(500 * time.Millisecond)
			}
		}
		return admin, eng
	}

	// Positive path: a configured instance is on, exposes the AI state metrics and runs its agents.
	pgOn := env.StartPostgres()
	fx.ScriptJSON(t, "querylog_anomalies", map[string]any{"anomalies": []any{}})
	on := env.StartMgmt(pgOn, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx, "NEXORA_AI_QUERYLOG_INTERVAL=2s", "NEXORA_AI_AGENT_START_DELAY=0s")})
	onAPI, engOn := traffic(on, "ai-on-1", 0, 20)
	if s, _ := aiStatus(onAPI); !s.Enabled {
		t.Fatalf("configured instance status = %+v", s)
	}
	if text := mgmtMetricsText(t, on); !strings.Contains(text, "nexora_mgmt_ai_enabled 1") || !strings.Contains(text, "nexora_mgmt_ai_budget_used_ratio") {
		t.Fatal("configured instance does not expose nexora_mgmt_ai_enabled 1 and the budget ratio")
	}
	if agentRegistered(onAPI, "querylog_anomalies") { // debt: M11 Task 13 registers the agent; remove this condition then
		harness.EventuallyTrue(t, 60*time.Second, func() bool {
			return len(fx.Requests(t)) > 0 || strings.Contains(mgmtMetricsText(t, on), `nexora_mgmt_ai_agent_runs_total{agent="querylog_anomalies"`)
		}, "the configured instance runs the query-log anomaly agent")
	}
	engOn.Proc.Stop()
	on.Proc.Stop()
	fx.Reset(t)

	pgOff := env.StartPostgres()
	off := env.StartMgmt(pgOff, ca, harness.MgmtOptions{})
	offAPI, _ := traffic(off, "ai-off-1", 60*time.Second, 20)

	if reqs := fx.Requests(t); len(reqs) != 0 {
		t.Fatalf("unconfigured instance contacted the AI endpoint: %+v", reqs)
	}
	if s, _ := aiStatus(offAPI); s.Enabled || s.Reason != "not_configured" {
		t.Fatalf("unconfigured status = %+v", s)
	}
	if status, code := offAPI.ErrorCode(http.MethodGet, "/ai/proposals", nil); status != http.StatusServiceUnavailable || code != "ai_disabled" {
		t.Fatalf("listAiProposals while off = %d %s", status, code)
	}
	text := mgmtMetricsText(t, off)
	for _, family := range []string{"nexora_mgmt_ai_agent_runs_total", "nexora_mgmt_ai_budget_used_ratio", "nexora_mgmt_ai_open_proposals", "nexora_mgmt_ai_requests_total"} {
		if strings.Contains(text, family) {
			t.Fatalf("unconfigured instance exposes %s", family)
		}
	}
	if !strings.Contains(text, "nexora_mgmt_ai_enabled 0") {
		t.Fatal("unconfigured instance does not report nexora_mgmt_ai_enabled 0")
	}
}

// TestApplyAiProposalsReplaysThroughAPI catches an apply that bypasses RBAC, the audit log or the revision
// check, applies a proposal twice, or changes configuration that never reaches the engines.
func TestApplyAiProposalsReplaysThroughAPI(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartOpenAIFixture()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: harness.AIEnv(fx)})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	login := func(name, role string) *harness.API {
		admin.Must(http.MethodPost, "/users", map[string]any{"username": name, "email": name + "@example.test", "password": name + "-password-e2e", "role": role}, nil, http.StatusCreated)
		a := env.NewAPI(mg.BaseURL)
		a.Must(http.MethodPost, "/auth/login", map[string]string{"username": name, "password": name + "-password-e2e"}, nil, http.StatusOK)
		return a
	}
	viewer, operator := login("vera", "viewer"), login("otto", "operator")
	env.StartManagedEngine("ai-apply-1", []string{mg.GRPCURL}, admin.CreateJoinToken())

	var g struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	operator.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}}, &g, http.StatusCreated)
	waitLatestApplied(t, admin, "ai-apply-1")

	insert := func(description string, revision int64) string {
		id := uuid.NewString()
		actions, _ := json.Marshal([]map[string]any{{"operation_id": "updatePolicyGroup", "path_params": map[string]string{"id": g.ID},
			"body": map[string]any{"name": "guests", "cidrs": []string{"10.9.0.0/16"}, "description": description, "revision": revision}}})
		harness.PGExec(t, pg.URL, `insert into ai_proposals(id, source, fingerprint, title, description, priority, actions)
			values ($1, 'filter_recommendations', $2, 't', 'd', 'low', $3::jsonb)`, id, "filter_recommendations:"+id, string(actions))
		return id
	}
	type applyResult struct {
		Results []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Actions []struct {
				HTTPStatus int    `json:"http_status"`
				Code       string `json:"code"`
			} `json:"actions"`
		} `json:"results"`
	}
	apply := func(id string) applyResult {
		var res applyResult
		operator.Must(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": []string{id}}, &res, http.StatusOK)
		if len(res.Results) != 1 {
			t.Fatalf("apply %s = %+v", id, res)
		}
		return res
	}

	applyID := insert("suggested by AI", g.Revision)
	if status, _ := viewer.Do(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": []string{applyID}}, nil); status != http.StatusForbidden {
		t.Fatalf("viewer apply = %d", status)
	}
	before := admin.LatestVersion()
	if r := apply(applyID).Results[0]; r.Status != "applied" || len(r.Actions) != 1 || r.Actions[0].HTTPStatus != http.StatusOK {
		t.Fatalf("operator apply = %+v", r)
	}
	var changed struct {
		Description string `json:"description"`
		Revision    int64  `json:"revision"`
	}
	operator.Must(http.MethodGet, "/policy-groups/"+g.ID, nil, &changed, http.StatusOK)
	if changed.Description != "suggested by AI" || changed.Revision != g.Revision+1 {
		t.Fatalf("group after apply = %+v", changed)
	}
	after := admin.LatestVersion()
	if after <= before {
		t.Fatalf("apply published no config version (%d -> %d)", before, after)
	}
	admin.WaitEngine("ai-apply-1", 30*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion >= after })
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where action = 'updatePolicyGroup' and actor_name = 'otto' and target_id = $1`, g.ID); n != 1 {
		t.Fatalf("updatePolicyGroup audit rows by otto = %d", n)
	}
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where action = 'applyAiProposals' and actor_name = 'otto' and target_id = $1
		and diff->'after'->>'proposal_id' = $1 and diff->'after'->'actions'->0->>'http_status' = '200'`, applyID); n != 1 {
		t.Fatalf("applyAiProposals audit rows by otto = %d", n)
	}
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where actor_name = 'vera'`); n != 0 {
		t.Fatalf("the viewer's refused apply wrote %d audit rows", n)
	}

	if r := apply(applyID).Results[0]; r.Status != "proposal_not_open" {
		t.Fatalf("second apply = %+v", r)
	}

	staleID := insert("stale suggestion", g.Revision) // the group is at g.Revision+1 now
	if r := apply(staleID).Results[0]; r.Status != "stale" || len(r.Actions) != 1 || r.Actions[0].HTTPStatus != http.StatusConflict {
		t.Fatalf("stale apply = %+v", r)
	}
	if n := pgInt(t, pg.URL, `select count(*) from ai_proposals where id = $1 and status = 'stale'`, staleID); n != 1 {
		t.Fatal("stale proposal not recorded as stale")
	}

	dismissID := insert("dismiss me", changed.Revision)
	var dismissed struct {
		Results []struct {
			Status string `json:"status"`
		} `json:"results"`
	}
	operator.Must(http.MethodPost, "/ai/proposals/dismiss", map[string]any{"ids": []string{dismissID}, "reason": "not now"}, &dismissed, http.StatusOK)
	if len(dismissed.Results) != 1 || dismissed.Results[0].Status != "dismissed" {
		t.Fatalf("dismiss = %+v", dismissed)
	}
	if n := pgInt(t, pg.URL, `select count(*) from ai_proposals where id = $1 and status = 'dismissed' and dismiss_reason = 'not now' and reviewed_by = 'otto'`, dismissID); n != 1 {
		t.Fatal("dismissal not recorded with its reason")
	}
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where action = 'dismissAiProposals' and actor_name = 'otto' and target_id = $1
		and diff->'after'->>'reason' = 'not now'`, dismissID); n != 1 {
		t.Fatalf("dismissAiProposals audit rows = %d", n)
	}
}
