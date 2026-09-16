package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/mcp"
	"github.com/piwi3910/nexora/e2e/harness"
)

// kwPromValue queries Prometheus at NEXORA_KW_PROMETHEUS_URL and returns
// the value (as a float64) for the given query. Returns 0 if the query
// has no result (or an error that prevents querying).
func kwPromValue(t *testing.T, q string) float64 {
	t.Helper()
	promURL := os.Getenv("NEXORA_KW_PROMETHEUS_URL")
	if promURL == "" {
		t.Skip("NEXORA_KW_PROMETHEUS_URL is not set")
	}
	resp, err := http.Get(promURL + "/api/v1/query?query=" + url.QueryEscape(q))
	if err != nil {
		t.Logf("prom query %s failed: %v", q, err)
		return 0
	}
	defer resp.Body.Close()
	var raw struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"` // [timestamp, value]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Logf("prom response decode %s failed: %v", q, err)
		return 0
	}
	if len(raw.Data.Result) == 0 {
		return 0
	}
	val := raw.Data.Result[0].Value[1]
	var v float64
	switch f := val.(type) {
	case float64:
		v = f
	case string:
		if _, err := fmt.Sscanf(f, "%f", &v); err != nil {
			t.Logf("prom value parse %s: %v", q, err)
			return 0
		}
	default:
		t.Logf("prom value unexpected type %T", val)
		return 0
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
	var task struct{ ID, Status, ErrorCode, ErrorMessage string }
	api.Must("POST", "/ai/query-log/search", map[string]string{"query": "blocked queries in the last hour"}, &task, 202)
	harness.Eventually(t, 300*time.Second, func() error {
		api.Must("GET", "/ai/tasks/"+task.ID, nil, &task, 200)
		if task.Status == "failed" {
			if task.ErrorCode == "busy" {
				return fmt.Errorf("ai busy, retrying")
			}
			t.Fatalf("search task failed: %s %s", task.ErrorCode, task.ErrorMessage)
		}
		if task.Status != "succeeded" {
			return fmt.Errorf("task %s", task.Status)
		}
		return nil
	})
	api.Must("POST", "/ai/agents/capacity_forecast/run", nil, nil, 202)
	harness.Eventually(t, 600*time.Second, func() error {
		var s struct {
			Agents []struct {
				Name        string `json:"name"`
				LastOutcome string `json:"last_outcome"`
				Running     bool
			}
		}
		api.Must("GET", "/ai/status", nil, &s, 200)
		for _, a := range s.Agents {
			if a.Name == "capacity_forecast" && !a.Running && (a.LastOutcome == "ok" || a.LastOutcome == "no_change") {
				return nil
			}
		}
		return errors.New("capacity_forecast not finished")
	})
	if in := kwPromValue(t, `sum(nexora_mgmt_ai_tokens_total{kind="input"})`); in <= 0 {
		t.Fatalf("input tokens %v", in)
	}
	if r := kwPromValue(t, `sum(nexora_mgmt_ai_tokens_total{kind="reasoning"})`); r <= 0 {
		t.Fatalf("reasoning tokens %v: fastllm reports no reasoning usage", r)
	}
	if after := kwPromValue(t, `sum(nexora_mgmt_ai_requests_total{outcome="invalid_output"}) or vector(0)`); after > invalidBefore {
		t.Fatalf("invalid_output grew from %v to %v", invalidBefore, after)
	}
	token := kwCreateAPIToken(t, api, "viewer")
	_ = token // the cleanup function revoked it; used for MCP auth below

	caFile := os.Getenv("NEXORA_KW_API_CA_FILE")
	apiRoots := certPool(t, caFile)
	mcpTransport := mcp.NewStreamableHTTPTransportWithOptions(
		os.Getenv("NEXORA_KW_API_URL")+"/mcp",
		mcp.WithAuthHeader("Authorization"),
		mcp.WithHTTPClientOpt(&http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: apiRoots}},
		}),
	)
	mcpClient := mcp.NewClient(mcpTransport)
	defer mcpClient.Close()
	if err := mcpClient.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tools, err := mcp.Tools(context.Background(), mcpClient); err != nil || len(tools) == 0 {
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
