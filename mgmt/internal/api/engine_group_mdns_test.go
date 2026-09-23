package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestEngineGroupMdnsAPI(t *testing.T) {
	s := newAPITestServer(t)
	body := func(mdns string) string { return `{"name":"lan","mdns":` + mdns + `}` }
	for name, m := range map[string]string{
		"no interfaces":       `{"enabled":true,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`,
		"one reflect iface":   `{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":true,"reflect_interfaces":["eth0"]}`,
		"bad interface name":  `{"enabled":true,"interfaces":["eth0/1"],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`,
		"timeout too small":   `{"enabled":true,"interfaces":["eth0"],"timeout_ms":50,"reflect":false,"reflect_interfaces":[]}`,
		"duplicate interface": `{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":true,"reflect_interfaces":["eth0","eth0"]}`,
	} {
		if code := s.operator.status(t, "POST", "/api/v1/engine-groups", body(m)); code != 400 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if code := s.viewer.status(t, "POST", "/api/v1/engine-groups", body(`{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`)); code != 403 {
		t.Fatalf("viewer create: %d", code)
	}
	g := s.operator.post(t, "/api/v1/engine-groups", body(`{"enabled":true,"interfaces":["vlan10"],"timeout_ms":700,"reflect":true,"reflect_interfaces":["vlan10","vlan20"]}`))
	if m := g["mdns"].(map[string]any); m["enabled"] != true || m["timeout_ms"].(float64) != 700 {
		t.Fatalf("create echo %v", g["mdns"])
	}
	snap := s.groupSnapshot(t, g["id"].(string))
	if mc := snap.GetMdns(); !mc.GetEnabled() || mc.GetInterfaces()[0] != "vlan10" || mc.GetTimeoutMs() != 700 || len(mc.GetReflectInterfaces()) != 2 {
		t.Fatalf("group snapshot mdns %v", mc)
	}
	if s.groupSnapshot(t, "00000000-0000-0000-0000-000000000001").Mdns != nil {
		t.Fatal("the default group got an mdns config")
	}
	s.operator.put(t, "/api/v1/engine-groups/"+g["id"].(string), `{"name":"lan","description":"renamed","revision":1}`)
	if !s.groupSnapshot(t, g["id"].(string)).GetMdns().GetEnabled() {
		t.Fatal("an update without mdns reset the settings")
	}
	s.operator.put(t, "/api/v1/engine-groups/"+g["id"].(string), `{"name":"lan","revision":2,"mdns":{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}}`)
	if s.groupSnapshot(t, g["id"].(string)).Mdns != nil {
		t.Fatal("mdns config kept after turning both features off")
	}
	auditHas(t, s, "updateEngineGroup", "mdns")
}

// mdnsServer is an API server with logged-in operator and viewer clients.
type mdnsServer struct {
	env              *apiEnv
	operator, viewer mdnsClient
}

type mdnsClient struct{ c *client }

func newAPITestServer(t *testing.T) *mdnsServer {
	t.Helper()
	op, viewer, env := roleClientsWith(t, nil)
	return &mdnsServer{env: env, operator: mdnsClient{op}, viewer: mdnsClient{viewer}}
}

// status sends body (raw JSON) and returns the status code.
func (m mdnsClient) status(t *testing.T, method, path, body string) int {
	t.Helper()
	return m.c.do(method, strings.TrimPrefix(path, "/api/v1"), json.RawMessage(body), nil)
}

func (m mdnsClient) expect(t *testing.T, want int, method, path, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if code := m.c.do(method, strings.TrimPrefix(path, "/api/v1"), json.RawMessage(body), &out); code != want {
		t.Fatalf("%s %s -> %d %v", method, path, code, out)
	}
	return out
}

func (m mdnsClient) post(t *testing.T, path, body string) map[string]any {
	return m.expect(t, 201, "POST", path, body)
}

func (m mdnsClient) put(t *testing.T, path, body string) map[string]any {
	return m.expect(t, 200, "PUT", path, body)
}

// groupSnapshot returns engine group id's newest snapshot.
func (s *mdnsServer) groupSnapshot(t *testing.T, id string) *controlv1.ConfigSnapshot {
	t.Helper()
	var raw []byte
	if err := s.env.st.Pool.QueryRow(s.env.ctx, `select snapshot from group_snapshots where engine_group_id = $1
		order by version desc limit 1`, id).Scan(&raw); err != nil {
		t.Fatalf("group %s snapshot: %v", id, err)
	}
	snap := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(raw, snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// auditHas fails unless an audit row of action has a diff containing text.
func auditHas(t *testing.T, s *mdnsServer, action, text string) {
	t.Helper()
	var n int
	if err := s.env.st.Pool.QueryRow(s.env.ctx, `select count(*) from audit_log where action = $1 and diff::text like '%' || $2 || '%'`,
		action, text).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("no %s audit row mentions %q", action, text)
	}
}
