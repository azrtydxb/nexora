package api_test

import (
	"net/http"
	"strconv"
	"testing"
)

func TestSystemUserIsReadOnlyInAPI(t *testing.T) {
	e := newAPI(t)
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	if _, err := e.svc.EnsureBootstrapToken(e.ctx, "nxt_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"); err != nil {
		t.Fatal(err)
	}
	var users []map[string]any
	if code := admin.do("GET", "/users", nil, &users); code != 200 {
		t.Fatalf("list users -> %d", code)
	}
	var sysID, adminID string
	var sysRev, adminRev float64
	for _, u := range users {
		switch u["username"] {
		case "nexora-operator":
			sysID, sysRev = u["id"].(string), u["revision"].(float64)
			if u["source"] != "system" {
				t.Fatalf("system user source = %v", u["source"])
			}
		case "admin":
			adminID, adminRev = u["id"].(string), u["revision"].(float64)
		}
	}
	if sysID == "" || adminID == "" {
		t.Fatalf("users = %v", users)
	}
	var apiErr map[string]string
	if code := admin.do("PUT", "/users/"+sysID, map[string]any{"role": "viewer", "email": "", "disabled": true, "revision": sysRev}, &apiErr); code != http.StatusConflict || apiErr["code"] != "system_user" {
		t.Fatalf("update system user -> %d %v", code, apiErr)
	}
	if code := admin.do("DELETE", "/users/"+sysID+"?revision=1", nil, &apiErr); code != http.StatusConflict || apiErr["code"] != "system_user" {
		t.Fatalf("delete system user -> %d %v", code, apiErr)
	}
	anon := e.client(t)
	if code := anon.do("POST", "/auth/login", map[string]string{"username": "nexora-operator", "password": ""}, nil); code != http.StatusUnauthorized {
		t.Fatalf("system login -> %d", code)
	}
	if code := admin.do("DELETE", "/users/"+adminID+"?revision="+formatRev(adminRev), nil, &apiErr); code != http.StatusConflict {
		t.Fatalf("deleting the last human admin -> %d %v", code, apiErr)
	}
}

func formatRev(f float64) string { return strconv.FormatInt(int64(f), 10) }
