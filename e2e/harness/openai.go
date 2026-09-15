package harness

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// OpenAIFixture is a running `nexora-fixture openai` scripted fake OpenAI-compatible server.
type OpenAIFixture struct {
	URL string // "http://127.0.0.1:<port>/v1"
}

// OpenAIResponse is one scripted chat completion answer. FinishReason defaults to "stop" and
// Status to 200; a non-200 Status answers {"error":{"message":Content}}. A Hold response waits until
// Release for its feature (use it where a test must observe the task before the model answers).
type OpenAIResponse struct {
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	FinishReason     string `json:"finish_reason"`
	Status           int    `json:"status"`
	DelayMS          int    `json:"delay_ms"`
	Hold             bool   `json:"hold"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens"`
}

// OpenAIRequest is what the fixture recorded about one chat completion request. ResponseFormat is
// "", "json_schema" or "json_object"; Bearer says an Authorization: Bearer header was present (its
// value is never recorded).
type OpenAIRequest struct {
	Feature, Model, ResponseFormat string
	Bearer                         bool
	MaxTokens, Messages            int
	At                             time.Time
}

// StartOpenAIFixture starts `nexora-fixture openai` on a kernel-chosen loopback port.
func (e *Env) StartOpenAIFixture() *OpenAIFixture {
	e.T.Helper()
	p := e.Start("nexora-fixture", []string{"openai", "--listen", loopbackPort0}, nil)
	return &OpenAIFixture{URL: "http://" + p.Addr(p.WaitReady(10*time.Second), "openai") + "/v1"}
}

func (f *OpenAIFixture) control() string {
	return strings.TrimSuffix(f.URL, "/v1") + "/control"
}

// ControlURL is the fixture's control endpoint, for Playwright specs that release held responses
// with POST <ControlURL>/release/<feature>.
func (f *OpenAIFixture) ControlURL() string { return f.control() }

// Release lets feature's held responses answer, now and until Reset.
func (f *OpenAIFixture) Release(t *testing.T, feature string) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.control()+"/release/"+url.PathEscape(feature), nil, nil)
}

// Script replaces feature's response queue with rs; the last response repeats.
func (f *OpenAIFixture) Script(t *testing.T, feature string, rs ...OpenAIResponse) {
	t.Helper()
	fixtureCall(t, http.MethodPut, f.control()+"/script/"+url.PathEscape(feature),
		map[string][]OpenAIResponse{"responses": rs}, nil)
}

// ScriptJSON scripts feature with each v marshalled as the answer content, using 120 prompt, 80
// completion and 40 reasoning tokens per answer.
func (f *OpenAIFixture) ScriptJSON(t *testing.T, feature string, vs ...any) {
	t.Helper()
	rs := make([]OpenAIResponse, 0, len(vs))
	for _, v := range vs {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		rs = append(rs, OpenAIResponse{Content: string(b), PromptTokens: 120, CompletionTokens: 80, ReasoningTokens: 40})
	}
	f.Script(t, feature, rs...)
}

// Requests returns every chat completion request the fixture received since start or Reset.
func (f *OpenAIFixture) Requests(t *testing.T) []OpenAIRequest {
	t.Helper()
	var out []OpenAIRequest
	fixtureCall(t, http.MethodGet, f.control()+"/requests", nil, &out)
	return out
}

// Reset clears every script and the recorded requests.
func (f *OpenAIFixture) Reset(t *testing.T) {
	t.Helper()
	fixtureCall(t, http.MethodPost, f.control()+"/reset", nil, nil)
}

// aiAgentIntervalVars are the interval variables of every agent that has one (rollout risk polls
// at a fixed 15 s).
var aiAgentIntervalVars = []string{
	"NEXORA_AI_QUERYLOG_INTERVAL",
	"NEXORA_AI_DASHBOARD_INTERVAL",
	"NEXORA_AI_FILTER_RECOMMENDATIONS_INTERVAL",
	"NEXORA_AI_UPSTREAM_PREDICTION_INTERVAL",
	"NEXORA_AI_THREAT_CLASSIFICATION_INTERVAL",
	"NEXORA_AI_CAPACITY_FORECAST_INTERVAL",
	"NEXORA_AI_RPZ_SUGGESTIONS_INTERVAL",
}

// AIEnv returns the management plane environment that points AI at f with the fake model, a
// generous rate limit, and agents that only run through runAiAgent, followed by extra.
func AIEnv(f *OpenAIFixture, extra ...string) []string {
	env := []string{
		"NEXORA_AI_BASE_URL=" + f.URL,
		"NEXORA_AI_MODEL=fake-qwen",
		"NEXORA_AI_API_KEY=test-key-not-secret",
		"NEXORA_AI_REQUESTS_PER_MINUTE=600",
		"NEXORA_AI_LLM_MIN_INTERVAL=1s",
		"NEXORA_AI_AGENT_START_DELAY=1000h",
	}
	for _, v := range aiAgentIntervalVars {
		env = append(env, v+"=24h")
	}
	return append(env, extra...)
}
