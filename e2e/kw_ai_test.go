package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/mcp"
	"github.com/piwi3910/nexora/e2e/harness"
)

// kwPromValue queries Prometheus at NEXORA_KW_PROMETHEUS_URL and returns
// the value (as a float64) for the given query. Returns 0 if the query
// has no result. Query and decoding errors fail the test.
func kwPromValue(t *testing.T, q string) float64 {
	t.Helper()
	promURL := os.Getenv("NEXORA_KW_PROMETHEUS_URL")
	if promURL == "" {
		t.Skip("NEXORA_KW_PROMETHEUS_URL is not set")
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(promURL + "/api/v1/query?query=" + url.QueryEscape(q))
	if err != nil {
		t.Fatalf("prom query %s failed: %v", q, err)
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prom query %s: HTTP %d", q, resp.StatusCode)
	}
	var raw struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"` // [timestamp, value]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("prom response decode %s failed: %v", q, err)
		return 0
	}
	if raw.Status != "success" {
		t.Fatalf("prom query %s: status %q", q, raw.Status)
	}
	if len(raw.Data.Result) == 0 {
		return 0
	}
	if len(raw.Data.Result) != 1 || len(raw.Data.Result[0].Value) != 2 {
		t.Fatalf("prom query %s: expected one timestamp/value pair", q)
	}
	val := raw.Data.Result[0].Value[1]
	var v float64
	switch f := val.(type) {
	case float64:
		v = f
	case string:
		v, err = strconv.ParseFloat(f, 64)
		if err != nil {
			t.Fatalf("prom value parse %s: %v", q, err)
			return 0
		}
	default:
		t.Fatalf("prom value unexpected type %T", val)
		return 0
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Fatalf("prom query %s: non-finite value %v", q, v)
	}
	return v
}

// kwCreateAPIToken creates an API token via POST /api-tokens and returns
// the generated token string. The token is revoked in t.Cleanup.
func kwCreateAPIToken(t *testing.T, api *harness.API, role string) string {
	t.Helper()
	name := "kw-ai-test-" + regexp.MustCompile(`[^a-zA-Z0-9-]`).ReplaceAllString(t.Name(), "-")
	var created struct {
		Token string `json:"token"`
	}
	api.Must(http.MethodPost, "/api-tokens", map[string]any{"name": name, "role": role}, &created, http.StatusCreated)
	t.Cleanup(func() {
		var tokens []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Revision int64  `json:"revision"`
		}
		api.Must(http.MethodGet, "/api-tokens", nil, &tokens, http.StatusOK)
		for _, tok := range tokens {
			if tok.Name == name {
				api.Must(http.MethodDelete, "/api-tokens/"+tok.ID+"?revision="+fmt.Sprint(tok.Revision), nil, nil, http.StatusNoContent)
				return
			}
		}
	})
	return created.Token
}

type kwAITask struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// TestKwSmokeAI checks that the AI service is wired, that the model
// responds, and that the MCP server serves tools. It is a kw-deployment
// smoke test — it skips when NEXORA_KW_API_URL is not set.
func TestKwSmokeAI(t *testing.T) {
	api := kwAdminAPI(t)
	var st struct {
		Enabled      bool   `json:"enabled"`
		Reason       string `json:"reason"`
		Model        string `json:"model"`
		EndpointHost string `json:"endpoint_host"`
	}
	api.Must("GET", "/ai/status", nil, &st, 200)
	if !st.Enabled || st.Model != "qwen3-6-35b-a3b" {
		t.Fatalf("AI status on kw: %+v (Secret nexora-ai present in namespace nexora?)", st)
	}
	if a, err := netip.ParseAddr(st.EndpointHost); err != nil || !a.IsPrivate() {
		t.Fatalf("endpoint host %q is not a private address", st.EndpointHost)
	}
	invalidBefore := kwPromValue(t, `sum(nexora_mgmt_ai_requests_total{outcome="invalid_output"}) or vector(0)`)
	var task kwAITask
	attempts := 0
	startSearch := func() {
		task = kwAITask{}
		attempts++
		api.Must("POST", "/ai/query-log/search", map[string]string{"query": "blocked queries in the last hour"}, &task, 202)
		if task.ID == "" {
			t.Fatal("search task has no ID")
		}
	}
	startSearch()
	harness.Eventually(t, 300*time.Second, func() error {
		api.Must("GET", "/ai/tasks/"+task.ID, nil, &task, 200)
		if task.Status == "failed" {
			if (task.ErrorCode == "busy" || task.ErrorCode == "timeout") && attempts < 3 {
				t.Logf("search task %s failed: %s; submitting replacement", task.ID, task.ErrorCode)
				time.Sleep(time.Second)
				startSearch()
				return errors.New("replacement search queued")
			}
			t.Fatalf("search task failed: code=%q msg=%q", task.ErrorCode, task.ErrorMessage)
		}
		if task.Status != "succeeded" {
			return fmt.Errorf("task %s", task.Status)
		}
		return nil
	})
	type agentStatus struct {
		Agents []struct {
			Name          string    `json:"name"`
			LastOutcome   string    `json:"last_outcome"`
			LastStartedAt time.Time `json:"last_started_at"`
			Running       bool      `json:"running"`
		} `json:"agents"`
	}
	var before agentStatus
	api.Must("GET", "/ai/status", nil, &before, 200)
	var previousRun time.Time
	for _, a := range before.Agents {
		if a.Name == "capacity_forecast" {
			previousRun = a.LastStartedAt
		}
	}
	api.Must("POST", "/ai/agents/capacity_forecast/run", nil, nil, 202)
	harness.Eventually(t, 600*time.Second, func() error {
		var s agentStatus
		api.Must("GET", "/ai/status", nil, &s, 200)
		for _, a := range s.Agents {
			if a.Name == "capacity_forecast" && a.LastStartedAt.After(previousRun) && !a.Running && (a.LastOutcome == "ok" || a.LastOutcome == "no_change") {
				return nil
			}
		}
		return errors.New("capacity_forecast not finished")
	})
	if in := kwPromValue(t, `sum(nexora_mgmt_ai_tokens_total{kind="input"})`); in <= 0 {
		t.Errorf("no input tokens reported by the AI model")
	}
	if r := kwPromValue(t, `sum(nexora_mgmt_ai_tokens_total{kind="reasoning"})`); r <= 0 {
		t.Errorf("no reasoning tokens reported by the AI model")
	}
	if after := kwPromValue(t, `sum(nexora_mgmt_ai_requests_total{outcome="invalid_output"}) or vector(0)`); after > invalidBefore {
		t.Errorf("invalid_output grew from %v to %v", invalidBefore, after)
	}
	token := kwCreateAPIToken(t, api, "viewer")

	caFile := os.Getenv("NEXORA_KW_API_CA_FILE")
	apiRoots := certPool(t, caFile)
	mcpTransport := mcp.NewStreamableHTTPTransportWithOptions(
		os.Getenv("NEXORA_KW_API_URL")+"/mcp",
		mcp.WithTokenProvider(mcp.TokenProviderFunc(func(context.Context) (string, error) { return token, nil })),
		mcp.WithHTTPClientOpt(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: apiRoots, MinVersion: tls.VersionTLS12}},
		}),
	)
	mcpClient := mcp.NewClient(mcpTransport)
	defer mcpClient.Close()
	mcpCtx, cancelMCP := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelMCP()
	if err := mcpClient.Initialize(mcpCtx); err != nil {
		t.Fatal(err)
	}
	if tools, err := mcp.Tools(mcpCtx, mcpClient); err != nil || len(tools) == 0 {
		t.Fatalf("MCP tools: %d %v", len(tools), err)
	}
}

// kwAdminAPI signs in as the bootstrap admin and returns an authenticated
// harness.API client. It skips when NEXORA_KW_API_URL is not set.
func kwAdminAPI(t *testing.T) *harness.API {
	t.Helper()
	apiURL := os.Getenv("NEXORA_KW_API_URL")
	if apiURL == "" {
		t.Skip("NEXORA_KW_API_URL is not set")
	}
	if !strings.HasPrefix(apiURL, "https://") {
		t.Fatalf("NEXORA_KW_API_URL must be https (session cookies are Secure): %s", apiURL)
	}
	raw, err := os.ReadFile(os.Getenv("NEXORA_KW_ADMIN_PASSWORD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(string(raw))
	caFile := os.Getenv("NEXORA_KW_API_CA_FILE")
	apiRoots := certPool(t, caFile)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	api := &harness.API{T: t, Base: apiURL, HC: &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: apiRoots, MinVersion: tls.VersionTLS12}},
	}}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	req, err := http.NewRequest(http.MethodPost, apiURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := api.HC.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	for _, c := range cookies {
		if !c.Secure || !c.HttpOnly {
			t.Fatalf("cookie %s: Secure=%v HttpOnly=%v, want both", c.Name, c.Secure, c.HttpOnly)
		}
	}
	return api
}
