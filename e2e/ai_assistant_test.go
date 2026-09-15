package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

// assistantPlan is a scripted config_assistant answer creating the policy group name.
func assistantPlan(name string) map[string]any {
	return map[string]any{"reply": "This creates the policy group " + name + " blocking adult content.", "summary": "Create " + name,
		"actions": []any{map[string]any{"operation_id": "createPolicyGroup", "path_params": map[string]string{},
			"body": map[string]any{"name": name, "cidrs": []string{"10.9.0.0/24"}, "category_keys": []string{"adult"}}}}}
}

// TestAIConfigAssistant catches an assistant plan that changes configuration without an apply, a second
// plan that leaves the first open, an apply that is not audited as the operator, and a session that
// leaks to another user or is open to a viewer.
func TestAIConfigAssistant(t *testing.T) {
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
	viewer, otto, olga := login("vera", "viewer"), login("otto", "operator"), login("olga", "operator")
	fx.ScriptJSON(t, "config_assistant", assistantPlan("guest-wifi"), assistantPlan("guest-wifi-2"))

	var session struct {
		ID string `json:"id"`
	}
	otto.Must(http.MethodPost, "/ai/assistant/sessions", nil, &session, http.StatusCreated)
	turn := func(content string) string {
		t.Helper()
		var task struct {
			ID string `json:"id"`
		}
		otto.Must(http.MethodPost, "/ai/assistant/sessions/"+session.ID+"/messages", map[string]string{"content": content}, &task, http.StatusAccepted)
		result, _ := waitAITask(otto, task.ID, 60*time.Second)["result"].(map[string]any)
		id, _ := result["proposal_id"].(string)
		if id == "" {
			t.Fatalf("assistant turn %q made no proposal: %v", content, result)
		}
		return id
	}
	type sessionView struct {
		Title    string `json:"title"`
		Messages []struct {
			Role       string  `json:"role"`
			ProposalID *string `json:"proposal_id"`
		} `json:"messages"`
		Proposal *struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Actions []struct {
				OperationID string `json:"operation_id"`
			} `json:"actions"`
		} `json:"proposal"`
		Task *struct {
			Status string `json:"status"`
		} `json:"task"`
	}
	groupNames := func() map[string]bool {
		var groups []struct {
			Name string `json:"name"`
		}
		admin.Must(http.MethodGet, "/policy-groups", nil, &groups, http.StatusOK)
		names := map[string]bool{}
		for _, g := range groups {
			names[g.Name] = true
		}
		return names
	}

	first := turn("Block adult content for 10.9.0.0/24 as guest-wifi")
	var view sessionView
	otto.Must(http.MethodGet, "/ai/assistant/sessions/"+session.ID, nil, &view, http.StatusOK)
	if view.Title != "Block adult content for 10.9.0.0/24 as guest-wifi" || len(view.Messages) != 2 || view.Proposal == nil ||
		view.Proposal.ID != first || view.Proposal.Status != "open" || len(view.Proposal.Actions) != 1 ||
		view.Proposal.Actions[0].OperationID != "createPolicyGroup" || view.Task == nil || view.Task.Status != "succeeded" {
		t.Fatalf("session after the first turn = %+v", view)
	}

	second := turn("Call it guest-wifi-2 instead")
	if second == first {
		t.Fatal("the second plan reused the first proposal")
	}
	if n := pgInt(t, pg.URL, `select count(*) from ai_proposals where id = $1 and status = 'superseded'`, first); n != 1 {
		t.Fatal("the first plan is not superseded by the second")
	}
	otto.Must(http.MethodGet, "/ai/assistant/sessions/"+session.ID, nil, &view, http.StatusOK)
	if len(view.Messages) != 4 || view.Proposal == nil || view.Proposal.ID != second {
		t.Fatalf("session after the second turn = %+v", view)
	}
	if names := groupNames(); names["guest-wifi"] || names["guest-wifi-2"] {
		t.Fatalf("a plan changed configuration without apply: %v", names)
	}

	var applied struct {
		Results []struct {
			Status string `json:"status"`
		} `json:"results"`
	}
	otto.Must(http.MethodPost, "/ai/proposals/apply", map[string]any{"ids": []string{second}, "acknowledge_license": true}, &applied, http.StatusOK)
	if len(applied.Results) != 1 || applied.Results[0].Status != "applied" {
		t.Fatalf("apply = %+v", applied)
	}
	if names := groupNames(); !names["guest-wifi-2"] || names["guest-wifi"] {
		t.Fatalf("policy groups after apply = %v", names)
	}
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where action = 'createPolicyGroup' and actor_name = 'otto'`); n != 1 {
		t.Fatalf("createPolicyGroup audit rows by otto = %d", n)
	}

	if status, _ := olga.Do(http.MethodGet, "/ai/assistant/sessions/"+session.ID, nil, nil); status != http.StatusNotFound {
		t.Fatalf("another operator reads the session: %d", status)
	}
	if status, _ := olga.Do(http.MethodPost, "/ai/assistant/sessions/"+session.ID+"/messages", map[string]string{"content": "hi"}, nil); status != http.StatusNotFound {
		t.Fatalf("another operator posts to the session: %d", status)
	}
	if status, _ := viewer.Do(http.MethodPost, "/ai/assistant/sessions", nil, nil); status != http.StatusForbidden {
		t.Fatalf("viewer creates a session: %d", status)
	}
}
