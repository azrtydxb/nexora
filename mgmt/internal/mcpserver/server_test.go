package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	apispec "github.com/piwi3910/nexora/mgmt/api"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// replayCall is one call the fake replayer received.
type replayCall struct {
	OperationID string
	Path        map[string]string
	Query       url.Values
	Body        json.RawMessage
}

// fakeReplayer authenticates the bearer "viewer", "operator" or "admin" with that role and answers
// replays from results (by operation id; 200 {} otherwise).
type fakeReplayer struct {
	mu      sync.Mutex
	calls   []replayCall
	results map[string]api.ReplayResult
}

func (f *fakeReplayer) Authenticate(r *http.Request) (auth.Principal, error) {
	role, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, err := auth.ParseRole(role); !ok || err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{Username: role, Role: auth.Role(role), Kind: "api_token"}, nil
}

func (f *fakeReplayer) Replay(_ *http.Request, operationID string, path map[string]string, query url.Values, body json.RawMessage) api.ReplayResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, replayCall{operationID, path, query, body})
	if res, ok := f.results[operationID]; ok {
		return res
	}
	return api.ReplayResult{Status: http.StatusOK, Body: json.RawMessage(`{}`)}
}

func (f *fakeReplayer) last(t *testing.T) replayCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no replay happened")
	}
	return f.calls[len(f.calls)-1]
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

const publicURL = "https://nexora.example:8443"

// post sends raw to the server with the given bearer and headers and returns the HTTP status and body.
func post(t *testing.T, h http.Handler, bearer, raw string, headers ...string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// call sends one JSON-RPC request and decodes the reply.
func call(t *testing.T, h http.Handler, bearer, method string, params any) rpcReply {
	t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	status, body := post(t, h, bearer, fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":%s}`, method, p))
	if status != http.StatusOK {
		t.Fatalf("%s: HTTP %d %s", method, status, body)
	}
	var r rpcReply
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("%s: %v: %s", method, err, body)
	}
	if string(r.ID) != "7" {
		t.Fatalf("%s: reply id %s, want 7", method, r.ID)
	}
	return r
}

func listTools(t *testing.T, h http.Handler, bearer string) map[string]bool {
	t.Helper()
	r := call(t, h, bearer, "tools/list", map[string]any{})
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if r.Error != nil || json.Unmarshal(r.Result, &res) != nil {
		t.Fatalf("tools/list: %+v %s", r.Error, r.Result)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		if len(tool.InputSchema) == 0 {
			t.Fatalf("%s has no inputSchema", tool.Name)
		}
		names[tool.Name] = true
	}
	return names
}

func callTool(t *testing.T, h http.Handler, bearer, name string, args any) toolResult {
	t.Helper()
	r := call(t, h, bearer, "tools/call", map[string]any{"name": name, "arguments": args})
	var res toolResult
	if r.Error != nil || json.Unmarshal(r.Result, &res) != nil || len(res.Content) == 0 {
		t.Fatalf("tools/call %s: %+v %s", name, r.Error, r.Result)
	}
	return res
}

func operationOf(name string) (apispec.Operation, auth.Role) {
	for _, tool := range Tools {
		if tool.Name == name {
			return apispec.Operations()[tool.OperationID], auth.Permissions[tool.OperationID]
		}
	}
	panic("no tool " + name)
}

// TestServerProtocol catches a JSON-RPC or Streamable HTTP deviation (version negotiation, batches,
// unknown methods, GET, notifications), a missing DNS-rebinding or credential check, read-only mode or
// role filtering that lists or runs what it must not, an API error that is not a tool error, and an
// unbounded tool result.
func TestServerProtocol(t *testing.T) {
	fr := &fakeReplayer{results: map[string]api.ReplayResult{}}
	h := New(Options{Replayer: fr, PublicURL: publicURL, ReadOnly: true, Registerer: prometheus.NewRegistry()})

	for requested, want := range map[string]string{"2025-06-18": "2025-06-18", "2025-03-26": "2025-03-26", "2024-01-01": "2025-06-18"} {
		r := call(t, h, "viewer", "initialize", map[string]any{"protocolVersion": requested, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "t", "version": "1"}})
		var res struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
		}
		if r.Error != nil || json.Unmarshal(r.Result, &res) != nil || res.ProtocolVersion != want {
			t.Fatalf("initialize %s: %+v %s, want %s", requested, r.Error, r.Result, want)
		}
		for _, c := range []string{"tools", "resources", "prompts"} {
			if _, ok := res.Capabilities[c]; !ok {
				t.Fatalf("initialize does not advertise %s: %s", c, r.Result)
			}
		}
	}
	if r := call(t, h, "viewer", "ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Fatalf("ping = %+v %s", r.Error, r.Result)
	}
	if status, body := post(t, h, "viewer", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("notification: HTTP %d %s, want 202 without body", status, body)
	}

	status, body := post(t, h, "viewer", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	var batch rpcReply
	if status != http.StatusOK || json.Unmarshal(body, &batch) != nil || batch.Error == nil || batch.Error.Code != -32600 {
		t.Fatalf("batch: HTTP %d %s, want error -32600", status, body)
	}
	if status, body := post(t, h, "viewer", `{"jsonrpc":"2.0","id":1,`); status != http.StatusOK || !bytes.Contains(body, []byte("-32700")) {
		t.Fatalf("malformed JSON: HTTP %d %s, want error -32700", status, body)
	}
	if r := call(t, h, "viewer", "sampling/createMessage", map[string]any{}); r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("unknown method = %+v, want -32601", r.Error)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
	if status, _ := post(t, h, "viewer", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Origin", publicURL); status != http.StatusOK {
		t.Fatalf("own Origin = %d, want 200", status)
	}
	if status, _ := post(t, h, "viewer", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Origin", "https://evil.example"); status != http.StatusForbidden {
		t.Fatalf("foreign Origin = %d, want 403", status)
	}
	if status, _ := post(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); status != http.StatusUnauthorized {
		t.Fatalf("no credential = %d, want 401", status)
	}
	if status, _ := post(t, h, "nobody", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); status != http.StatusUnauthorized {
		t.Fatalf("invalid credential = %d, want 401", status)
	}

	// Read-only: an operator sees the GET tools only, and a write tool is refused without a replay.
	names := listTools(t, h, "operator")
	if !names["nexora_policy_groups_list"] {
		t.Fatalf("read-only operator tools lack nexora_policy_groups_list: %v", names)
	}
	for name := range names {
		if op, _ := operationOf(name); op.Method != http.MethodGet {
			t.Fatalf("read-only lists %s (%s)", name, op.Method)
		}
	}
	calls := len(fr.calls)
	if res := callTool(t, h, "operator", "nexora_policy_groups_create", map[string]any{"body": map[string]any{"name": "x", "cidrs": []string{"10.0.0.0/8"}}}); !res.IsError || !strings.Contains(res.Content[0].Text, "read_only") {
		t.Fatalf("read-only create = %+v, want isError read_only", res)
	}
	if len(fr.calls) != calls {
		t.Fatal("a read-only refusal still replayed the operation")
	}

	// A GET tool passes path and query parameters and drops a body.
	if res := callTool(t, h, "viewer", "nexora_query_log_query", map[string]any{"name": "example.com", "qtype": []string{"A", "AAAA"}, "limit": 5, "body": map[string]any{"x": 1}}); res.IsError {
		t.Fatalf("query log tool = %+v", res)
	}
	if c := fr.last(t); c.OperationID != "searchQueryLog" || c.Query.Get("name") != "example.com" || len(c.Query["qtype"]) != 2 || c.Query.Get("limit") != "5" || c.Body != nil {
		t.Fatalf("query log replay = %+v", c)
	}
	if res := callTool(t, h, "viewer", "nexora_engines_get", map[string]any{}); !res.IsError || !strings.Contains(res.Content[0].Text, "invalid_request") {
		t.Fatalf("missing path parameter = %+v, want isError invalid_request", res)
	}
	if res := callTool(t, h, "viewer", "nexora_nope", map[string]any{}); !res.IsError || !strings.Contains(res.Content[0].Text, "not_found") {
		t.Fatalf("unknown tool = %+v, want isError not_found", res)
	}

	// Read-only off: a viewer sees no operator tool, an operator does, and a replayed 403 is a tool error.
	rw := New(Options{Replayer: fr, PublicURL: publicURL, ReadOnly: false, Registerer: prometheus.NewRegistry()})
	names = listTools(t, rw, "viewer")
	if !names["nexora_policy_groups_list"] {
		t.Fatalf("viewer tools lack nexora_policy_groups_list: %v", names)
	}
	for name := range names {
		if _, role := operationOf(name); role != auth.RoleViewer {
			t.Fatalf("viewer lists %s (role %s)", name, role)
		}
	}
	if names = listTools(t, rw, "operator"); !names["nexora_policy_groups_create"] || names["nexora_engines_revoke"] {
		t.Fatalf("operator tools = %v, want nexora_policy_groups_create and no admin tool", names)
	}
	if names = listTools(t, rw, "admin"); len(names) != len(Tools) {
		t.Fatalf("admin lists %d tools, want %d", len(names), len(Tools))
	}
	fr.results["createPolicyGroup"] = api.ReplayResult{Status: http.StatusForbidden, Code: "forbidden", Message: "createPolicyGroup requires role operator"}
	if res := callTool(t, rw, "viewer", "nexora_policy_groups_create", map[string]any{"body": map[string]any{"name": "x"}}); !res.IsError || !strings.Contains(res.Content[0].Text, "forbidden") {
		t.Fatalf("replayed 403 = %+v, want isError forbidden", res)
	}
	if c := fr.last(t); c.OperationID != "createPolicyGroup" || string(c.Body) != `{"name":"x"}` {
		t.Fatalf("create replay = %+v", c)
	}
	if res := callTool(t, rw, "operator", "nexora_policy_groups_delete", map[string]any{"id": "g1", "revision": 3}); res.IsError {
		t.Fatalf("delete = %+v", res)
	}
	if c := fr.last(t); c.Path["id"] != "g1" || c.Query.Get("revision") != "3" {
		t.Fatalf("delete replay = %+v", c)
	}

	// A result over 1 MiB is truncated and says so.
	big := `"` + strings.Repeat("a", 2<<20) + `"`
	fr.results["listPolicyGroups"] = api.ReplayResult{Status: http.StatusOK, Body: json.RawMessage(big)}
	res := callTool(t, h, "viewer", "nexora_policy_groups_list", map[string]any{})
	if res.IsError || len(res.Content[0].Text) > maxResultBytes+200 || !strings.Contains(res.Content[len(res.Content)-1].Text, "truncated: true") {
		t.Fatalf("2 MiB result: isError %v, %d bytes, last part %.100q", res.IsError, len(res.Content[0].Text), res.Content[len(res.Content)-1].Text)
	}
}

// TestResourcesAndPrompts catches a resource bound to the wrong operation or read without the API's
// authorization, filter-list content that is not decoded or not bounded to 10,000 names, and prompts
// that ignore or fail to validate their argument.
func TestResourcesAndPrompts(t *testing.T) {
	st := storetest.New(t)
	names := make([]string, 10_050)
	for i := range names {
		names[i] = fmt.Sprintf("d%05d.example", i)
	}
	data, sha, err := blocklist.Compress(blocklist.Normalize(names))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(context.Background(), "insert into blobs(sha256, size, data) values ($1, $2, $3)", sha, len(data), data); err != nil {
		t.Fatal(err)
	}
	fr := &fakeReplayer{results: map[string]api.ReplayResult{
		"getFleetSummary": {Status: http.StatusOK, Body: json.RawMessage(`{"engines":2}`)},
		"getFilterList":   {Status: http.StatusOK, Body: json.RawMessage(fmt.Sprintf(`{"id":"l1","current_blob_sha256":%q}`, sha))},
	}}
	h := New(Options{Replayer: fr, Store: st, PublicURL: publicURL, ReadOnly: true, Registerer: prometheus.NewRegistry()})

	read := func(uri string) rpcReply {
		return call(t, h, "viewer", "resources/read", map[string]string{"uri": uri})
	}
	type contents struct {
		Contents []struct {
			URI, MimeType, Text string
		} `json:"contents"`
	}
	var c contents
	if r := read("nexora://fleet/status"); r.Error != nil || json.Unmarshal(r.Result, &c) != nil || len(c.Contents) != 1 || c.Contents[0].Text != `{"engines":2}` || c.Contents[0].URI != "nexora://fleet/status" {
		t.Fatalf("fleet status = %+v %s", r.Error, r.Result)
	}
	if fr.last(t).OperationID != "getFleetSummary" {
		t.Fatalf("fleet status replayed %+v", fr.last(t))
	}
	if r := read("nexora://query-log/recent"); r.Error != nil || fr.last(t).OperationID != "searchQueryLog" || fr.last(t).Query.Get("limit") != "100" {
		t.Fatalf("recent query log = %+v, replay %+v", r.Error, fr.last(t))
	}
	c = contents{}
	if r := read("nexora://filter-lists/l1/content"); r.Error != nil || json.Unmarshal(r.Result, &c) != nil || len(c.Contents) != 1 {
		t.Fatalf("filter list content = %+v %.200s", r.Error, r.Result)
	}
	if lines := strings.Split(strings.TrimSuffix(c.Contents[0].Text, "\n"), "\n"); len(lines) != 10_000 || lines[0] != "d00000.example" || lines[9999] != "d09999.example" {
		t.Fatalf("filter list content has %d lines (%q … %q), want the first 10000", len(lines), lines[0], lines[len(lines)-1])
	}
	if c := fr.last(t); c.OperationID != "getFilterList" || c.Path["id"] != "l1" {
		t.Fatalf("content authorization replay = %+v", c)
	}
	fr.results["getFilterList"] = api.ReplayResult{Status: http.StatusForbidden, Code: "forbidden", Message: "no"}
	if r := read("nexora://filter-lists/l1/content"); r.Error == nil || !strings.Contains(r.Error.Message, "forbidden") {
		t.Fatalf("forbidden filter list content = %+v %.100s", r.Error, r.Result)
	}
	if r := read("nexora://nope"); r.Error == nil || r.Error.Code != -32002 {
		t.Fatalf("unknown resource = %+v, want -32002", r.Error)
	}

	var list struct {
		Resources []struct{ URI string } `json:"resources"`
	}
	if r := call(t, h, "viewer", "resources/list", map[string]any{}); r.Error != nil || json.Unmarshal(r.Result, &list) != nil || len(list.Resources) != 4 {
		t.Fatalf("resources/list = %+v %s", r.Error, r.Result)
	}
	if r := call(t, h, "viewer", "resources/templates/list", map[string]any{}); r.Error != nil || !strings.Contains(string(r.Result), "nexora://filter-lists/{id}/content") {
		t.Fatalf("resources/templates/list = %+v %s", r.Error, r.Result)
	}
	if r := call(t, h, "viewer", "prompts/list", map[string]any{}); r.Error != nil || strings.Count(string(r.Result), `"name":"nexora_`) != 3 {
		t.Fatalf("prompts/list = %+v %s", r.Error, r.Result)
	}
	var prompt struct {
		Messages []struct {
			Role    string `json:"role"`
			Content struct{ Type, Text string }
		} `json:"messages"`
	}
	r := call(t, h, "viewer", "prompts/get", map[string]any{"name": "nexora_query_log", "arguments": map[string]string{"minutes": "30"}})
	if r.Error != nil || json.Unmarshal(r.Result, &prompt) != nil || len(prompt.Messages) != 1 || prompt.Messages[0].Role != "user" ||
		prompt.Messages[0].Content.Text != "Show Nexora query log entries from the last 30 minutes using nexora_query_log_query." {
		t.Fatalf("prompts/get nexora_query_log = %+v %s", r.Error, r.Result)
	}
	if r := call(t, h, "viewer", "prompts/get", map[string]any{"name": "nexora_query_log"}); r.Error != nil || !strings.Contains(string(r.Result), "last 15 minutes") {
		t.Fatalf("prompts/get default minutes = %+v %s", r.Error, r.Result)
	}
	if r := call(t, h, "viewer", "prompts/get", map[string]any{"name": "nexora_query_log", "arguments": map[string]string{"minutes": "soon"}}); r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("invalid minutes = %+v, want -32602", r.Error)
	}
	if r := call(t, h, "viewer", "prompts/get", map[string]any{"name": "nexora_nope"}); r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("unknown prompt = %+v, want -32602", r.Error)
	}
}
