package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/odoh"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

// odohServer is an API server with Deps.ODoH over a KEK box and logged-in admin, operator and viewer clients.
type odohServer struct {
	env                     *apiEnv
	keys                    *odoh.Keys
	admin, operator, viewer *odohClient
}

type odohClient struct{ c *client }

func odohKEK(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadKEKFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func odohTestServer(t *testing.T) *odohServer {
	t.Helper()
	box := odohKEK(t)
	s := &odohServer{}
	e := newAPIWith(t, func(d *api.Deps) {
		s.keys = &odoh.Keys{Pool: d.Store.Pool, Box: box}
		d.ODoH = api.NewODoHService(d.Store, s.keys, snapshot.BuildConfig{})
	})
	s.env = e
	admin := e.client(t)
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	login := func(name, role string) *odohClient {
		if code := admin.do("POST", "/users", map[string]any{"username": name, "email": name + "@x", "password": name + "-password-1", "role": role}, nil); code != 201 {
			t.Fatalf("create %s -> %d", role, code)
		}
		c := e.client(t)
		if code := c.do("POST", "/auth/login", map[string]string{"username": name, "password": name + "-password-1"}, nil); code != 200 {
			t.Fatalf("login %s -> %d", role, code)
		}
		return &odohClient{c}
	}
	s.admin, s.operator, s.viewer = &odohClient{admin}, login("opal", "operator"), login("vic", "viewer")
	return s
}

// status sends body (raw JSON; "" sends none) and returns the status code.
func (o *odohClient) status(t *testing.T, method, path, body string) int {
	t.Helper()
	var b any
	if body != "" {
		b = json.RawMessage(body)
	}
	return o.c.do(method, strings.TrimPrefix(path, "/api/v1"), b, nil)
}

func (o *odohClient) call(t *testing.T, method, path, body string) map[string]any {
	t.Helper()
	var b any
	if body != "" {
		b = json.RawMessage(body)
	}
	var out map[string]any
	if code := o.c.do(method, strings.TrimPrefix(path, "/api/v1"), b, &out); code != http.StatusOK {
		t.Fatalf("%s %s -> %d %v", method, path, code, out)
	}
	return out
}

func (o *odohClient) get(t *testing.T, path string) map[string]any { return o.call(t, "GET", path, "") }
func (o *odohClient) put(t *testing.T, path, body string) map[string]any {
	return o.call(t, "PUT", path, body)
}
func (o *odohClient) post(t *testing.T, path, body string) map[string]any {
	return o.call(t, "POST", path, body)
}

func (s *odohServer) latestVersion(t *testing.T) uint64 {
	t.Helper()
	v, _, err := snapshot.Latest(s.env.ctx, s.env.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (s *odohServer) latestSnapshot(t *testing.T) *controlv1.ConfigSnapshot {
	t.Helper()
	_, snap, err := snapshot.Latest(s.env.ctx, s.env.st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// odohAuditRows returns the diffs of the audit rows of action.
func (s *odohServer) odohAuditRows(t *testing.T, action string) []string {
	t.Helper()
	rows, err := s.env.st.Pool.Query(s.env.ctx, `select actor_type || ' ' || actor_id || ' ' || target_type || ' ' || target_id || ' ' || coalesce(diff::text, '')
		from audit_log where action = $1`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// odohAuditHasNoSecret asserts every action has an audit row and that no row holds a current seed
// or a sealed envelope in any encoding.
func odohAuditHasNoSecret(t *testing.T, s *odohServer, actions ...string) {
	t.Helper()
	keys, _, err := s.keys.Load(s.env.ctx)
	if err != nil || len(keys.Keys) == 0 {
		t.Fatalf("load keys: %d %v", len(keys.GetKeys()), err)
	}
	var secretsText []string
	for _, k := range keys.Keys {
		secretsText = append(secretsText, hex.EncodeToString(k.Seed), base64.StdEncoding.EncodeToString(k.Seed), string(k.Seed))
	}
	envs, err := s.env.st.Pool.Query(s.env.ctx, `select seed_envelope from odoh_keys`)
	if err != nil {
		t.Fatal(err)
	}
	for envs.Next() {
		var env []byte
		if err := envs.Scan(&env); err != nil {
			t.Fatal(err)
		}
		secretsText = append(secretsText, hex.EncodeToString(env), base64.StdEncoding.EncodeToString(env))
	}
	envs.Close()
	for _, action := range actions {
		rows := s.odohAuditRows(t, action)
		if len(rows) == 0 {
			t.Fatalf("no audit row for %s", action)
		}
		for _, r := range rows {
			for _, secret := range secretsText {
				if bytes.Contains([]byte(r), []byte(secret)) {
					t.Fatalf("audit row of %s holds key material: %s", action, r)
				}
			}
			if strings.Contains(r, "NXE1") || strings.Contains(r, "TlhFM") {
				t.Fatalf("audit row of %s holds an envelope: %s", action, r)
			}
		}
	}
}

func TestOdohSettingsAPI(t *testing.T) {
	s := odohTestServer(t)
	got := s.viewer.get(t, "/api/v1/odoh")
	if got["target_enabled"] != false || len(got["keys"].([]any)) != 0 {
		t.Fatalf("defaults: %v", got)
	}
	v0 := s.latestVersion(t)
	if code := s.viewer.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 403 {
		t.Fatalf("viewer update: %d", code)
	}
	if code := s.operator.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":false,"proxy_enabled":true,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 400 {
		t.Fatalf("proxy without targets: %d", code)
	}
	up := s.operator.put(t, "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":true,"proxy_targets":[{"host":"odoh.example:8443","ca_pem":""}],"proxy_timeout_ms":1500,"key_rotation_hours":12,"revision":1}`)
	if up["revision"].(float64) != 2 {
		t.Fatalf("update: %v", up)
	}
	snap := s.latestSnapshot(t)
	if s.latestVersion(t) != v0+1 || !snap.GetOdoh().GetTargetEnabled() || snap.GetOdoh().GetProxyTargets()[0].GetHost() != "odoh.example:8443" || snap.GetOdoh().GetProxyTimeoutMs() != 1500 {
		t.Fatalf("snapshot odoh %v", snap.GetOdoh())
	}
	if code := s.operator.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 409 {
		t.Fatalf("stale revision: %d", code)
	}
	if code := s.operator.status(t, "POST", "/api/v1/odoh/rotate-key", ""); code != 403 {
		t.Fatalf("operator rotate: %d", code)
	}
	rot := s.admin.post(t, "/api/v1/odoh/rotate-key", "")
	if len(rot["keys"].([]any)) != 1 {
		t.Fatalf("rotate: %v", rot)
	}
	if n := len(s.odohAuditRows(t, "rotateOdohKeyScheduled")); n != 0 {
		t.Fatalf("a forced rotation wrote %d scheduled audit rows", n)
	}

	// The scheduled rotation (Keys.Run, as main.go starts it) records rotateOdohKeyScheduled once
	// per key it creates: 13 h later the 12 h interval is due.
	ctx, cancel := context.WithCancel(s.env.ctx)
	later := &odoh.Keys{Pool: s.keys.Pool, Box: s.keys.Box, Now: func() time.Time { return time.Now().Add(13 * time.Hour) }}
	done := make(chan error, 1)
	go func() { done <- later.Run(ctx, 20*time.Millisecond) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(s.odohAuditRows(t, "rotateOdohKeyScheduled")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // later ticks find the new key fresh and write nothing
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	scheduled := s.odohAuditRows(t, "rotateOdohKeyScheduled")
	if len(scheduled) != 1 || !strings.HasPrefix(scheduled[0], "system odoh-rotate odoh_key ") {
		t.Fatalf("scheduled rotation audit rows: %v", scheduled)
	}
	if infos, err := s.keys.List(s.env.ctx); err != nil || len(infos) != 2 || !strings.Contains(scheduled[0], infos[0].ID.String()) {
		t.Fatalf("scheduled rotation must name its new key: %v %v %v", infos, scheduled, err)
	}

	s.admin.put(t, "/api/v1/odoh", `{"target_enabled":false,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":2}`)
	if s.latestSnapshot(t).Odoh != nil {
		t.Fatal("odoh config kept with both roles off")
	}
	odohAuditHasNoSecret(t, s, "updateOdohSettings", "rotateOdohKey", "rotateOdohKeyScheduled") // no seed, no envelope in audit rows

	// Without key storage a forced rotation is refused and creates nothing.
	unboxed := api.NewODoHService(s.env.st, &odoh.Keys{Pool: s.env.st.Pool}, snapshot.BuildConfig{})
	if _, err := unboxed.Rotate(s.env.ctx, auth.Actor{Type: "user", ID: "admin", Name: "admin"}); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("rotate without key storage: %v", err)
	}
}
