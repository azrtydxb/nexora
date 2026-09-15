package harness_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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

// Catches a held response that answers before Release (a spec asserting the in-progress state would
// race the model again) and a Release that does not let held and later requests through.
func TestOpenAIFixtureHoldsUntilReleased(t *testing.T) {
	fx := harness.New(t).StartOpenAIFixture()
	fx.Script(t, "held", harness.OpenAIResponse{Content: "late", Hold: true})
	chat := func() <-chan int {
		done := make(chan int, 1)
		go func() {
			body := `{"model":"fake-qwen","messages":[{"role":"system","content":"nexora-feature: held"}]}`
			resp, err := http.Post(fx.URL+"/chat/completions", "application/json", strings.NewReader(body))
			if err != nil {
				done <- 0
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
		return done
	}
	first := chat()
	select {
	case code := <-first:
		t.Fatalf("held response answered before Release: %d", code)
	case <-time.After(500 * time.Millisecond):
	}
	fx.Release(t, "held")
	for i, c := range []<-chan int{first, chat()} {
		select {
		case code := <-c:
			if code != http.StatusOK {
				t.Fatalf("request %d after Release: status %d", i, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d still held after Release", i)
		}
	}
}
