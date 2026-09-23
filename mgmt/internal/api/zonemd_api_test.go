package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestZonemdAPI(t *testing.T) {
	s := zmdNewServer(t)
	p := s.admin.createZone(t, `{"name":"p.test.","kind":"primary","soa":{"mname":"ns.p.test.","rname":"h.p.test."},"nameservers":["ns.p.test."],"zonemd_generate":true}`)
	if p["zonemd_generate"] != true || p["zonemd_status"] != "not_checked" {
		t.Fatalf("create: %v", p)
	}
	if code := s.admin.status(t, "POST", "/zones", `{"name":"s.test.","kind":"secondary","primaries":[{"address":"127.0.0.1:53"}],"zonemd_generate":true}`); code != 400 {
		t.Fatalf("zonemd_generate on a secondary: %d", code)
	}
	sec := s.admin.createZone(t, `{"name":"s.test.","kind":"secondary","primaries":[{"address":"127.0.0.1:53"}],"zonemd_verify":"required"}`)
	if sec["zonemd_verify"] != "required" {
		t.Fatalf("secondary verify: %v", sec)
	}

	if code := s.viewer.status(t, "PATCH", "/zones/"+p["id"].(string), `{"revision":1,"zonemd_generate":false}`); code != 403 {
		t.Fatalf("viewer update: %d", code)
	}
	// Toggling generation rebuilds at once: the zone publishes a new version although no record changed.
	seq := s.currentSeq(t, p["id"].(string))
	if got := s.admin.updateZone(t, p["id"].(string), `{"revision":1,"zonemd_generate":false}`); got["zonemd_generate"] != false {
		t.Fatalf("toggle off: %v", got)
	}
	if next := s.currentSeq(t, p["id"].(string)); next != seq+1 {
		t.Fatalf("zonemd_generate toggle wrote seq %d -> %d, want a forced rebuild", seq, next)
	}

	rpz := s.admin.createRPZTransfer(t, "rpz.test.", "127.0.0.1:53", `"zonemd_verify":"required"`)
	snap := s.latestSnapshot(t)
	if src := zmdFindRPZ(snap, rpz["id"].(string)).GetTransfer(); src.GetZonemdVerify() != controlv1.ZonemdVerify_ZONEMD_VERIFY_REQUIRED {
		t.Fatalf("snapshot zonemd_verify %v", src.GetZonemdVerify())
	}
	if code := s.admin.status(t, "POST", "/rpz-zones", `{"name":"rpz.file.test.","source_type":"file","policy_override":"given","min_refresh_seconds":60,"zonemd_verify":"required"}`); code != 400 {
		t.Fatalf("zonemd_verify on a file RPZ zone: %d", code)
	}
	upd := s.admin.updateRPZ(t, rpz, `"zonemd_verify":"off"`)
	if upd["zonemd_verify"] != "off" {
		t.Fatalf("rpz update: %v", upd)
	}
	if src := zmdFindRPZ(s.latestSnapshot(t), rpz["id"].(string)).GetTransfer(); src.GetZonemdVerify() != controlv1.ZonemdVerify_ZONEMD_VERIFY_OFF {
		t.Fatalf("snapshot zonemd_verify after update %v", src.GetZonemdVerify())
	}
	s.recordRPZStats(t, rpz["id"].(string), controlv1.ZonemdStatus_ZONEMD_STATUS_FAILED, "digest mismatch")
	var got []map[string]any
	s.admin.raw(t, "GET", "/rpz-zones", "", &got)
	if st := zmdRPZStatus(got, rpz["id"].(string)); st["zonemd"] != "failed" || st["zonemd_error"] != "digest mismatch" {
		t.Fatalf("rpz status %v", st)
	}
	s.auditHasNoSecret(t, "createZone", "updateZone", "createRpzZone", "updateRpzZone")
}

func TestCatalogMembershipRules(t *testing.T) {
	s := zmdNewServer(t)
	var calls [][]uuid.UUID
	s.zones.CatalogChanged = func(_ context.Context, _ pgx.Tx, ids []uuid.UUID) error { calls = append(calls, ids); return nil }
	prodA, prodB := s.insertCatalog(t, "a.cat.", "producer"), s.insertCatalog(t, "b.cat.", "producer")
	z := s.admin.createZone(t, `{"name":"m.test.","kind":"primary","soa":{"mname":"ns.m.test.","rname":"h.m.test."},"nameservers":["ns.m.test."],"catalog_zone_id":"`+prodA.String()+`"}`)
	if z["catalog_zone_id"] != prodA.String() {
		t.Fatalf("create member: %v", z)
	}
	moved := s.admin.updateZone(t, z["id"].(string), `{"revision":1,"catalog_zone_id":"`+prodB.String()+`"}`)
	if moved["catalog_zone_id"] != prodB.String() {
		t.Fatalf("move member: %v", moved)
	}
	s.admin.deleteZone(t, z["id"].(string))
	want := [][]uuid.UUID{{prodA}, {prodA, prodB}, {prodB}}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("hook calls %v, want %v", calls, want)
	}
	// Leaving a catalog with an explicit null calls the hook with the old id only.
	left := s.admin.createZone(t, `{"name":"n.test.","kind":"primary","soa":{"mname":"ns.n.test.","rname":"h.n.test."},"nameservers":["ns.n.test."],"catalog_zone_id":"`+prodA.String()+`"}`)
	calls = nil
	if out := s.admin.updateZone(t, left["id"].(string), `{"revision":1,"catalog_zone_id":null}`); out["catalog_zone_id"] != nil {
		t.Fatalf("leave catalog: %v", out)
	}
	if fmt.Sprint(calls) != fmt.Sprint([][]uuid.UUID{{prodA}}) {
		t.Fatalf("hook calls on leave %v", calls)
	}
	consumer := s.insertCatalog(t, "c.cat.", "consumer")
	if code := s.admin.status(t, "POST", "/zones", `{"name":"o.test.","kind":"primary","soa":{"mname":"ns.o.test.","rname":"h.o.test."},"nameservers":["ns.o.test."],"catalog_zone_id":"`+consumer.String()+`"}`); code != 400 {
		t.Fatalf("member of a consumer catalog: %d", code)
	}
	member := s.insertMemberZone(t, "x.test.", consumer, "lbl") // secondary created by the consumer
	if code := s.admin.status(t, "PATCH", "/zones/"+member.String(), `{"revision":1,"zonemd_verify":"off"}`); code != 409 {
		t.Fatalf("update of a consumer member: %d", code)
	}
	if code := s.admin.status(t, "DELETE", "/zones/"+member.String()+"?revision=1", ""); code != 409 {
		t.Fatalf("delete of a consumer member: %d", code)
	}
	catZone := s.catalogZoneID(t, prodA)
	if code := s.admin.status(t, "POST", "/zones/"+catZone.String()+"/records", `{"name":"x.a.cat.","type":"TXT","ttl":0,"data":"\"x\""}`); code != 409 {
		t.Fatalf("record edit of a producer catalog: %d", code)
	}
	if code := s.admin.status(t, "PATCH", "/zones/"+catZone.String(), `{"revision":1,"catalog_zone_id":"`+prodB.String()+`"}`); code != 400 {
		t.Fatalf("catalog zone as a member: %d", code)
	}
}

// zmdServer is the API test server of the ZONEMD and catalog membership tests: an operator
// ("admin") and a viewer client, the zone service and a configured key store.
type zmdServer struct {
	env           *apiEnv
	admin, viewer zmdClient
	zones         *zone.Service
	secret        string // base64 TSIG secret of the RPZ transfer zones
}

func zmdNewServer(t *testing.T) *zmdServer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kekPath := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(kekPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadKEKFile(kekPath)
	if err != nil {
		t.Fatal(err)
	}
	var zones *zone.Service
	op, viewer, env := roleClientsWith(t, func(d *api.Deps) { d.Secrets, zones = box, d.Zones })
	return &zmdServer{env: env, admin: zmdClient{op}, viewer: zmdClient{viewer}, zones: zones,
		secret: base64.StdEncoding.EncodeToString([]byte("zonemd-tsig-secret-0123"))}
}

type zmdClient struct{ *client }

// raw sends body verbatim and decodes the response into out.
func (c zmdClient) raw(t *testing.T, method, path, body string, out any) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode
}

func (c zmdClient) status(t *testing.T, method, path, body string) int {
	t.Helper()
	return c.raw(t, method, path, body, nil)
}

func (c zmdClient) expect(t *testing.T, want int, method, path, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if code := c.raw(t, method, path, body, &out); code != want {
		t.Fatalf("%s %s = %d %v", method, path, code, out)
	}
	return out
}

func (c zmdClient) createZone(t *testing.T, body string) map[string]any {
	t.Helper()
	return c.expect(t, 201, "POST", "/zones", body)
}

func (c zmdClient) updateZone(t *testing.T, id, body string) map[string]any {
	t.Helper()
	return c.expect(t, 200, "PATCH", "/zones/"+id, body)
}

func (c zmdClient) deleteZone(t *testing.T, id string) {
	t.Helper()
	z := c.expect(t, 200, "GET", "/zones/"+id, "")
	c.expect(t, 204, "DELETE", fmt.Sprintf("/zones/%s?revision=%v", id, z["revision"]), "")
}

// createRPZTransfer creates a TSIG-authenticated transfer RPZ zone; extra is appended to the body.
func (c zmdClient) createRPZTransfer(t *testing.T, name, primary, extra string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"source_type":"transfer","primary":%q,"tsig_key_name":"rpz-key.","tsig_algorithm":"hmac-sha256","tsig_secret":%q,"policy_override":"given","min_refresh_seconds":60,%s}`,
		name, primary, base64.StdEncoding.EncodeToString([]byte("zonemd-tsig-secret-0123")), extra)
	return c.expect(t, 201, "POST", "/rpz-zones", body)
}

// updateRPZ updates a transfer RPZ zone, keeping its source and TSIG fields; extra is appended.
func (c zmdClient) updateRPZ(t *testing.T, z map[string]any, extra string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"primary":%q,"tsig_key_name":"rpz-key.","tsig_algorithm":"hmac-sha256","policy_override":"given","min_refresh_seconds":60,"revision":%v,%s}`,
		z["primary"], z["revision"], extra)
	return c.expect(t, 200, "PUT", "/rpz-zones/"+z["id"].(string), body)
}

func (s *zmdServer) currentSeq(t *testing.T, id string) int64 {
	t.Helper()
	var seq int64
	if err := s.env.st.Pool.QueryRow(s.env.ctx, "SELECT current_seq FROM zones WHERE id = $1", id).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func (s *zmdServer) latestSnapshot(t *testing.T) *controlv1.ConfigSnapshot {
	t.Helper()
	_, snap, err := snapshot.Latest(s.env.ctx, s.env.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func zmdFindRPZ(snap *controlv1.ConfigSnapshot, id string) *controlv1.RpzZone {
	for _, z := range snap.RpzZones {
		if z.Id == id {
			return z
		}
	}
	return nil
}

// recordRPZStats reports status for RPZ zone id from a new engine, as the stats stream would.
func (s *zmdServer) recordRPZStats(t *testing.T, id string, status controlv1.ZonemdStatus, errText string) {
	t.Helper()
	var engineID string
	if err := s.env.st.Pool.QueryRow(s.env.ctx, "INSERT INTO engines(node_name, certificate_serial) VALUES ('zmd-e1', 'zmd01') RETURNING id::text").Scan(&engineID); err != nil {
		t.Fatal(err)
	}
	st := &controlv1.Stats{RpzZones: []*controlv1.RpzZoneStatus{{Id: id, Serial: 1, Zonemd: status, ZonemdError: errText}}}
	if err := stats.RecordM3(s.env.ctx, s.env.st, engineID, st); err != nil {
		t.Fatal(err)
	}
}

func zmdRPZStatus(zones []map[string]any, id string) map[string]any {
	for _, z := range zones {
		if z["id"] != id {
			continue
		}
		if list, _ := z["status"].([]any); len(list) > 0 {
			st, _ := list[0].(map[string]any)
			return st
		}
	}
	return nil
}

// auditHasNoSecret requires an audit row for every action and no TSIG secret in any of them.
func (s *zmdServer) auditHasNoSecret(t *testing.T, actions ...string) {
	t.Helper()
	for _, action := range actions {
		var n int
		var diffs string
		if err := s.env.st.Pool.QueryRow(s.env.ctx, "SELECT count(*), coalesce(string_agg(diff::text, ' '), '') FROM audit_log WHERE action = $1", action).Scan(&n, &diffs); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("no audit row for %s", action)
		}
		if strings.Contains(diffs, s.secret) || strings.Contains(diffs, "zonemd-tsig-secret-0123") {
			t.Fatalf("audit rows of %s carry the TSIG secret", action)
		}
	}
}

// insertCatalog creates the catalog's zone through the API (a primary for producers, a secondary
// for consumers) and inserts its catalog_zones row, returning the catalog id.
func (s *zmdServer) insertCatalog(t *testing.T, name, role string) uuid.UUID {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"kind":"primary","soa":{"mname":"invalid.","rname":"hostmaster.invalid."},"nameservers":["invalid."]}`, name)
	if role == "consumer" {
		body = fmt.Sprintf(`{"name":%q,"kind":"secondary","primaries":[{"address":"127.0.0.1:53"}]}`, name)
	}
	z := s.admin.createZone(t, body)
	var id uuid.UUID
	if err := s.env.st.Pool.QueryRow(s.env.ctx, "INSERT INTO catalog_zones(zone_id, role) VALUES ($1, $2) RETURNING id", z["id"], role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// insertMemberZone creates secondary zone name as consumer catalog cat would, with label.
func (s *zmdServer) insertMemberZone(t *testing.T, name string, cat uuid.UUID, label string) uuid.UUID {
	t.Helper()
	z := s.admin.createZone(t, fmt.Sprintf(`{"name":%q,"kind":"secondary","primaries":[{"address":"127.0.0.1:53"}]}`, name))
	id := uuid.MustParse(z["id"].(string))
	if _, err := s.env.st.Pool.Exec(s.env.ctx, "UPDATE zones SET catalog_zone_id = $1, catalog_member_label = $2 WHERE id = $3", cat, label, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (s *zmdServer) catalogZoneID(t *testing.T, cat uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := s.env.st.Pool.QueryRow(s.env.ctx, "SELECT zone_id FROM catalog_zones WHERE id = $1", cat).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Both creation and replacement must reject managed ZONEMD before publishing anything.
func TestZonemdRecordRejectionDoesNotMutate(t *testing.T) {
	s := zmdNewServer(t)
	z := s.admin.createZone(t, `{"name":"reject.test.","kind":"primary","soa":{"mname":"ns.reject.test.","rname":"h.reject.test."},"nameservers":["ns.reject.test."],"zonemd_generate":true}`)
	path := "/zones/" + z["id"].(string)
	record := s.admin.expect(t, 201, "POST", path+"/records", `{"name":"www.reject.test.","type":"A","ttl":60,"data":"192.0.2.1"}`)
	state := func() []string {
		var rows []string
		for _, table := range []string{"zones", "zone_records", "zone_images", "zone_journal", "config_versions", "group_snapshots", "audit_log"} {
			var row string
			if err := s.env.st.Pool.QueryRow(s.env.ctx, `SELECT coalesce(jsonb_agg(v ORDER BY v::text), '[]'::jsonb)::text FROM (SELECT to_jsonb(r) v FROM `+pgx.Identifier{table}.Sanitize()+` r) s`).Scan(&row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		return rows
	}
	for _, method := range []string{"POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			endpoint := path + "/records"
			if method == "PUT" {
				endpoint += "/" + record["id"].(string)
			}
			before := state()
			body := fmt.Sprintf(`{"name":"reject.test.","type":"ZONEMD","ttl":60,"data":"1 1 1 %s"`, strings.Repeat("00", 48))
			if method == "PUT" {
				body += fmt.Sprintf(`,"revision":%v`, record["revision"])
			}
			result := s.admin.expect(t, 400, method, endpoint, body+"}")
			if result["code"] != "invalid_request" {
				t.Fatalf("error envelope: %v", result)
			}
			if after := state(); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected record changed records, zone versions, snapshots or audit")
			}
		})
	}
}
