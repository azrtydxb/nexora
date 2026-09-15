// Package mcpserver serves the Model Context Protocol (Streamable HTTP, one JSON body per POST, no
// sessions) at /mcp. Every tool call and resource read is replayed in-process through the API handler
// with the caller's own credentials, so authentication, RBAC, validation and audit are the API's.
package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"

	apispec "github.com/piwi3910/nexora/mgmt/api"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	// latestProtocol is answered to a client requesting a version this server does not support.
	latestProtocol = "2025-06-18"
	// maxRequestBytes is the API's request body limit.
	maxRequestBytes = 4 << 20
	// maxResultBytes bounds the text of one tool result or resource.
	maxResultBytes = 1 << 20
)

var supportedProtocols = []string{latestProtocol, "2025-03-26"}

// JSON-RPC error codes.
const (
	codeParseError       = -32700
	codeInvalidRequest   = -32600
	codeMethodNotFound   = -32601
	codeInvalidParams    = -32602
	codeInternal         = -32603
	codeResourceNotFound = -32002
)

// Options configure New.
type Options struct {
	Replayer   api.Replayer
	Store      *store.Store // blob reads for nexora://filter-lists/{id}/content
	PublicURL  string       // an Origin header must match its origin
	ReadOnly   bool         // only GET operations are listed and callable
	Registerer prometheus.Registerer
}

type server struct {
	o         Options
	origin    string
	requests  *prometheus.CounterVec
	toolCalls *prometheus.CounterVec
}

// New returns the /mcp handler.
func New(o Options) http.Handler {
	s := &server{
		o: o,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_mgmt_mcp_requests_total", Help: "MCP requests by JSON-RPC method and outcome",
		}, []string{"method", "outcome"}),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nexora_mgmt_mcp_tool_calls_total", Help: "MCP tool calls by tool and outcome",
		}, []string{"tool", "outcome"}),
	}
	if u, err := url.Parse(o.PublicURL); err == nil && u.Host != "" {
		s.origin = u.Scheme + "://" + u.Host
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(s.requests, s.toolCalls)
	}
	return s
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// methodFunc answers one JSON-RPC method for the authenticated caller.
type methodFunc func(s *server, r *http.Request, p auth.Principal, params json.RawMessage) (any, *rpcError)

var methods = map[string]methodFunc{
	"initialize":               (*server).initialize,
	"ping":                     func(*server, *http.Request, auth.Principal, json.RawMessage) (any, *rpcError) { return struct{}{}, nil },
	"tools/list":               (*server).listTools,
	"tools/call":               (*server).callTool,
	"resources/list":           (*server).listResources,
	"resources/templates/list": (*server).listResourceTemplates,
	"resources/read":           (*server).readResource,
	"prompts/list":             (*server).listPrompts,
	"prompts/get":              (*server).getPrompt,
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.httpError(w, http.StatusMethodNotAllowed, "invalid_request", "the MCP endpoint accepts POST only")
		return
	}
	// DNS-rebinding protection: a browser page on another origin must not reach the endpoint.
	if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, s.origin) {
		s.httpError(w, http.StatusForbidden, "forbidden", "Origin does not match NEXORA_PUBLIC_URL")
		return
	}
	// Like the API: a browser cannot send this type cross-site without a CORS preflight.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		s.httpError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "requests must send Content-Type: application/json")
		return
	}
	p, err := s.o.Replayer.Authenticate(r)
	switch {
	case errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrInvalidCredentials):
		w.Header().Set("WWW-Authenticate", "Bearer")
		s.httpError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	case err != nil:
		slog.Warn("mcp authenticate", "err", err)
		s.httpError(w, http.StatusServiceUnavailable, "unavailable", "authentication unavailable")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		s.httpError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body too large")
		return
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
		s.reply(w, "other", nil, nil, &rpcError{codeInvalidRequest, "JSON-RPC batches are not supported"})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.reply(w, "other", nil, nil, &rpcError{codeParseError, "parse error"})
		return
	}
	label := req.Method
	if _, ok := methods[label]; !ok && !strings.HasPrefix(label, "notifications/") {
		label = "other"
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// A notification or a client response: accepted, nothing to answer.
		s.requests.WithLabelValues(notificationLabel(label), "ok").Inc()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		s.reply(w, "other", req.ID, nil, &rpcError{codeInvalidRequest, "invalid JSON-RPC request"})
		return
	}
	m, ok := methods[req.Method]
	if !ok {
		s.reply(w, "other", req.ID, nil, &rpcError{codeMethodNotFound, "method not found: " + req.Method})
		return
	}
	result, rerr := m(s, r, p, req.Params)
	s.reply(w, label, req.ID, result, rerr)
}

// notificationLabel keeps the method label bounded for notifications.
func notificationLabel(method string) string {
	if method == "notifications/initialized" || method == "notifications/cancelled" {
		return method
	}
	return "other"
}

func (s *server) reply(w http.ResponseWriter, method string, id json.RawMessage, result any, rerr *rpcError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	body := map[string]any{"jsonrpc": "2.0", "id": id}
	outcome := "ok"
	if rerr != nil {
		body["error"], outcome = rerr, "error"
	} else {
		body["result"] = result
	}
	s.requests.WithLabelValues(method, outcome).Inc()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *server) httpError(w http.ResponseWriter, status int, code, message string) {
	s.requests.WithLabelValues("other", code).Inc()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func decodeParams(params json.RawMessage, v any) *rpcError {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
	}
	return nil
}

func (s *server) initialize(_ *http.Request, _ auth.Principal, params json.RawMessage) (any, *rpcError) {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := decodeParams(params, &in); err != nil {
		return nil, err
	}
	version := latestProtocol
	if slices.Contains(supportedProtocols, in.ProtocolVersion) {
		version = in.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]bool{"listChanged": false},
			"resources": map[string]bool{"listChanged": false, "subscribe": false},
			"prompts":   map[string]bool{"listChanged": false},
		},
		"serverInfo":   map[string]string{"name": "nexora-mgmt", "version": api.Version},
		"instructions": "Nexora DNS management plane. Tools act with the role of your credential and are audited like the API.",
	}, nil
}

// callable reports whether p may see and call the operation in this mode.
func (s *server) callable(p auth.Principal, op apispec.Operation) bool {
	return (!s.o.ReadOnly || op.Method == http.MethodGet) && auth.Authorize(p, op.ID) == nil
}

func (s *server) listTools(_ *http.Request, p auth.Principal, _ json.RawMessage) (any, *rpcError) {
	tools := []map[string]any{}
	for _, t := range Tools {
		if s.callable(p, apispec.Operations()[t.OperationID]) {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": toolSchema(t.OperationID)})
		}
	}
	return map[string]any{"tools": tools}, nil
}

func (s *server) callTool(r *http.Request, _ auth.Principal, params json.RawMessage) (any, *rpcError) {
	var in struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := decodeParams(params, &in); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(Tools, func(t Tool) bool { return t.Name == in.Name })
	if i < 0 {
		s.toolCalls.WithLabelValues("unknown", "error").Inc()
		return toolError("not_found", "unknown tool "+in.Name), nil
	}
	tool := Tools[i]
	op := apispec.Operations()[tool.OperationID]
	if s.o.ReadOnly && op.Method != http.MethodGet {
		s.toolCalls.WithLabelValues(tool.Name, "read_only").Inc()
		return toolError("read_only", "the MCP endpoint is read-only (NEXORA_MCP_READ_ONLY)"), nil
	}
	path, query, body, err := operationArgs(op, in.Arguments)
	if err != nil {
		s.toolCalls.WithLabelValues(tool.Name, "error").Inc()
		return toolError("invalid_request", err.Error()), nil
	}
	res := s.o.Replayer.Replay(r, op.ID, path, query, body)
	if res.Status >= 400 {
		outcome := "error"
		if res.Status == http.StatusForbidden {
			outcome = "forbidden"
		}
		s.toolCalls.WithLabelValues(tool.Name, outcome).Inc()
		return toolError(errorCode(res), res.Message), nil
	}
	s.toolCalls.WithLabelValues(tool.Name, "ok").Inc()
	text := fmt.Sprintf(`{"status":%d}`, res.Status)
	if len(res.Body) > 0 {
		text = bounded(res.Body)
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": false}, nil
}

func errorCode(res api.ReplayResult) string {
	if res.Code != "" {
		return res.Code
	}
	return fmt.Sprintf("http_%d", res.Status)
}

func toolError(code, message string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": code + ": " + message}}, "isError": true}
}

// operationArgs splits tool arguments into path parameters, query parameters and the body (dropped for
// GET operations).
func operationArgs(op apispec.Operation, args map[string]json.RawMessage) (map[string]string, url.Values, json.RawMessage, error) {
	path, query := map[string]string{}, url.Values{}
	for _, p := range op.Params {
		v, ok := args[p.Name]
		switch {
		case p.In == "path" && !ok:
			return nil, nil, nil, fmt.Errorf("missing path parameter %s", p.Name)
		case p.In == "path":
			s, err := scalar(v)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("parameter %s: %w", p.Name, err)
			}
			path[p.Name] = s
		case p.In == "query" && ok:
			var list []json.RawMessage
			if json.Unmarshal(v, &list) != nil {
				list = []json.RawMessage{v}
			}
			for _, e := range list {
				s, err := scalar(e)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("parameter %s: %w", p.Name, err)
				}
				query.Add(p.Name, s)
			}
		}
	}
	var body json.RawMessage
	if b, ok := args["body"]; ok && op.Method != http.MethodGet && op.Body != nil {
		body = b
	}
	return path, query, body, nil
}

// scalar renders a JSON string, number or boolean as its parameter text.
func scalar(v json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, nil
	}
	var x any
	if err := json.Unmarshal(v, &x); err != nil {
		return "", err
	}
	switch x.(type) {
	case float64, bool:
		return string(bytes.TrimSpace(v)), nil
	}
	return "", errors.New("must be a string, number or boolean")
}

// bounded is b as text, cut at maxResultBytes on a character boundary with a truncation note.
func bounded(b []byte) string {
	if len(b) <= maxResultBytes {
		return string(b)
	}
	cut := maxResultBytes
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + fmt.Sprintf("\n… truncated: true (the result has %d bytes; narrow the request)", len(b))
}

// replayGET replays a GET operation for a resource read and maps an API error to a JSON-RPC error.
func (s *server) replayGET(r *http.Request, operationID string, path map[string]string, query url.Values) ([]byte, *rpcError) {
	res := s.o.Replayer.Replay(r, operationID, path, query, nil)
	switch {
	case res.Status == http.StatusNotFound:
		return nil, &rpcError{codeResourceNotFound, "resource not found"}
	case res.Status >= 400:
		return nil, &rpcError{codeInternal, errorCode(res) + ": " + res.Message}
	}
	return res.Body, nil
}
