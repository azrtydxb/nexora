package harness_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestOpenAIFixtureHarness(t *testing.T) {
	fx := harness.New(t).StartOpenAIFixture()
	fx.ScriptJSON(t, "smoke", map[string]int{"a": 1})

	body := `{"model":"fake-qwen","max_tokens":64,"response_format":{"type":"json_object"},` +
		`"messages":[{"role":"system","content":"nexora-feature: smoke\nnotice"},{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, fx.URL+"/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key-not-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != http.StatusOK || len(out.Choices) != 1 ||
		out.Choices[0].Message.Content != `{"a":1}` || out.Usage.PromptTokens != 120 {
		t.Fatalf("chat completion %d %s: %v", resp.StatusCode, raw, err)
	}

	reqs := fx.Requests(t)
	if len(reqs) != 1 || reqs[0].Feature != "smoke" || !reqs[0].Bearer || reqs[0].ResponseFormat != "json_object" ||
		reqs[0].MaxTokens != 64 || reqs[0].Messages != 2 || reqs[0].At.IsZero() {
		t.Fatalf("requests %+v", reqs)
	}
	env := harness.AIEnv(fx, "EXTRA=1")
	if !slices.Contains(env, "NEXORA_AI_MODEL=fake-qwen") || !slices.Contains(env, "NEXORA_AI_BASE_URL="+fx.URL) ||
		!slices.Contains(env, "NEXORA_AI_QUERYLOG_INTERVAL=24h") || env[len(env)-1] != "EXTRA=1" {
		t.Fatalf("AIEnv %v", env)
	}

	fx.Reset(t)
	if got := fx.Requests(t); len(got) != 0 {
		t.Fatalf("after reset: %+v", got)
	}
}
