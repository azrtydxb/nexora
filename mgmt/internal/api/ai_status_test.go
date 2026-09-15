package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// aiUser is a user with a session cookie and the principal the authorization middleware derives.
type aiUser struct {
	cookie *http.Cookie
	p      auth.Principal
}

func newAIUser(t *testing.T, ctx context.Context, st *store.Store, svc *auth.Service, username string, role auth.Role) aiUser {
	t.Helper()
	var u auth.User
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = auth.CreateUser(ctx, tx, username, username+"@x", "long-enough-password-1", role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := svc.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return aiUser{cookie: svc.SessionCookie(token), p: auth.Principal{UserID: u.ID, Username: username, Role: role, Kind: "session"}}
}

// aiDo serves one API request as u and returns the status and body.
func aiDo(t *testing.T, r http.Handler, u aiUser, method, path string) (int, string) {
	t.Helper()
	var body *bytes.Reader
	if method != http.MethodGet {
		body = bytes.NewReader([]byte("{}"))
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, body)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(u.cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestAiStatusAndTasks catches a status that leaks the API key or misreports the configuration, an AI
// operation that works while AI is off, a Run now that skips RBAC or runs an unknown or disabled agent,
// and a task readable by anyone but its requester or an admin.
func TestAiStatusAndTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := storetest.New(t)
	svc := auth.NewService(st, false)
	vic := newAIUser(t, ctx, st, svc, "vic", auth.RoleViewer)
	val := newAIUser(t, ctx, st, svc, "val", auth.RoleViewer)
	olga := newAIUser(t, ctx, st, svc, "olga", auth.RoleOperator)
	ada := newAIUser(t, ctx, st, svc, "ada", auth.RoleAdmin)

	// AI off: status explains why, every other AI operation is 503 ai_disabled.
	_, off := newHandlers(Deps{Store: st, Auth: svc, AIDisabledReason: "not_configured"})
	code, body := aiDo(t, off, vic, http.MethodGet, "/ai/status")
	var status AiStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil || code != http.StatusOK || status.Enabled || status.Reason != AiStatusReasonNotConfigured {
		t.Fatalf("status while off = %d %s", code, body)
	}
	if code, body = aiDo(t, off, vic, http.MethodGet, "/ai/proposals"); code != http.StatusServiceUnavailable || !strings.Contains(body, `"ai_disabled"`) {
		t.Fatalf("proposals while off = %d %s", code, body)
	}

	// AI on.
	svcAI := aifake.Service(t, st, aifake.Model(), func(c *config.AIConfig) {
		c.Agents["rpz_suggestions"] = config.AgentConfig{Enabled: false}
	})
	tasks := ai.NewTasks(ctx, st, "test")
	tasks.Register(ai.TaskQueryLogSearch, func(context.Context, ai.Task) (any, error) {
		return map[string]string{"summary": "s"}, nil
	})
	rt := &AIRuntime{Service: svcAI, Tasks: tasks, Config: svcAI.Config(), InstanceStart: time.Now(),
		TaskKinds: map[ai.TaskKind]bool{ai.TaskQueryLogSearch: true},
		Agents:    map[string]bool{"capacity_forecast": true, "rpz_suggestions": true}}
	h, on := newHandlers(Deps{Store: st, Auth: svc, AI: rt})

	code, body = aiDo(t, on, vic, http.MethodGet, "/ai/status")
	status = AiStatus{}
	if err := json.Unmarshal([]byte(body), &status); err != nil || code != http.StatusOK {
		t.Fatalf("status = %d %s", code, body)
	}
	if !status.Enabled || status.Reason != AiStatusReasonEmpty || status.Model != "fake-qwen" || status.EndpointHost != "127.0.0.1" ||
		status.StructuredOutput != "json_schema" || !status.Features.QuerylogSearch || status.Features.ThreatCheck ||
		status.Budget.LimitTokens != 1000000 || status.Mcp.Enabled || !status.Mcp.ReadOnly {
		t.Fatalf("status = %s", body)
	}
	if strings.Contains(body, "test-key-not-secret") || strings.Contains(body, "api_key") {
		t.Fatalf("status leaks the API key: %s", body)
	}
	enabled := map[string]bool{}
	for _, a := range status.Agents {
		enabled[string(a.Name)] = a.Enabled
	}
	if len(status.Agents) != len(ai.AgentNames) || !enabled["capacity_forecast"] || enabled["rpz_suggestions"] || enabled["dashboard_insights"] {
		t.Fatalf("agents = %+v (enabled means registered and configured on)", status.Agents)
	}

	if code, body = aiDo(t, on, vic, http.MethodPost, "/ai/agents/capacity_forecast/run"); code != http.StatusForbidden {
		t.Fatalf("viewer run = %d %s", code, body)
	}
	if code, body = aiDo(t, on, olga, http.MethodPost, "/ai/agents/capacity_forecast/run"); code != http.StatusAccepted {
		t.Fatalf("operator run = %d %s", code, body)
	}
	var requested int
	if err := st.Pool.QueryRow(ctx, "select count(*) from ai_agent_requests where agent = 'capacity_forecast'").Scan(&requested); err != nil || requested != 1 {
		t.Fatalf("ai_agent_requests rows = %d %v", requested, err)
	}
	if code, body = aiDo(t, on, olga, http.MethodPost, "/ai/agents/nope/run"); code != http.StatusBadRequest {
		t.Fatalf("unknown agent name = %d %s", code, body)
	}
	for _, agent := range []string{"rpz_suggestions", "dashboard_insights"} { // disabled in config; not registered
		if code, body = aiDo(t, on, olga, http.MethodPost, "/ai/agents/"+agent+"/run"); code != http.StatusNotFound || !strings.Contains(body, `"unknown_agent"`) {
			t.Fatalf("run %s = %d %s", agent, code, body)
		}
	}

	pctx := context.WithValue(ctx, principalKey{}, vic.p)
	if _, err := h.startTask(pctx, ai.TaskThreatCheck, map[string]any{}); !isAPIError(err, http.StatusServiceUnavailable, "feature_disabled") {
		t.Fatalf("unregistered task kind: %v", err)
	}
	task, err := h.startTask(pctx, ai.TaskQueryLogSearch, map[string]string{"query": "q"})
	if err != nil || task.Status != AiTaskStatusQueued || task.Kind != QuerylogSearch {
		t.Fatalf("startTask = %+v %v", task, err)
	}
	path := "/ai/tasks/" + task.Id.String()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body = aiDo(t, on, vic, http.MethodGet, path)
		var got AiTask
		_ = json.Unmarshal([]byte(body), &got)
		if code == http.StatusOK && got.Status == AiTaskStatusSucceeded {
			if got.Result == nil || (*got.Result)["summary"] != "s" || got.FinishedAt == nil {
				t.Fatalf("succeeded task = %s", body)
			}
			break
		}
		if code != http.StatusOK || time.Now().After(deadline) {
			t.Fatalf("requester poll = %d %s", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, body = aiDo(t, on, ada, http.MethodGet, path); code != http.StatusOK {
		t.Fatalf("admin read = %d %s", code, body)
	}
	if code, body = aiDo(t, on, val, http.MethodGet, path); code != http.StatusNotFound {
		t.Fatalf("other viewer read = %d %s", code, body)
	}
}

func isAPIError(err error, status int, code string) bool {
	var aerr apiError
	return errors.As(err, &aerr) && aerr.status == status && aerr.code == code
}
