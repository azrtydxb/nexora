package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/catalog"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

type recordingBackend struct {
	got  querylog.Query
	page querylog.Page
}

func (r *recordingBackend) Name() string { return "recording" }
func (r *recordingBackend) Search(_ context.Context, q querylog.Query) (querylog.Page, error) {
	r.got = q
	return r.page, nil
}

func TestSearchQueryLogRepeatedParameters(t *testing.T) {
	rb := &recordingBackend{}
	e := newAPIWith(t, func(d *api.Deps) { d.QueryLog = rb })
	c := e.client(t)
	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	if code := c.do("GET", "/query-log?qtype=A&qtype=AAAA&rcode=NXDOMAIN&source=allowlist&policy_group=global&engine_id=e1&name=You", nil, nil); code != http.StatusOK {
		t.Fatalf("repeated -> %d", code)
	}
	if len(rb.got.QTypes) != 2 || rb.got.RCodes[0] != "NXDOMAIN" || rb.got.Sources[0] != "allowlist" || rb.got.PolicyGroups[0] != "global" || rb.got.EngineIDs[0] != "e1" || rb.got.Name != "You" {
		t.Fatalf("query: %+v", rb.got)
	}
	if code := c.do("GET", "/query-log?qtype=MX", nil, nil); code != http.StatusOK || len(rb.got.QTypes) != 1 {
		t.Fatalf("single value -> %d %+v", code, rb.got)
	}
	many := "/query-log?"
	for i := 0; i < 33; i++ {
		many += "qtype=A&"
	}
	if code := c.do("GET", many, nil, nil); code != http.StatusBadRequest {
		t.Fatalf("33 values -> %d, want 400", code)
	}
	if code := c.do("GET", "/query-log?source=bogus", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown source -> %d, want 400", code)
	}
}

func TestSearchQueryLogResolvesNames(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	src, ok := cat.Source("gambling", "hagezi-gambling")
	if !ok {
		t.Fatal("catalog source gambling/hagezi-gambling missing")
	}
	rb := &recordingBackend{}
	e := newAPIWith(t, func(d *api.Deps) { d.QueryLog = rb; d.Catalog = cat })
	c := e.client(t)
	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	var custom, catalogList, group, rpz string
	for _, l := range []struct {
		name string
		id   *string
	}{{"custom-attr", &custom}, {catalog.ListName("gambling", "hagezi-gambling"), &catalogList}} {
		if err := e.st.Pool.QueryRow(e.ctx, "insert into filter_lists (name, kind, url) values ($1, 'block', 'https://lists.example.test/x') returning id::text", l.name).Scan(l.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.Pool.QueryRow(e.ctx, "insert into policy_groups (name) values ('kids') returning id::text").Scan(&group); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Pool.QueryRow(e.ctx, "insert into rpz_zones (name, position, source_type, primary_address) values ('rpz.example.test', 1, 'transfer', '192.0.2.9:53') returning id::text").Scan(&rpz); err != nil {
		t.Fatal(err)
	}
	for _, rw := range []struct{ group, name, typ, value string }{
		{"", "rw.example.test", "A", "192.0.2.55"}, {"", "rw.example.test", "AAAA", "2001:db8::55"},
		{group, "rw.example.test", "A", "192.0.2.66"}, {"", "*.wild.example.test", "CNAME", "target.example.test"},
	} {
		if _, err := e.st.Pool.Exec(e.ctx, "insert into rewrites (group_id, name, type, value) values (nullif($1, '')::uuid, $2, $3, $4)", rw.group, rw.name, rw.typ, rw.value); err != nil {
			t.Fatal(err)
		}
	}
	rb.page = querylog.Page{Records: []querylog.Record{
		{Name: "www.custom.test.", Source: "blocklist", ListID: custom, Rule: "custom.test"},
		{Name: "casino.test.", Source: "category", ListID: catalogList, Category: "gambling"},
		{Name: "ok.test.", Source: "allowlist", ListID: "allowlist", Rule: "ok.test"},
		{Name: "grp.test.", Source: "allowlist", ListID: "group-allow:abc", PolicyGroupID: group},
		{Name: "rw.example.test.", Source: "rewrite", Rule: "rw.example.test"},
		{Name: "rw.example.test.", Source: "rewrite", Rule: "rw.example.test", PolicyGroupID: group},
		{Name: "a.wild.example.test.", Source: "rewrite", Rule: "*.wild.example.test", PolicyGroupID: group},
		{Name: "bad.test.", Source: "rpz", RPZZoneID: rpz, RPZAction: "nxdomain"},
		{Name: "plain.test."},
	}}
	var page struct {
		Records []struct {
			ListName        string `json:"list_name"`
			PolicyGroupName string `json:"policy_group_name"`
			RpzZoneName     string `json:"rpz_zone_name"`
			RewriteAnswer   string `json:"rewrite_answer"`
			Source          string `json:"source"`
		} `json:"records"`
	}
	if code := c.do("GET", "/query-log", nil, &page); code != http.StatusOK || len(page.Records) != 9 {
		t.Fatalf("search -> %d %+v", code, page)
	}
	r := page.Records
	for i, want := range []struct{ got, want string }{
		{r[0].ListName, "custom-attr"}, {r[1].ListName, src.Name}, {r[2].ListName, "Global allowlist"},
		{r[3].ListName, "kids allowlist"}, {r[3].PolicyGroupName, "kids"},
		{r[4].RewriteAnswer, "A 192.0.2.55"}, {r[5].RewriteAnswer, "A 192.0.2.66"}, {r[6].RewriteAnswer, "CNAME target.example.test"},
		{r[7].RpzZoneName, "rpz.example.test"}, {r[8].ListName + r[8].PolicyGroupName + r[8].RewriteAnswer + r[8].Source, ""},
	} {
		if want.got != want.want {
			t.Errorf("check %d: got %q, want %q", i, want.got, want.want)
		}
	}
}
