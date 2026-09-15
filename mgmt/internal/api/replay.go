package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	apispec "github.com/piwi3910/nexora/mgmt/api"
)

// replayHeader marks an in-process replayed request; such a request cannot replay again.
const replayHeader = "X-Nexora-Replay"

// replayCall is one API operation to run in-process with the original caller's credentials.
type replayCall struct {
	OperationID string
	PathParams  map[string]string
	Query       url.Values
	Body        json.RawMessage
}

// replayResult is the replayed response.
type replayResult struct {
	Status        int
	Body          json.RawMessage
	Code, Message string // from the Error body when Status >= 400
}

// replay serves c through the full router as a request carrying only the Cookie and Authorization
// headers of the original request (requestFrom(ctx)), so authentication, RBAC, validation, revision
// checks, snapshot publishing and audit are exactly those of a direct call by the same caller.
func (h *handlers) replay(ctx context.Context, c replayCall) (replayResult, error) {
	orig := requestFrom(ctx)
	if orig == nil {
		return replayResult{}, errors.New("replay: no original request in context")
	}
	if orig.Header.Get(replayHeader) != "" {
		return replayResult{}, apiError{status: http.StatusBadRequest, code: "invalid_request", msg: "a replayed request cannot replay API operations"}
	}
	op, ok := apispec.Operations()[c.OperationID]
	if !ok {
		return replayResult{}, fmt.Errorf("replay: unknown operation %s", c.OperationID)
	}
	path := op.Path
	for _, p := range op.Params {
		if p.In != "path" {
			continue
		}
		v, ok := c.PathParams[p.Name]
		if !ok {
			return replayResult{}, fmt.Errorf("replay %s: missing path parameter %s", c.OperationID, p.Name)
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(v))
	}
	target := "/api/v1" + path
	if len(c.Query) > 0 {
		target += "?" + c.Query.Encode()
	}
	var body io.Reader
	if len(c.Body) > 0 && op.Method != http.MethodGet {
		body = bytes.NewReader(c.Body)
	}
	// A client disconnect mid-apply must not cut a replay short, and the router must route the replayed
	// request afresh rather than reuse the original request's chi routing state.
	rctx := context.WithValue(context.WithoutCancel(ctx), chi.RouteCtxKey, nil)
	req, err := http.NewRequestWithContext(rctx, op.Method, target, body)
	if err != nil {
		return replayResult{}, fmt.Errorf("replay %s: %w", c.OperationID, err)
	}
	for _, name := range []string{"Cookie", "Authorization"} {
		for _, v := range orig.Header.Values(name) {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(replayHeader, "1")
	req.RemoteAddr = orig.RemoteAddr
	rec := httptest.NewRecorder()
	h.root.ServeHTTP(rec, req)
	res := replayResult{Status: rec.Code, Body: rec.Body.Bytes()}
	if res.Status >= 400 {
		var e Error
		if json.Unmarshal(res.Body, &e) == nil {
			res.Code, res.Message = e.Code, e.Message
		}
	}
	return res, nil
}
