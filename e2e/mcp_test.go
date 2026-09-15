package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/mcp"

	"github.com/piwi3910/nexora/e2e/harness"
)

// createToken creates an API token with role through admin and returns its id and secret.
func createToken(admin *harness.API, name, role string) (id, token string) {
	admin.T.Helper()
	var created struct {
		APIToken struct {
			ID string `json:"id"`
		} `json:"api_token"`
		Token string `json:"token"`
	}
	admin.Must(http.MethodPost, "/api-tokens", map[string]any{"name": name, "role": role}, &created, http.StatusCreated)
	return created.APIToken.ID, created.Token
}

// mcpClient is an initialized go-ai-sdk MCP client over Streamable HTTP with the bearer token.
func mcpClient(t *testing.T, ctx context.Context, baseURL, token string) *mcp.Client {
	t.Helper()
	c := mcp.NewClient(mcp.NewStreamableHTTPTransport(baseURL+"/mcp", map[string]string{"Authorization": "Bearer " + token}))
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func toolNames(t *testing.T, ctx context.Context, c *mcp.Client) []string {
	t.Helper()
	tools, err := mcp.Tools(ctx, c)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}

// mcpPost sends one raw JSON-RPC ping to /mcp and returns the HTTP status.
func mcpPost(t *testing.T, baseURL, token, origin string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestMCPServerWithGoAISDKClient catches an MCP endpoint that bypasses the API's RBAC or audit, lists
// or runs write tools while read-only, lacks the credential or DNS-rebinding checks, serves broken
// resources or prompts, or depends on AI being configured.
func TestMCPServerWithGoAISDKClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	// No NEXORA_AI_* variables: AI stays unconfigured, MCP must still work.
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_MCP_ENABLED=true", "NEXORA_MCP_READ_ONLY=true"}})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	if s, _ := aiStatus(admin); s.Enabled {
		t.Fatalf("AI is configured in the MCP test: %+v", s)
	}
	_, viewerToken := createToken(admin, "mcp-viewer", "viewer")
	operatorID, operatorToken := createToken(admin, "mcp-operator", "operator")
	admin.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "existing", "cidrs": []string{"10.1.0.0/16"}}, nil, http.StatusCreated)

	if code := mcpPost(t, mg.BaseURL, viewerToken, ""); code != http.StatusOK {
		t.Fatalf("ping with a viewer token = %d, want 200", code)
	}
	if code := mcpPost(t, mg.BaseURL, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", code)
	}
	if code := mcpPost(t, mg.BaseURL, viewerToken, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("foreign Origin = %d, want 403", code)
	}

	viewer := mcpClient(t, ctx, mg.BaseURL, viewerToken)
	if v := viewer.ProtocolVersion(); v != "2025-06-18" {
		t.Fatalf("negotiated %q, want 2025-06-18", v)
	}
	names := toolNames(t, ctx, viewer)
	if !slices.Contains(names, "nexora_policy_groups_list") || slices.Contains(names, "nexora_policy_groups_create") {
		t.Fatalf("read-only viewer tools = %v", names)
	}
	res, err := viewer.CallTool(ctx, "nexora_policy_groups_list", nil)
	if err != nil || res.IsError || !strings.Contains(res.Text, `"existing"`) {
		t.Fatalf("nexora_policy_groups_list = %+v, %v", res, err)
	}
	operatorRO := mcpClient(t, ctx, mg.BaseURL, operatorToken)
	if names := toolNames(t, ctx, operatorRO); slices.Contains(names, "nexora_policy_groups_create") {
		t.Fatalf("read-only operator lists a write tool: %v", names)
	}
	res, err = operatorRO.CallTool(ctx, "nexora_policy_groups_create", json.RawMessage(`{"body":{"name":"ro-refused","cidrs":["10.2.0.0/16"]}}`))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "read_only") {
		t.Fatalf("read-only create = %+v, %v", res, err)
	}
	contents, err := viewer.ReadResource(ctx, "nexora://fleet/status")
	if err != nil || len(contents) != 1 || !json.Valid([]byte(contents[0].Text)) {
		t.Fatalf("resources/read nexora://fleet/status = %+v, %v", contents, err)
	}
	if _, msgs, err := viewer.GetPrompt(ctx, "nexora_status", nil); err != nil || len(msgs) != 1 || len(msgs[0].Content) == 0 ||
		!strings.Contains(msgs[0].Content[0].Text, "nexora_fleet_summary") {
		t.Fatalf("prompts/get nexora_status = %+v, %v", msgs, err)
	}

	// Read-only off: the operator's write goes through the API with its audit row, the viewer's is refused.
	mg.Proc.Stop()
	mg = env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_MCP_ENABLED=true", "NEXORA_MCP_READ_ONLY=false"}})
	operator := mcpClient(t, ctx, mg.BaseURL, operatorToken)
	if names := toolNames(t, ctx, operator); !slices.Contains(names, "nexora_policy_groups_create") {
		t.Fatalf("operator tools lack nexora_policy_groups_create: %v", names)
	}
	res, err = operator.CallTool(ctx, "nexora_policy_groups_create", json.RawMessage(`{"body":{"name":"mcp-created","cidrs":["10.3.0.0/16"]}}`))
	if err != nil || res.IsError || !strings.Contains(res.Text, `"mcp-created"`) {
		t.Fatalf("operator create = %+v, %v", res, err)
	}
	if n := pgInt(t, pg.URL, `select count(*) from audit_log where action = 'createPolicyGroup' and actor_type = 'api_token' and actor_id = $1`, operatorID); n != 1 {
		t.Fatalf("audit rows of the operator token's create = %d, want 1", n)
	}
	viewer = mcpClient(t, ctx, mg.BaseURL, viewerToken)
	if names := toolNames(t, ctx, viewer); slices.Contains(names, "nexora_policy_groups_create") {
		t.Fatalf("viewer lists an operator tool: %v", names)
	}
	res, err = viewer.CallTool(ctx, "nexora_policy_groups_create", json.RawMessage(`{"body":{"name":"viewer-refused","cidrs":["10.4.0.0/16"]}}`))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "forbidden") {
		t.Fatalf("viewer create = %+v, %v", res, err)
	}
	if n := pgInt(t, pg.URL, `select count(*) from policy_groups where name in ('viewer-refused', 'ro-refused')`); n != 0 {
		t.Fatalf("refused creates wrote %d policy groups", n)
	}
	if got := mg.Metric(t, "nexora_mgmt_mcp_tool_calls_total", map[string]string{"tool": "nexora_policy_groups_create", "outcome": "ok"}); got != 1 {
		t.Fatalf("nexora_mgmt_mcp_tool_calls_total ok = %v, want 1", got)
	}
}

// TestMCPStdioBridge catches a stdio bridge that cannot relay the handshake and tools/list to the HTTP
// endpoint with its token.
func TestMCPStdioBridge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_MCP_ENABLED=true"}})
	admin := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	_, token := createToken(admin, "mcp-stdio", "viewer")
	tokenFile := filepath.Join(env.Dir, "mcp-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, err := mcp.NewStdioTransport([]string{env.Bin("nexora-mgmt"), "mcp-stdio", "--url", mg.BaseURL, "--token-file", tokenFile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(tr)
	defer c.Close()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize over stdio: %v", err)
	}
	names := toolNames(t, ctx, c)
	if !slices.Contains(names, "nexora_fleet_summary") || slices.Contains(names, "nexora_policy_groups_create") {
		t.Fatalf("stdio tools = %v", names)
	}
}
