package api_test

import (
	"net/http"
	"testing"
)

// roleClients sets up an admin and returns logged-in operator and viewer clients (M1 api_test.go helpers).
func roleClients(t *testing.T) (*client, *client) {
	t.Helper()
	e := newAPI(t)
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	login := func(name, role string) *client {
		if code := admin.do("POST", "/users", map[string]any{"username": name, "email": name + "@x", "password": name + "-password-1", "role": role}, nil); code != 201 {
			t.Fatalf("create %s -> %d", role, code)
		}
		c := e.client(t)
		if code := c.do("POST", "/auth/login", map[string]string{"username": name, "password": name + "-password-1"}, nil); code != 200 {
			t.Fatalf("login %s -> %d", role, code)
		}
		return c
	}
	return login("opal", "operator"), login("vic", "viewer")
}

type group struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	CIDRs      []string `json:"cidrs"`
	Allowlist  []string `json:"allowlist"`
	Revision   int64    `json:"revision"`
	SafeSearch struct {
		Google  bool   `json:"google"`
		YouTube string `json:"youtube"`
	} `json:"safe_search"`
}

type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func TestPolicyGroupAPI(t *testing.T) {
	op, viewer := roleClients(t)
	body := map[string]any{
		"name": "kids", "cidrs": []string{"192.168.50.0/24"},
		"allowlist":   []string{"School.Example."},
		"safe_search": map[string]any{"google": true, "bing": false, "duckduckgo": false, "youtube": "strict"},
	}
	var g group
	if code := op.do(http.MethodPost, "/policy-groups", body, &g); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if g.Allowlist[0] != "school.example" || g.Revision != 1 || !g.SafeSearch.Google {
		t.Fatalf("created = %+v", g)
	}
	var e apiErr
	if code := viewer.do(http.MethodPost, "/policy-groups", body, &e); code != http.StatusForbidden {
		t.Fatalf("viewer create = %d", code)
	}
	body["name"] = "dup"
	if code := op.do(http.MethodPost, "/policy-groups", body, &e); code != http.StatusConflict || e.Code != "cidr_in_use" {
		t.Fatalf("duplicate cidr = %d %+v", code, e)
	}
	body["cidrs"] = []string{"192.168.60.1/24"}
	if code := op.do(http.MethodPost, "/policy-groups", body, &e); code != http.StatusUnprocessableEntity || e.Message != "cidr 192.168.60.1/24 has host bits set" {
		t.Fatalf("host bits = %d %+v", code, e)
	}
	upd := map[string]any{"name": "kids", "cidrs": []string{"192.168.50.0/24", "fd00:50::/64"}, "revision": g.Revision,
		"safe_search": map[string]any{"google": true, "bing": true, "duckduckgo": false, "youtube": "moderate"}}
	var g2 group
	if code := op.do(http.MethodPut, "/policy-groups/"+g.ID, upd, &g2); code != http.StatusOK || g2.Revision != 2 {
		t.Fatalf("update = %d %+v", code, g2)
	}
	if code := op.do(http.MethodPut, "/policy-groups/"+g.ID, upd, &e); code != http.StatusConflict || e.Code != "conflict" {
		t.Fatalf("stale update = %d %+v", code, e)
	}
	var list []group
	if code := viewer.do(http.MethodGet, "/policy-groups", nil, &list); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list = %d %d", code, len(list))
	}
	if code := op.do(http.MethodDelete, "/policy-groups/"+g.ID+"?revision=1", nil, &e); code != http.StatusConflict {
		t.Fatalf("stale delete = %d", code)
	}
	if code := op.do(http.MethodDelete, "/policy-groups/"+g.ID+"?revision=2", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
}

func TestRewriteAPIValidation(t *testing.T) {
	op, _ := roleClients(t)
	var e apiErr
	cases := []struct {
		body map[string]any
		code int
		msg  string
	}{
		{map[string]any{"name": "nas.home.test", "type": "A", "value": "fd00::1"}, 422, "value fd00::1 is not an IPv4 address"},
		{map[string]any{"name": "nas.home.test", "type": "AAAA", "value": "::ffff:1.2.3.4"}, 422, "value ::ffff:1.2.3.4 is not an IPv6 address"},
		{map[string]any{"name": "a.*.test", "type": "A", "value": "1.2.3.4"}, 422, "name a.*.test is not a domain or *.domain"},
		{map[string]any{"name": "loop.test", "type": "CNAME", "value": "loop.test"}, 422, "CNAME target must differ from name"},
		{map[string]any{"name": "x.test", "type": "A", "value": "1.2.3.4", "ttl": 90000}, 422, "ttl must be between 0 and 86400"},
	}
	for _, c := range cases {
		e = apiErr{}
		if code := op.do(http.MethodPost, "/rewrites", c.body, &e); code != c.code || e.Message != c.msg {
			t.Errorf("%v: got %d %q, want %d %q", c.body, code, e.Message, c.code, c.msg)
		}
	}
	var created struct {
		ID  string `json:"id"`
		TTL int    `json:"ttl"`
	}
	if code := op.do(http.MethodPost, "/rewrites", map[string]any{"name": "NAS.home.test.", "type": "A", "value": "192.168.1.50"}, &created); code != 201 || created.TTL != 300 {
		t.Fatalf("create = %d %+v", code, created)
	}
	if code := op.do(http.MethodPost, "/rewrites", map[string]any{"name": "nas.home.test", "type": "CNAME", "value": "other.test"}, &e); code != 409 || e.Code != "rewrite_conflict" {
		t.Fatalf("cname conflict = %d %+v", code, e)
	}
	var list []map[string]any
	if code := op.do(http.MethodGet, "/rewrites?scope=global", nil, &list); code != 200 || len(list) != 1 || list[0]["name"] != "nas.home.test" {
		t.Fatalf("list = %d %v", code, list)
	}
	if code := op.do(http.MethodGet, "/rewrites?scope=bogus", nil, &e); code != 422 {
		t.Fatalf("bad scope = %d", code)
	}
}

func TestDnsTlsStatusWithoutCertificate(t *testing.T) {
	_, viewer := roleClients(t)
	var out map[string]any
	if code := viewer.do("GET", "/settings/dns-tls", nil, &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out["configured"] != false || out["certificate"] != nil {
		t.Fatalf("out = %v", out)
	}
	if engines, ok := out["engines"].([]any); !ok || len(engines) != 0 {
		t.Fatalf("engines = %v", out["engines"])
	}
}
