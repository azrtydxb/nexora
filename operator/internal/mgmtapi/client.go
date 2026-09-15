// Package mgmtapi is the operator's client of the Nexora management API. The models and the raw client
// are generated from mgmt/api/openapi.yaml (client.gen.go); this file adds bearer authentication and
// maps error responses onto sentinel errors the controllers branch on.
package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors matched by errors.Is on an *APIError or a transport failure.
var (
	ErrUnauthorized = errors.New("management API: unauthorized")
	ErrNotFound     = errors.New("management API: not found")
	ErrConflict     = errors.New("management API: conflict")
	ErrUnavailable  = errors.New("management API: unavailable")
)

// defaultTimeout bounds a request when the caller passes no http.Client.
const defaultTimeout = 15 * time.Second

// APIError is a non-2xx answer of the management API.
type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("management API status %d (%s): %s", e.Status, e.Code, e.Message)
}

// Is maps 401, 404, 409 and 5xx onto ErrUnauthorized, ErrNotFound, ErrConflict and ErrUnavailable.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	case ErrUnavailable:
		return e.Status >= 500
	}
	return false
}

// Client calls the management API with a bearer token.
type Client struct {
	api *ClientWithResponses
}

// New returns a client for baseURL (scheme and host, without /api/v1). A nil hc uses a client with a
// 15 second timeout.
func New(baseURL, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("management API base URL %q: want http(s)://host[:port]", baseURL)
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	auth := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		// The management plane refuses any request other than GET and HEAD without this type, including
		// the bodiless DELETEs (revoke join token, delete engine group) the generated client sends bare.
		if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return nil
	}
	api, err := NewClientWithResponses(strings.TrimRight(baseURL, "/")+"/api/v1",
		WithHTTPClient(hc), WithRequestEditorFn(auth))
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

// response is what every generated …Response offers.
type response interface {
	StatusCode() int
	GetBody() []byte
}

// check turns a generated call's outcome into nil or an error: transport (and response decoding)
// failures wrap ErrUnavailable, non-2xx answers become *APIError. r is only used when err is nil.
func check(r response, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	status := r.StatusCode()
	if status >= 200 && status < 300 {
		return nil
	}
	out := &APIError{Status: status}
	var e Error
	if json.Unmarshal(r.GetBody(), &e) == nil && e.Code != "" {
		out.Code, out.Message = e.Code, e.Message
	} else {
		out.Message = http.StatusText(status)
	}
	return out
}

// errNoBody reports a 2xx answer without the expected JSON body.
func errNoBody(op string) error {
	return fmt.Errorf("%w: %s: response has no JSON body", ErrUnavailable, op)
}

// Health returns nil when the management plane answers 2xx on /health.
func (c *Client) Health(ctx context.Context) error {
	r, err := c.api.GetHealthWithResponse(ctx)
	return check(r, err)
}

// SetupRequired reports whether the installation still waits for its first administrator.
func (c *Client) SetupRequired(ctx context.Context) (bool, error) {
	r, err := c.api.GetSetupStatusWithResponse(ctx)
	if err := check(r, err); err != nil {
		return false, err
	}
	if r.JSON200 == nil {
		return false, errNoBody("getSetupStatus")
	}
	return r.JSON200.Required, nil
}

// EngineGroups lists every engine group.
func (c *Client) EngineGroups(ctx context.Context) ([]EngineGroup, error) {
	r, err := c.api.ListEngineGroupsWithResponse(ctx)
	if err := check(r, err); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, errNoBody("listEngineGroups")
	}
	return *r.JSON200, nil
}

// CreateEngineGroup creates an engine group.
func (c *Client) CreateEngineGroup(ctx context.Context, in EngineGroupInput) (EngineGroup, error) {
	r, err := c.api.CreateEngineGroupWithResponse(ctx, in)
	if err := check(r, err); err != nil {
		return EngineGroup{}, err
	}
	if r.JSON201 == nil {
		return EngineGroup{}, errNoBody("createEngineGroup")
	}
	return *r.JSON201, nil
}

// UpdateEngineGroup replaces an engine group's settings at in.Revision.
func (c *Client) UpdateEngineGroup(ctx context.Context, id uuid.UUID, in EngineGroupUpdate) (EngineGroup, error) {
	r, err := c.api.UpdateEngineGroupWithResponse(ctx, id, in)
	if err := check(r, err); err != nil {
		return EngineGroup{}, err
	}
	if r.JSON200 == nil {
		return EngineGroup{}, errNoBody("updateEngineGroup")
	}
	return *r.JSON200, nil
}

// DeleteEngineGroup deletes an engine group at revision.
func (c *Client) DeleteEngineGroup(ctx context.Context, id uuid.UUID, revision int64) error {
	r, err := c.api.DeleteEngineGroupWithResponse(ctx, id, &DeleteEngineGroupParams{Revision: revision})
	return check(r, err)
}

// JoinTokens lists every join token.
func (c *Client) JoinTokens(ctx context.Context) ([]JoinToken, error) {
	r, err := c.api.ListJoinTokensWithResponse(ctx)
	if err := check(r, err); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, errNoBody("listJoinTokens")
	}
	return *r.JSON200, nil
}

// CreateJoinToken creates a join token; the token value is only in the returned JoinTokenCreated.
func (c *Client) CreateJoinToken(ctx context.Context, in JoinTokenCreate) (JoinTokenCreated, error) {
	r, err := c.api.CreateJoinTokenWithResponse(ctx, in)
	if err := check(r, err); err != nil {
		return JoinTokenCreated{}, err
	}
	if r.JSON201 == nil {
		return JoinTokenCreated{}, errNoBody("createJoinToken")
	}
	return *r.JSON201, nil
}

// RevokeJoinToken revokes a join token.
func (c *Client) RevokeJoinToken(ctx context.Context, id uuid.UUID) error {
	r, err := c.api.RevokeJoinTokenWithResponse(ctx, id)
	return check(r, err)
}
