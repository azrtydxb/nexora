package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

func TestM11OperationsHaveRoles(t *testing.T) {
	want := map[string]auth.Role{
		"getAiStatus": auth.RoleViewer, "runAiAgent": auth.RoleOperator, "getAiTask": auth.RoleViewer,
		"startAiQueryLogSearch": auth.RoleViewer, "listAiFindings": auth.RoleViewer, "updateAiFinding": auth.RoleOperator,
		"getAiInsights": auth.RoleViewer, "listAiProposals": auth.RoleViewer, "getAiProposal": auth.RoleViewer,
		"applyAiProposals": auth.RoleOperator, "dismissAiProposals": auth.RoleOperator,
		"createAiAssistantSession": auth.RoleOperator, "getAiAssistantSession": auth.RoleOperator,
		"postAiAssistantMessage": auth.RoleOperator, "listAiForecasts": auth.RoleViewer,
		"getAiRolloutRisk": auth.RoleViewer, "startAiThreatCheck": auth.RoleViewer,
		"getAiFilterListClassification": auth.RoleViewer,
	}
	for op, role := range want {
		if got := auth.Permissions[op]; got != role {
			t.Errorf("%s: role %q, want %q", op, got, role)
		}
	}
}

// While AI is off, AI operations answer 503 ai_disabled naming the reason; the model errors map to 429.
func TestM11AIGateAndErrorCodes(t *testing.T) {
	h := &handlers{d: Deps{AIDisabledReason: "not_configured"}}
	_, err := h.ListAiProposals(context.Background(), ListAiProposalsRequestObject{})
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{err, http.StatusServiceUnavailable, "ai_disabled"},
		{fmt.Errorf("generate: %w", ai.ErrBusy), http.StatusTooManyRequests, "ai_busy"},
		{fmt.Errorf("generate: %w", ai.ErrBudgetExhausted), http.StatusTooManyRequests, "ai_budget_exhausted"},
	} {
		rec := httptest.NewRecorder()
		mapError(rec, httptest.NewRequest("GET", "/api/v1/ai/proposals", nil), c.err)
		var body Error
		if jerr := json.Unmarshal(rec.Body.Bytes(), &body); jerr != nil {
			t.Fatal(jerr)
		}
		if rec.Code != c.status || body.Code != c.code {
			t.Errorf("%v -> %d %q, want %d %q", c.err, rec.Code, body.Code, c.status, c.code)
		}
		if c.code == "ai_disabled" && body.Message != "AI is not configured: not_configured" {
			t.Errorf("ai_disabled message %q", body.Message)
		}
	}
}
