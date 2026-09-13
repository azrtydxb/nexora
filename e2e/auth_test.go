package e2e

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestAuthRBACAuditOIDC(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	oidc := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@example.test", Groups: []string{"nexora-admins"}})
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OIDC: oidc, OIDCAdminGroup: "nexora-admins", OIDCOperatorGroup: "nexora-operators"})
	admin := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)

	for _, u := range []map[string]any{
		{"username": "vera", "email": "vera@example.test", "password": "viewer-password-e2e", "role": "viewer"},
		{"username": "otto", "email": "otto@example.test", "password": "operator-password-e2e", "role": "operator"},
	} {
		admin.Must("POST", "/users", u, nil, 201)
	}
	admin.Must("POST", "/upstreams", map[string]any{"name": "seed", "protocol": "udp", "address": "192.0.2.1:53", "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)

	// Go API checks: viewer write is 403, operator write succeeds, every change is audited.
	viewer := env.NewAPI(mgmt.BaseURL)
	viewer.Must("POST", "/auth/login", map[string]string{"username": "vera", "password": "viewer-password-e2e"}, nil, 200)
	viewer.Must("GET", "/upstreams", nil, nil, 200)
	viewer.Must("POST", "/upstreams", map[string]any{"name": "nope", "protocol": "udp", "address": "192.0.2.2:53", "timeout_ms": 250, "enabled": true, "position": 1}, nil, 403)
	operator := env.NewAPI(mgmt.BaseURL)
	operator.Must("POST", "/auth/login", map[string]string{"username": "otto", "password": "operator-password-e2e"}, nil, 200)
	var created map[string]any
	operator.Must("POST", "/upstreams", map[string]any{"name": "api-op", "protocol": "udp", "address": "192.0.2.3:53", "timeout_ms": 250, "enabled": true, "position": 2}, &created, 201)
	var audit []map[string]any
	admin.Must("GET", "/audit?limit=500", nil, &audit, 200)
	found := false
	for _, a := range audit {
		if a["action"] == "createUpstream" && a["actor_name"] == "otto" && a["diff"] != nil {
			found = true
		}
		if a["actor_name"] == "vera" {
			t.Fatalf("viewer produced an audit entry: %v", a)
		}
	}
	if !found {
		t.Fatal("operator change has no audit entry with actor and diff")
	}
	var versions []map[string]any
	admin.Must("GET", "/config-versions", nil, &versions, 200)
	audited := map[float64]bool{}
	for _, a := range audit {
		if v, ok := a["config_version"].(float64); ok {
			audited[v] = true
		}
	}
	for _, v := range versions {
		if !audited[v["version"].(float64)] {
			t.Fatalf("config version %v has no audit entry", v["version"])
		}
	}

	pw := map[string]string{
		"NEXORA_E2E_BASE_URL":          mgmt.BaseURL,
		"NEXORA_E2E_ADMIN_USER":        "admin",
		"NEXORA_E2E_ADMIN_PASSWORD":    "admin-password-e2e",
		"NEXORA_E2E_VIEWER_USER":       "vera",
		"NEXORA_E2E_VIEWER_PASSWORD":   "viewer-password-e2e",
		"NEXORA_E2E_OPERATOR_USER":     "otto",
		"NEXORA_E2E_OPERATOR_PASSWORD": "operator-password-e2e",
		"NEXORA_E2E_OIDC_USER":         "ada",
	}
	harness.RunPlaywright(t, []string{"e2e/auth.spec.ts"}, pw)

	oidc.Proc.Kill()
	time.Sleep(500 * time.Millisecond)
	harness.RunPlaywright(t, []string{"e2e/auth-oidc-down.spec.ts"}, pw)
}
