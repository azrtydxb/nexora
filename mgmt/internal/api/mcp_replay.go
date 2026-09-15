package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// ReplayResult is the response of an API operation replayed for MCP.
type ReplayResult struct {
	Status        int
	Body          json.RawMessage
	Code, Message string // from the Error body when Status >= 400
}

// Replayer runs API operations in-process with the credentials of an MCP request.
type Replayer interface {
	// Replay serves operationID through the full API router as a request carrying only r's Cookie and
	// Authorization headers, so authentication (401 without a valid credential), RBAC, validation and
	// audit are exactly those of a direct API call by the same caller.
	Replay(r *http.Request, operationID string, path map[string]string, query url.Values, body json.RawMessage) ReplayResult
	// Authenticate resolves r's credential like the API does.
	Authenticate(r *http.Request) (auth.Principal, error)
}

// NewHandlerWithReplayer is NewHandler that also returns the Replayer over the same router, for the MCP
// server mounted into it.
func NewHandlerWithReplayer(d Deps) (http.Handler, Replayer) {
	h, r := newHandlers(d)
	return r, h
}

// Replay implements Replayer.
func (h *handlers) Replay(r *http.Request, operationID string, path map[string]string, query url.Values, body json.RawMessage) ReplayResult {
	res, err := h.replay(context.WithValue(r.Context(), requestKey{}, r), replayCall{OperationID: operationID, PathParams: path, Query: query, Body: body})
	if err != nil {
		var aerr apiError
		if errors.As(err, &aerr) {
			return ReplayResult{Status: aerr.status, Code: aerr.code, Message: aerr.msg}
		}
		slog.Error("mcp replay", "operation", operationID, "err", err)
		return ReplayResult{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
	}
	return ReplayResult(res)
}

// Authenticate implements Replayer.
func (h *handlers) Authenticate(r *http.Request) (auth.Principal, error) {
	return h.d.Auth.Authenticate(r.Context(), r)
}
