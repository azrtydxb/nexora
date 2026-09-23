package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

// Every M8 operation exists with its role; without a service it answers 501, never 404.
func TestM8OperationsDeclared(t *testing.T) {
	want := map[string]auth.Role{
		"listCatalogZones": auth.RoleViewer, "createCatalogZone": auth.RoleOperator,
		"getCatalogZone": auth.RoleViewer, "deleteCatalogZone": auth.RoleOperator,
		"getOdohSettings": auth.RoleViewer, "updateOdohSettings": auth.RoleOperator,
		"rotateOdohKey": auth.RoleAdmin,
	}
	for op, role := range want {
		if got, ok := auth.Permissions[op]; !ok || got != role {
			t.Fatalf("%s: role %v, want %v", op, got, role)
		}
	}
	srv := newM8TestServer(t) // admin session, Deps without CatalogZones and ODoH
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/catalog-zones", ""},
		{http.MethodPost, "/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`},
		{http.MethodGet, "/catalog-zones/00000000-0000-0000-0000-0000000000aa", ""},
		{http.MethodDelete, "/catalog-zones/00000000-0000-0000-0000-0000000000aa", ""},
		{http.MethodGet, "/odoh", ""},
		{http.MethodPut, "/odoh", `{"target_enabled":false,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`},
		{http.MethodPost, "/odoh/rotate-key", ""},
	} {
		if code := srv.do(t, c.method, c.path, c.body); code != http.StatusNotImplemented {
			t.Fatalf("%s %s: %d, want 501", c.method, c.path, code)
		}
	}
}

// m8Server is an API server with an admin session and no M8 services.
type m8Server struct{ c *client }

func newM8TestServer(t *testing.T) *m8Server {
	e := newAPI(t)
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	return &m8Server{c: admin}
}

// do sends body (raw JSON; "" sends none) to path under /api/v1 and returns the status.
func (s *m8Server) do(t *testing.T, method, path, body string) int {
	t.Helper()
	var b any
	if body != "" {
		b = json.RawMessage(body)
	}
	return s.c.do(method, path, b, nil)
}
