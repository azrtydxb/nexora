package api_test

import (
	"net/http"
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

func TestAccessControlSplitAPI(t *testing.T) {
	e := newAPI(t)
	c := e.client(t)
	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
		t.Fatalf("setup -> %d", code)
	}
	var ac struct {
		AllowCidrs []string `json:"allow_cidrs"`
		AuthCidrs  []string `json:"authoritative_allow_cidrs"`
		Revision   int64    `json:"revision"`
	}
	c.do("GET", "/access-control", nil, &ac)
	if len(ac.AuthCidrs) != 2 || ac.AuthCidrs[0] != "0.0.0.0/0" {
		t.Fatalf("default authoritative access: %+v", ac)
	}
	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "authoritative_allow_cidrs": []string{"192.168.0.0/16"}, "revision": ac.Revision}, &ac); code != http.StatusOK {
		t.Fatalf("update -> %d", code)
	}
	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "revision": ac.Revision}, &ac); code != http.StatusOK || len(ac.AuthCidrs) != 1 {
		t.Fatalf("omitting authoritative_allow_cidrs keeps it: %d %+v", code, ac)
	}
	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "authoritative_allow_cidrs": []string{"nope"}, "revision": ac.Revision}, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid cidr -> %d", code)
	}
	var z struct {
		ID       string   `json:"id"`
		Revision int64    `json:"revision"`
		Allow    []string `json:"allow_query_cidrs"`
		Update   struct {
			AllowCidrs []string `json:"allow_cidrs"`
		} `json:"update"`
	}
	if code := c.do("POST", "/zones", map[string]any{"name": "kw.test.", "kind": "primary", "nameservers": []string{"ns1.kw.test."},
		"soa": map[string]any{"mname": "ns1.kw.test.", "rname": "hostmaster.kw.test."}, "allow_query_cidrs": []string{"10.0.0.0/8"},
		"update": map[string]any{"tsig_key_ids": []string{}, "allow_cidrs": []string{"127.0.0.1/32"}}}, &z); code != http.StatusCreated {
		t.Fatalf("create zone -> %d", code)
	}
	if len(z.Allow) != 1 || len(z.Update.AllowCidrs) != 1 || z.Update.AllowCidrs[0] != "127.0.0.1/32" {
		t.Fatalf("zone allow_query_cidrs/update.allow_cidrs: %+v", z)
	}
	if code := c.do("PATCH", "/zones/"+z.ID, map[string]any{"revision": z.Revision, "allow_query_cidrs": []string{"bad"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid zone cidr -> %d", code)
	}
	_, snap, err := snapshot.Latest(e.ctx, e.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.AuthoritativeAclSet || len(snap.AuthoritativeAllowCidrs) != 1 || snap.AuthoritativeAllowCidrs[0] != "192.168.0.0/16" {
		t.Fatalf("snapshot authoritative ACL: %v %v", snap.AuthoritativeAclSet, snap.AuthoritativeAllowCidrs)
	}
	var hosted *controlv1.AuthZone
	for _, az := range snap.AuthZones {
		if az.Name == "kw.test." {
			hosted = az
		}
	}
	if hosted == nil || len(hosted.AllowQueryCidrs) != 1 || hosted.AllowQueryCidrs[0] != "10.0.0.0/8" || len(hosted.UpdateAllowCidrs) != 1 || hosted.UpdateAllowCidrs[0] != "127.0.0.1/32" {
		t.Fatalf("snapshot zone: %+v", hosted)
	}
	var audit []struct {
		Action string `json:"action"`
	}
	c.do("GET", "/audit", nil, &audit)
	found := false
	for _, ev := range audit {
		found = found || ev.Action == "updateAccessControl"
	}
	if !found {
		t.Fatalf("no updateAccessControl audit row: %+v", audit)
	}
}
