package deploytest

import (
	"os"
	"strings"
	"testing"
)

// aiDocStrings are every M11 environment variable, metric, error code and operator command that
// docs/operations.md must document, so an operator never has to read the code to run AI.
var aiDocStrings = []string{
	// Connection and privacy.
	"NEXORA_AI_BASE_URL", "NEXORA_AI_MODEL", "NEXORA_AI_API_KEY", "NEXORA_AI_ALLOW_PUBLIC_ENDPOINT",
	// Model calls.
	"NEXORA_AI_STRUCTURED_OUTPUT", "NEXORA_AI_MAX_TOKENS", "NEXORA_AI_TEMPERATURE", "NEXORA_AI_TIMEOUT",
	"NEXORA_AI_VALIDATION_ATTEMPTS",
	// Limits and budget.
	"NEXORA_AI_MAX_CONCURRENCY", "NEXORA_AI_REQUESTS_PER_MINUTE", "NEXORA_AI_DAILY_TOKEN_BUDGET",
	"NEXORA_AI_BACKGROUND_BUDGET_PERCENT", "NEXORA_AI_LLM_MIN_INTERVAL", "NEXORA_AI_AGENT_START_DELAY",
	// Agents.
	"NEXORA_AI_QUERYLOG_INTERVAL", "NEXORA_AI_DASHBOARD_INTERVAL",
	"NEXORA_AI_FILTER_RECOMMENDATIONS_INTERVAL", "NEXORA_AI_UPSTREAM_PREDICTION_INTERVAL",
	"NEXORA_AI_ROLLOUT_RISK_ENABLED", "NEXORA_AI_THREAT_CLASSIFICATION_INTERVAL",
	"NEXORA_AI_CAPACITY_FORECAST_INTERVAL", "NEXORA_AI_RPZ_SUGGESTIONS_INTERVAL",
	"NEXORA_AI_CONFIG_ASSISTANT_ENABLED",
	// MCP.
	"NEXORA_MCP_ENABLED", "NEXORA_MCP_READ_ONLY",
	// Metrics.
	"nexora_mgmt_ai_enabled", "nexora_mgmt_ai_requests_total", "nexora_mgmt_ai_request_duration_seconds",
	"nexora_mgmt_ai_tokens_total", "nexora_mgmt_ai_validation_retries_total",
	"nexora_mgmt_ai_inflight_requests", "nexora_mgmt_ai_queue_wait_seconds",
	"nexora_mgmt_ai_budget_used_ratio", "nexora_mgmt_ai_agent_runs_total",
	"nexora_mgmt_ai_agent_last_success_timestamp_seconds", "nexora_mgmt_ai_open_findings",
	"nexora_mgmt_ai_open_proposals", "nexora_mgmt_mcp_requests_total", "nexora_mgmt_mcp_tool_calls_total",
	// Error codes.
	"ai_disabled", "ai_busy", "ai_budget_exhausted", "feature_disabled", "proposal_not_open",
	"unknown_agent", "endpoint_not_private",
	// Operator handles.
	"nexora-mgmt mcp-stdio", "ai-suggested.rpz",
}

// TestOperationsDocumentsAI checks that the operations guide carries the AI section the in-app help
// topic web/src/help/topics/ai.md points at, and every name an operator needs from it.
func TestOperationsDocumentsAI(t *testing.T) {
	b, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	ops := string(b)
	if !strings.Contains(ops, "\n## AI\n") {
		t.Fatal(`docs/operations.md lacks heading "## AI"`)
	}
	for _, want := range aiDocStrings {
		if !strings.Contains(ops, want) {
			t.Errorf("docs/operations.md does not document %q", want)
		}
	}
}
