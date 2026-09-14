package api_test

import (
	"net/http"
	"testing"
)

// Every M6 operation is routed, authenticated and answers (a 501 stub until its task lands, never a 404).
func TestM6OperationsAreRoutedAndAuthenticated(t *testing.T) {
	e := newAPI(t)
	anon := e.client(t)
	paths := []struct{ method, path string }{
		{"GET", "/dashboard/series?range=1h"}, {"GET", "/dashboard/top?range=1h"}, {"GET", "/dashboard/health"},
		{"GET", "/engines/00000000-0000-0000-0000-00000000abcd/metrics?window=5m"},
		{"GET", "/engines/00000000-0000-0000-0000-00000000abcd/logs"},
		{"PUT", "/auth/me"}, {"POST", "/auth/me/password"}, {"GET", "/version"},
	}
	for _, p := range paths {
		if code := anon.do(p.method, p.path, map[string]any{}, nil); code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session -> %d, want 401", p.method, p.path, code)
		}
	}
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	for _, p := range paths {
		if code := admin.do(p.method, p.path, map[string]any{}, nil); code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Fatalf("%s %s is not routed (%d)", p.method, p.path, code)
		}
	}
}
