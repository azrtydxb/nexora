package api_test

import (
	"fmt"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/catzone"
)

type catServer struct {
	env                     *apiEnv
	admin, operator, viewer zmdClient
}

// catNewServer serves the API with the catalog zone service wired as nexora-mgmt wires it.
func catNewServer(t *testing.T) *catServer {
	t.Helper()
	e := newAPIWith(t, func(d *api.Deps) {
		cat := &catzone.Service{Store: d.Store, Zones: d.Zones, Build: d.Build}
		d.Zones.CatalogChanged = cat.Regenerate
		d.CatalogZones = api.NewCatalogZoneService(cat)
	})
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	login := func(name, role string) zmdClient {
		if code := admin.do("POST", "/users", map[string]any{"username": name, "email": name + "@x", "password": name + "-password-1", "role": role}, nil); code != 201 {
			t.Fatalf("create %s -> %d", role, code)
		}
		c := e.client(t)
		if code := c.do("POST", "/auth/login", map[string]string{"username": name, "password": name + "-password-1"}, nil); code != 200 {
			t.Fatalf("login %s -> %d", role, code)
		}
		return zmdClient{c}
	}
	return &catServer{env: e, admin: zmdClient{admin}, operator: login("opal", "operator"), viewer: login("vic", "viewer")}
}

func (s *catServer) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := s.env.st.Pool.QueryRow(s.env.ctx, "SELECT count(*) FROM audit_log WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCatalogZonesAPI(t *testing.T) {
	s := catNewServer(t)
	if code := s.operator.status(t, "POST", "/catalog-zones", `{"name":"catalog.test.","role":"producer"}`); code != 400 {
		t.Fatalf("producer without transfer CIDRs: %d", code)
	}
	if code := s.operator.status(t, "POST", "/catalog-zones", `{"name":"cat.remote.","role":"consumer"}`); code != 400 {
		t.Fatalf("consumer without primaries: %d", code)
	}
	if code := s.viewer.status(t, "POST", "/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`); code != 403 {
		t.Fatalf("viewer create: %d", code)
	}
	prod := s.operator.expect(t, 201, "POST", "/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`)
	cons := s.operator.expect(t, 201, "POST", "/catalog-zones", `{"name":"cat.remote.","role":"consumer","primaries":[{"address":"127.0.0.1:5399"}]}`)
	if prod["role"] != "producer" || cons["role"] != "consumer" || prod["zone_id"] == nil {
		t.Fatalf("create: %v %v", prod, cons)
	}
	if code := s.operator.status(t, "POST", "/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`); code != 409 {
		t.Fatalf("duplicate catalog name: %d", code)
	}
	z := s.admin.createZone(t, `{"name":"m.test.","kind":"primary","soa":{"mname":"ns.m.test.","rname":"h.m.test."},"nameservers":["ns.m.test."],"catalog_zone_id":"`+prod["id"].(string)+`"}`)
	got := s.viewer.expect(t, 200, "GET", "/catalog-zones/"+prod["id"].(string), "")
	members := got["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("members %v", members)
	}
	m := members[0].(map[string]any)
	if m["name"] != "m.test." || m["zone_id"] != z["id"] || m["state"] != "configured" || m["label"] == "" {
		t.Fatalf("member %v", m)
	}
	var list []map[string]any
	if code := s.viewer.raw(t, "GET", "/catalog-zones", "", &list); code != 200 || len(list) != 2 {
		t.Fatalf("list %d %v", code, list)
	}
	if code := s.viewer.status(t, "DELETE", "/catalog-zones/"+cons["id"].(string), ""); code != 403 {
		t.Fatalf("viewer delete: %d", code)
	}
	s.operator.expect(t, 204, "DELETE", "/catalog-zones/"+cons["id"].(string), "")
	if code := s.viewer.status(t, "GET", "/catalog-zones/"+cons["id"].(string), ""); code != 404 {
		t.Fatalf("deleted catalog: %d", code)
	}
	for _, action := range []string{"createCatalogZone", "deleteCatalogZone"} {
		if s.auditCount(t, action) == 0 {
			t.Fatalf("no audit row for %s", action)
		}
	}
}

// A zone file import would replace the records the catalog service generates for a producer.
func TestImportRefusesProducerCatalog(t *testing.T) {
	s := catNewServer(t)
	prod := s.operator.expect(t, 201, "POST", "/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`)
	zoneID := prod["zone_id"].(string)
	before := s.admin.expect(t, 200, "GET", "/zones/"+zoneID, "")
	body := `{"revision":` + fmt.Sprint(before["revision"]) + `,"content":"$ORIGIN catalog.test.\n@ 0 IN SOA invalid. hostmaster.invalid. 99 3600 600 86400 0\n@ 0 IN NS invalid.\nx 0 IN TXT \"x\"\n"}`
	var out map[string]any
	if code := s.admin.raw(t, "POST", "/zones/"+zoneID+"/import", body, &out); code != 409 || out["code"] != "catalog_managed" {
		t.Fatalf("import into a producer catalog: %d %v", code, out)
	}
	if after := s.admin.expect(t, 200, "GET", "/zones/"+zoneID, ""); after["revision"] != before["revision"] {
		t.Fatalf("refused import changed the zone: %v -> %v", before["revision"], after["revision"])
	}
}
