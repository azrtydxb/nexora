package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

type apiEnv struct {
	srv   *httptest.Server
	st    *store.Store
	svc   *auth.Service
	pg    *harness.Postgres
	ctx   context.Context
	setup string
}

func newAPI(t *testing.T) *apiEnv { return newAPIWith(t, nil) }

// newAPIWith is newAPI with a hook that adjusts the handler dependencies.
func newAPIWith(t *testing.T, adjust func(*api.Deps)) *apiEnv {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_ = pki.InitCA(dir)
	ca, _ := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(st, false)
	tok, _, err := svc.EnsureSetupToken(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	deps := api.Deps{Store: st, Auth: svc, OIDC: auth.NewOIDC(auth.DisabledOIDC(), "http://x", st), CA: ca, QueryLog: querylog.Noop{}, InstanceID: "test", PublicURL: "http://x", Zones: &zone.Service{Store: st}}
	if adjust != nil {
		adjust(&deps)
	}
	h := api.NewHandler(deps)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &apiEnv{srv: srv, st: st, svc: svc, pg: pg, ctx: ctx, setup: tok}
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func (e *apiEnv) client(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: e.srv.URL + "/api/v1", hc: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

func TestPermissionsCoverEveryOperation(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			seen[op.OperationID] = true
			_, perm := auth.Permissions[op.OperationID]
			if !perm && !auth.Public[op.OperationID] {
				t.Errorf("operation %s has no permission entry", op.OperationID)
			}
		}
	}
	for op := range auth.Permissions {
		if !seen[op] {
			t.Errorf("permission %s names no OpenAPI operation", op)
		}
	}
	for op := range auth.Public {
		if !seen[op] {
			t.Errorf("public %s names no OpenAPI operation", op)
		}
	}
}

func TestSetupCRUDConflictAuditAndRBAC(t *testing.T) {
	e := newAPI(t)
	admin := e.client(t)
	var status map[string]bool
	if admin.do("GET", "/setup", nil, &status); !status["required"] {
		t.Fatal("setup should be required")
	}
	if code := admin.do("GET", "/upstreams", nil, nil); code != 401 {
		t.Fatalf("unauthenticated list -> %d", code)
	}
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	var up map[string]any
	if code := admin.do("POST", "/upstreams", map[string]any{"name": "q9", "protocol": "udp", "address": "9.9.9.9:53", "timeout_ms": 250, "enabled": true, "position": 0}, &up); code != 201 {
		t.Fatalf("create upstream -> %d %v", code, up)
	}
	id := up["id"].(string)
	body := map[string]any{"name": "q9", "protocol": "udp", "address": "149.112.112.112:53", "timeout_ms": 300, "enabled": true, "position": 0, "revision": 1}
	if code := admin.do("PUT", "/upstreams/"+id, body, &up); code != 200 || up["revision"].(float64) != 2 {
		t.Fatalf("update -> %d %v", code, up)
	}
	var apiErr map[string]string
	if code := admin.do("PUT", "/upstreams/"+id, body, &apiErr); code != 409 || apiErr["code"] != "conflict" {
		t.Fatalf("stale revision -> %d %v", code, apiErr)
	}
	if code := admin.do("POST", "/upstreams", map[string]any{"name": "bad", "protocol": "doh", "doh_url": "http://x/dns-query", "timeout_ms": 250, "enabled": true, "position": 1}, &apiErr); code != 400 || apiErr["code"] != "invalid_request" {
		t.Fatalf("http DoH -> %d %v", code, apiErr)
	}

	var audit []map[string]any
	admin.do("GET", "/audit", nil, &audit)
	actions := map[string]bool{}
	for _, a := range audit {
		if a["actor_name"] == "admin" && a["diff"] != nil {
			actions[a["action"].(string)] = true
		}
	}
	if !actions["createUpstream"] || !actions["updateUpstream"] {
		t.Fatalf("audit missing changes: %v", actions)
	}
	var versions []map[string]any
	admin.do("GET", "/config-versions", nil, &versions)
	if len(versions) < 3 {
		t.Fatalf("config versions = %d", len(versions))
	}

	admin.do("POST", "/users", map[string]any{"username": "vic", "email": "v@x", "password": "viewer-password-1", "role": "viewer"}, nil)
	viewer := e.client(t)
	if code := viewer.do("POST", "/auth/login", map[string]string{"username": "vic", "password": "viewer-password-1"}, nil); code != 200 {
		t.Fatalf("viewer login -> %d", code)
	}
	if code := viewer.do("GET", "/upstreams", nil, nil); code != 200 {
		t.Fatalf("viewer read -> %d", code)
	}
	if code := viewer.do("POST", "/upstreams", map[string]any{"name": "v", "protocol": "udp", "address": "1.1.1.1:53", "timeout_ms": 250, "enabled": true, "position": 2}, &apiErr); code != 403 || apiErr["code"] != "forbidden" {
		t.Fatalf("viewer write -> %d %v", code, apiErr)
	}
	if code := viewer.do("GET", "/audit", nil, nil); code != 403 {
		t.Fatalf("viewer audit -> %d", code)
	}
	var jt map[string]any
	if code := admin.do("POST", "/join-tokens", map[string]any{"name": "fleet", "ttl_seconds": 3600}, &jt); code != 201 || len(jt["token"].(string)) < 10 {
		t.Fatalf("join token -> %d %v", code, jt)
	}
}

func TestCSRFAndDatabaseDown(t *testing.T) {
	e := newAPI(t)
	c := e.client(t)
	c.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil)
	req, _ := http.NewRequest("POST", c.base+"/upstreams", bytes.NewReader([]byte(`{"name":"x","protocol":"udp","address":"1.1.1.1:53","timeout_ms":250,"enabled":true,"position":0}`)))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := c.hc.Do(req)
	if resp.StatusCode != 415 {
		t.Fatalf("non-JSON mutation -> %d", resp.StatusCode)
	}
	if code := c.do("GET", "/health", nil, nil); code != 200 {
		t.Fatalf("health -> %d", code)
	}
	harness.StopPostgres(t, e.pg)
	var apiErr map[string]string
	if code := c.do("POST", "/upstreams", map[string]any{"name": "y", "protocol": "udp", "address": "1.1.1.1:53", "timeout_ms": 250, "enabled": true, "position": 0}, &apiErr); code != 503 || apiErr["code"] != "unavailable" {
		t.Fatalf("write with database down -> %d %v", code, apiErr)
	}
	var h map[string]string
	if code := c.do("GET", "/health", nil, &h); code != 503 || h["database"] != "unavailable" {
		t.Fatalf("health with database down -> %d %v", code, h)
	}
}
