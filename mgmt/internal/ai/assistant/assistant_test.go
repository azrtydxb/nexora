package assistant_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/assistant"
	"github.com/piwi3910/nexora/mgmt/internal/ai/proposal"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

func lastText(c provider.Call) string {
	m := c.Messages[len(c.Messages)-1]
	for _, p := range m.Content {
		if t, ok := p.(provider.TextPart); ok {
			return t.Text
		}
	}
	return ""
}

func createPlan(body string) map[string]any {
	return map[string]any{"reply": "I will create the group.", "summary": "Create guest-wifi",
		"actions": []any{map[string]any{"operation_id": "createPolicyGroup", "path_params": map[string]string{}, "body": json.RawMessage(body)}}}
}

// turn adds a user message to a new session of p and runs the assistant task for it.
func turn(t *testing.T, ctx context.Context, st *store.Store, run ai.TaskFunc, p auth.Principal, content string) (assistant.Session, map[string]any) {
	t.Helper()
	s, err := assistant.CreateSession(ctx, st, p)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := assistant.AddUserMessage(ctx, st, s.ID, content)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(assistant.TaskInput{SessionID: s.ID, MessageID: msg.ID})
	out, err := run(ctx, ai.Task{ID: uuid.New(), Kind: ai.TaskAssistantMessage, Input: input})
	if err != nil {
		t.Fatalf("assistant task: %v", err)
	}
	raw, _ := json.Marshal(out)
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return s, result
}

// TestAssistantPlanValidation catches a plan stored with an unknown category, an invalid CIDR or an
// operation outside the allowlist, validation errors that never reach the model, a clarifying reply that
// is refused or turned into a proposal, and a session readable by another user.
func TestAssistantPlanValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st := storetest.New(t)
	if _, err := st.Pool.Exec(ctx, `insert into filter_categories(key) values ('adult')`); err != nil {
		t.Fatal(err)
	}
	valid := `{"name":"guest-wifi","cidrs":["10.9.0.0/24"],"category_keys":["adult"]}`
	m := aifake.Model(
		aifake.JSON(createPlan(`{"name":"guest-wifi","cidrs":["10.9.0.0/24"],"category_keys":["gambling-x"]}`)),
		aifake.JSON(createPlan(`{"name":"guest-wifi","cidrs":["10.9.0.0/33"],"category_keys":["adult"]}`)),
		aifake.JSON(map[string]any{"reply": "Deleting.", "summary": "Delete a zone",
			"actions": []any{map[string]any{"operation_id": "deleteZone", "path_params": map[string]string{"id": uuid.NewString()}}}}),
		aifake.JSON(createPlan(valid)),
		aifake.JSON(map[string]any{"reply": "Which network is the guest WiFi?", "summary": "", "actions": []any{}}),
	)
	svc := aifake.Service(t, st, m, func(c *config.AIConfig) { c.ValidationAttempts = 4 })
	run := assistant.NewTask(st, svc, &proposal.Validator{Store: st}, time.Now)
	otto := auth.Principal{UserID: uuid.NewString(), Username: "otto", Role: auth.RoleOperator, Kind: "session"}

	s, result := turn(t, ctx, st, run, otto, "Block adult content for 10.9.0.0/24 as guest-wifi")
	pid, _ := result["proposal_id"].(string)
	if pid == "" {
		t.Fatalf("task result has no proposal_id: %v", result)
	}
	p, err := proposal.Get(ctx, st.Pool, uuid.MustParse(pid))
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(p.Actions[0].Body, &got)
	_ = json.Unmarshal([]byte(valid), &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if p.Source != "config_assistant" || p.SessionID == nil || *p.SessionID != s.ID || len(p.Actions) != 1 ||
		p.Actions[0].OperationID != "createPolicyGroup" || string(gotJSON) != string(wantJSON) {
		t.Fatalf("proposal = %+v body %s", p, p.Actions[0].Body)
	}
	calls := m.RecordedCalls()
	if len(calls) != 4 {
		t.Fatalf("model calls = %d, want 4", len(calls))
	}
	for i, want := range []string{"gambling-x", "10.9.0.0/33", "operation deleteZone is not allowed"} {
		if text := lastText(calls[i+1]); !strings.Contains(text, want) {
			t.Fatalf("model call %d does not feed back %q: %q", i+2, want, text)
		}
	}

	session, msgs, err := assistant.GetSession(ctx, st, s.ID, otto)
	if err != nil || session.Title != "Block adult content for 10.9.0.0/24 as guest-wifi" || len(msgs) != 2 ||
		msgs[1].Role != "assistant" || msgs[1].ProposalID == nil || msgs[1].ProposalID.String() != pid {
		t.Fatalf("session = %+v messages %+v err %v", session, msgs, err)
	}
	other := auth.Principal{UserID: uuid.NewString(), Username: "olga", Role: auth.RoleOperator, Kind: "session"}
	if _, _, err := assistant.GetSession(ctx, st, s.ID, other); err != store.ErrNotFound {
		t.Fatalf("another operator reads the session: %v", err)
	}

	s2, result := turn(t, ctx, st, run, otto, "Set up guest WiFi filtering")
	if result["proposal_id"] != nil {
		t.Fatalf("clarifying reply created a proposal: %v", result)
	}
	_, msgs, err = assistant.GetSession(ctx, st, s2.ID, otto)
	if err != nil || len(msgs) != 2 || msgs[1].Content != "Which network is the guest WiFi?" || msgs[1].ProposalID != nil {
		t.Fatalf("clarifying session messages = %+v err %v", msgs, err)
	}
	var n int
	_ = st.Pool.QueryRow(ctx, `select count(*) from ai_proposals where session_id = $1`, s2.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("clarifying session has %d proposals", n)
	}
}
