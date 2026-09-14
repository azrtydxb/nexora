package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAccountSelfService(t *testing.T) {
	e := newAPI(t)
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	if code := admin.do("POST", "/users", map[string]any{"username": "vera", "email": "v@example.test", "password": "viewer-password-1", "role": "viewer"}, nil); code != http.StatusCreated {
		t.Fatalf("create viewer -> %d", code)
	}
	login := func(pw string) (*client, int) {
		c := e.client(t)
		return c, c.do("POST", "/auth/login", map[string]any{"username": "vera", "password": pw}, nil)
	}
	vera, code := login("viewer-password-1")
	if code != http.StatusOK {
		t.Fatalf("login -> %d", code)
	}
	other, _ := login("viewer-password-1")
	type me struct {
		Role, Email, DisplayName string
		Disabled                 bool
		Revision                 int64
		Preferences              map[string]any
		LastLoginAt              *string `json:"last_login_at"`
	}
	var u me
	vera.do("GET", "/auth/me", nil, &u)
	if u.LastLoginAt == nil {
		t.Fatal("last_login_at not set by login")
	}
	body := map[string]any{"revision": u.Revision, "email": "vera@example.test", "display_name": "Vera",
		"preferences": map[string]any{"theme": "dark", "time_zone": "Europe/Brussels", "clock_24h": true, "querylog_live": false},
		"role":        "admin", "disabled": true, "username": "root"}
	if code := vera.do("PUT", "/auth/me", body, &u); code != http.StatusOK {
		t.Fatalf("update profile -> %d", code)
	}
	if u.Role != "viewer" || u.Disabled || u.Email != "vera@example.test" || u.Preferences["time_zone"] != "Europe/Brussels" {
		t.Fatalf("self-service changed privileged fields or missed allowed ones: %+v", u)
	}
	if code := vera.do("PUT", "/auth/me", map[string]any{"revision": 1, "email": "x@example.test"}, nil); code != http.StatusConflict {
		t.Fatalf("stale revision -> %d", code)
	}
	pw := func(c *client, cur, next string) int {
		return c.do("POST", "/auth/me/password", map[string]any{"current_password": cur, "new_password": next, "revoke_other_sessions": true}, nil)
	}
	if code := pw(vera, "wrong-password-xx", "viewer-password-2"); code != http.StatusForbidden {
		t.Fatalf("wrong current -> %d", code)
	}
	if code := pw(vera, "viewer-password-1", "short"); code != http.StatusBadRequest {
		t.Fatalf("short -> %d", code)
	}
	if code := pw(vera, "viewer-password-1", "viewer-password-1"); code != http.StatusBadRequest {
		t.Fatalf("unchanged -> %d", code)
	}
	if code := pw(vera, "viewer-password-1", "viewer-password-2"); code != http.StatusNoContent {
		t.Fatalf("change -> %d", code)
	}
	if code := vera.do("GET", "/auth/me", nil, nil); code != http.StatusOK {
		t.Fatalf("current session revoked: %d", code)
	}
	if code := other.do("GET", "/auth/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("other session kept: %d", code)
	}
	if _, code := login("viewer-password-2"); code != http.StatusOK {
		t.Fatalf("new password login -> %d", code)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if code := admin.do("POST", "/api-tokens", map[string]any{"name": "t1", "role": "viewer"}, &tok); code != http.StatusCreated {
		t.Fatalf("token -> %d", code)
	}
	bearer := e.client(t)
	bearer.hc.Transport = bearerTransport(tok.Token)
	if code := bearer.do("POST", "/auth/me/password", map[string]any{"current_password": "admin-password-1", "new_password": "admin-password-2"}, nil); code != http.StatusForbidden {
		t.Fatalf("api token password change -> %d", code)
	}
	for i := 0; i < 11; i++ {
		login("not-the-password-" + strings.Repeat("x", i))
	}
	if _, code := login("viewer-password-2"); code != http.StatusTooManyRequests {
		t.Fatalf("12th attempt after 11 failures -> %d, want 429", code)
	}
	var rows int
	if err := e.st.Pool.QueryRow(e.ctx, `select count(*) from audit_log where diff::text ilike '%password-%'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("audit rows mention a password: %d %v", rows, err)
	}
	var actions int
	if err := e.st.Pool.QueryRow(e.ctx, `select count(*) from audit_log where action in ('updateCurrentUser','changeOwnPassword')`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("audit actions: %d %v", actions, err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `update users set source = 'oidc', password_hash = null where username = 'admin'`); err != nil {
		t.Fatal(err)
	}
	if code := pw(admin, "admin-password-1", "admin-password-2"); code != http.StatusConflict {
		t.Fatalf("oidc password change -> %d", code)
	}
	if code := admin.do("PUT", "/auth/me", map[string]any{"revision": 1, "email": "changed@example.test"}, nil); code != http.StatusConflict {
		t.Fatalf("oidc email change -> %d", code)
	}
}

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}
