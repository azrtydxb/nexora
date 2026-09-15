package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadAIConfig(t *testing.T) {
	base := map[string]string{"NEXORA_DATABASE_URL": "postgres://x", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k"}
	c, err := Load(env(base))
	if err != nil || c.AI.DisabledReason() != "not_configured" {
		t.Fatalf("empty: %v %q", err, c.AI.DisabledReason())
	}
	base["NEXORA_AI_BASE_URL"] = "http://192.168.10.125:4000/v1"
	if c, _ = Load(env(base)); c.AI.DisabledReason() != "incomplete_configuration" {
		t.Fatalf("base url only: %q", c.AI.DisabledReason())
	}
	delete(base, "NEXORA_AI_BASE_URL")
	base["NEXORA_AI_MODEL"] = "qwen3-6-35b-a3b"
	if c, _ = Load(env(base)); c.AI.DisabledReason() != "incomplete_configuration" {
		t.Fatalf("model only: %q", c.AI.DisabledReason())
	}
	base["NEXORA_AI_BASE_URL"] = "http://192.168.10.125:4000/v1"
	c, err = Load(env(base))
	if err != nil || c.AI.DisabledReason() != "" {
		t.Fatalf("configured: %v %q", err, c.AI.DisabledReason())
	}
	a := c.AI
	if a.StructuredOutput != "json_schema" || a.MaxTokens != 16384 || a.Temperature != 0.2 || a.Timeout != 180*time.Second ||
		a.ValidationAttempts != 3 || a.MaxConcurrency != 2 || a.RequestsPerMinute != 20 || a.DailyTokenBudget != 2_000_000 ||
		a.BackgroundBudgetPercent != 80 || a.LLMMinInterval != 5*time.Minute || a.AgentStartDelay != 2*time.Minute ||
		!a.ConfigAssistantEnabled || a.MCPEnabled || !a.MCPReadOnly || a.AllowPublicEndpoint {
		t.Fatalf("defaults: %+v", a)
	}
	for name, iv := range map[string]time.Duration{"querylog_anomalies": 30 * time.Second, "dashboard_insights": 30 * time.Second,
		"filter_recommendations": 6 * time.Hour, "upstream_prediction": 6 * time.Hour, "rollout_risk": 15 * time.Second,
		"threat_classification": 6 * time.Hour, "capacity_forecast": 24 * time.Hour, "rpz_suggestions": 24 * time.Hour} {
		if got := a.Agents[name]; !got.Enabled || got.Interval != iv {
			t.Errorf("%s: %+v, want enabled every %v", name, got, iv)
		}
	}
	if len(a.Agents) != 8 {
		t.Errorf("agents: %d, want 8", len(a.Agents))
	}
	base["NEXORA_AI_CAPACITY_FORECAST_INTERVAL"] = "0"
	base["NEXORA_AI_DASHBOARD_ENABLED"] = "false"
	if c, _ = Load(env(base)); c.AI.Agents["capacity_forecast"].Enabled || c.AI.Agents["dashboard_insights"].Enabled {
		t.Fatal("interval 0 and _ENABLED=false must disable the agent")
	}
	for key, bad := range map[string]string{"NEXORA_AI_TIMEOUT": "soon", "NEXORA_AI_MAX_TOKENS": "many", "NEXORA_AI_TEMPERATURE": "warm",
		"NEXORA_AI_DAILY_TOKEN_BUDGET": "0", "NEXORA_AI_BACKGROUND_BUDGET_PERCENT": "101", "NEXORA_AI_STRUCTURED_OUTPUT": "xml",
		"NEXORA_AI_RPZ_SUGGESTIONS_INTERVAL": "-1h", "NEXORA_MCP_READ_ONLY": "perhaps", "NEXORA_AI_BASE_URL": "ftp://fastllm/v1"} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		m[key] = bad
		if _, err = Load(env(m)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%q: %v, want an error naming the variable", key, bad, err)
		}
	}
}
