package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AgentConfig is one background AI agent's schedule; Enabled is false when its _ENABLED is false
// or its interval is 0.
type AgentConfig struct {
	Enabled  bool
	Interval time.Duration
}

// AIConfig configures the optional AI layer (M11). The API key is never logged or reported.
type AIConfig struct {
	BaseURL, Model, APIKey  string
	AllowPublicEndpoint     bool
	StructuredOutput        string // "json_schema" | "prompt"
	MaxTokens               int
	Temperature             float64
	Timeout                 time.Duration
	ValidationAttempts      int
	MaxConcurrency          int
	RequestsPerMinute       int
	DailyTokenBudget        int64
	BackgroundBudgetPercent int
	LLMMinInterval          time.Duration
	AgentStartDelay         time.Duration
	Agents                  map[string]AgentConfig // keys: the eight agent names
	ConfigAssistantEnabled  bool
	MCPEnabled, MCPReadOnly bool
}

// DisabledReason is "" when AI is configured, "not_configured" without base URL and model, and
// "incomplete_configuration" when only one of them is set.
func (c AIConfig) DisabledReason() string {
	switch {
	case c.BaseURL != "" && c.Model != "":
		return ""
	case c.BaseURL == "" && c.Model == "":
		return "not_configured"
	default:
		return "incomplete_configuration"
	}
}

// aiAgents maps each agent name to its environment prefix and default interval (0: no interval
// variable, the rollout risk agent runs every 15 s).
var aiAgents = []struct {
	name, prefix string
	interval     time.Duration
}{
	{"querylog_anomalies", "NEXORA_AI_QUERYLOG", 30 * time.Second},
	{"dashboard_insights", "NEXORA_AI_DASHBOARD", 30 * time.Second},
	{"filter_recommendations", "NEXORA_AI_FILTER_RECOMMENDATIONS", 6 * time.Hour},
	{"upstream_prediction", "NEXORA_AI_UPSTREAM_PREDICTION", 6 * time.Hour},
	{"rollout_risk", "NEXORA_AI_ROLLOUT_RISK", 0},
	{"threat_classification", "NEXORA_AI_THREAT_CLASSIFICATION", 6 * time.Hour},
	{"capacity_forecast", "NEXORA_AI_CAPACITY_FORECAST", 24 * time.Hour},
	{"rpz_suggestions", "NEXORA_AI_RPZ_SUGGESTIONS", 24 * time.Hour},
}

const rolloutRiskInterval = 15 * time.Second

// loadAI reads the NEXORA_AI_* and NEXORA_MCP_* variables. Every value is validated even when AI is
// off, so a typo surfaces at start rather than when AI is switched on.
func loadAI(getenv func(string) string) (AIConfig, error) {
	var firstErr error
	fail := func(key, msg string) {
		if firstErr == nil {
			firstErr = fmt.Errorf("%s %s", key, msg)
		}
	}
	get := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	boolean := func(key string, def bool) bool {
		v, err := strconv.ParseBool(get(key, strconv.FormatBool(def)))
		if err != nil {
			fail(key, "must be true or false")
		}
		return v
	}
	integer := func(key string, def, lo, hi int64) int64 {
		v, err := strconv.ParseInt(get(key, strconv.FormatInt(def, 10)), 10, 64)
		if err != nil || v < lo || v > hi {
			fail(key, fmt.Sprintf("must be an integer between %d and %d", lo, hi))
		}
		return v
	}
	duration := func(key string, def time.Duration, lo time.Duration) time.Duration {
		v, err := time.ParseDuration(get(key, def.String()))
		if err != nil || v < lo {
			fail(key, fmt.Sprintf("must be a duration of at least %s", lo))
		}
		return v
	}
	c := AIConfig{
		BaseURL:                 getenv("NEXORA_AI_BASE_URL"),
		Model:                   getenv("NEXORA_AI_MODEL"),
		APIKey:                  getenv("NEXORA_AI_API_KEY"),
		AllowPublicEndpoint:     boolean("NEXORA_AI_ALLOW_PUBLIC_ENDPOINT", false),
		StructuredOutput:        get("NEXORA_AI_STRUCTURED_OUTPUT", "json_schema"),
		MaxTokens:               int(integer("NEXORA_AI_MAX_TOKENS", 16384, 1, 32768)),
		Timeout:                 duration("NEXORA_AI_TIMEOUT", 180*time.Second, time.Second),
		ValidationAttempts:      int(integer("NEXORA_AI_VALIDATION_ATTEMPTS", 3, 1, 10)),
		MaxConcurrency:          int(integer("NEXORA_AI_MAX_CONCURRENCY", 2, 1, 64)),
		RequestsPerMinute:       int(integer("NEXORA_AI_REQUESTS_PER_MINUTE", 20, 1, 6000)),
		DailyTokenBudget:        integer("NEXORA_AI_DAILY_TOKEN_BUDGET", 2_000_000, 1, 1<<50),
		BackgroundBudgetPercent: int(integer("NEXORA_AI_BACKGROUND_BUDGET_PERCENT", 80, 0, 100)),
		LLMMinInterval:          duration("NEXORA_AI_LLM_MIN_INTERVAL", 5*time.Minute, 0),
		AgentStartDelay:         duration("NEXORA_AI_AGENT_START_DELAY", 2*time.Minute, 0),
		ConfigAssistantEnabled:  boolean("NEXORA_AI_CONFIG_ASSISTANT_ENABLED", true),
		MCPEnabled:              boolean("NEXORA_MCP_ENABLED", false),
		MCPReadOnly:             boolean("NEXORA_MCP_READ_ONLY", true),
		Agents:                  make(map[string]AgentConfig, len(aiAgents)),
	}
	temp, err := strconv.ParseFloat(get("NEXORA_AI_TEMPERATURE", "0.2"), 64)
	if err != nil || temp < 0 || temp > 2 {
		fail("NEXORA_AI_TEMPERATURE", "must be a number between 0 and 2")
	}
	c.Temperature = temp
	if c.StructuredOutput != "json_schema" && c.StructuredOutput != "prompt" {
		fail("NEXORA_AI_STRUCTURED_OUTPUT", "must be json_schema or prompt")
	}
	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		fail("NEXORA_AI_BASE_URL", "must be an http(s) URL")
	}
	for _, a := range aiAgents {
		ac := AgentConfig{Enabled: boolean(a.prefix+"_ENABLED", true), Interval: rolloutRiskInterval}
		if a.interval != 0 {
			ac.Interval = duration(a.prefix+"_INTERVAL", a.interval, 0)
		}
		ac.Enabled = ac.Enabled && ac.Interval > 0
		c.Agents[a.name] = ac
	}
	if firstErr != nil {
		return AIConfig{}, firstErr
	}
	return c, nil
}
