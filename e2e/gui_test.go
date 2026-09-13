package e2e

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestGUICoverage(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	oidc := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@example.test", Groups: []string{"nexora-admins"}})
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OIDC: oidc, OIDCAdminGroup: "nexora-admins", ExtraEnv: []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}})
	web := env.StartHTTPFixture()
	web.SetList(t, "gui", "a.gui.test\nb.gui.test\n")
	fx := env.StartDNSFixture()

	vars := map[string]string{
		"NEXORA_E2E_BASE_URL":       mgmt.BaseURL,
		"NEXORA_E2E_SETUP_TOKEN":    mgmt.SetupToken(t),
		"NEXORA_E2E_ADMIN_USER":     "admin",
		"NEXORA_E2E_ADMIN_PASSWORD": "admin-password-e2e",
		"NEXORA_E2E_OIDC_USER":      "ada",
		"NEXORA_E2E_LIST_URL":       web.URL("gui"),
	}
	dir := harness.RunPlaywright(t, []string{"e2e/screens/00-setup.spec.ts"}, vars)

	admin := env.NewAPI(mgmt.BaseURL)
	admin.Must("POST", "/auth/login", map[string]string{"username": "admin", "password": "admin-password-e2e"}, nil, 200)
	for _, u := range []map[string]any{
		{"username": "vera", "email": "vera@example.test", "password": "viewer-password-e2e", "role": "viewer"},
		{"username": "otto", "email": "otto@example.test", "password": "operator-password-e2e", "role": "operator"},
	} {
		admin.Must("POST", "/users", u, nil, 201)
	}
	vars["NEXORA_E2E_VIEWER_USER"] = "vera"
	vars["NEXORA_E2E_VIEWER_PASSWORD"] = "viewer-password-e2e"
	vars["NEXORA_E2E_OPERATOR_USER"] = "otto"
	vars["NEXORA_E2E_OPERATOR_PASSWORD"] = "operator-password-e2e"
	admin.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	eng := env.StartManagedEngine("gui-engine", []string{mgmt.GRPCURL}, admin.CreateJoinToken())
	env.StartManagedEngine("gui-engine-2", []string{mgmt.GRPCURL}, admin.CreateJoinToken())
	v := admin.LatestVersion()
	admin.WaitEngine("gui-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	admin.WaitEngine("gui-engine-2", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	name := harness.UniqueName("gui")
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	vars["NEXORA_E2E_QUERY_NAME"] = strings.TrimSuffix(name, ".")
	time.Sleep(12 * time.Second) // one engine Stats interval so the dashboard has samples

	specs, _ := filepath.Glob(filepath.Join(harness.RepoRoot(t), "web/e2e/screens/[012][0-9]-*.spec.ts"))
	var rel []string
	for _, s := range specs {
		if !strings.HasSuffix(s, "00-setup.spec.ts") {
			rel = append(rel, strings.TrimPrefix(s, filepath.Join(harness.RepoRoot(t), "web")+"/"))
		}
	}
	vars["NEXORA_E2E_COVERAGE_DIR"] = dir
	harness.RunPlaywright(t, rel, vars)

	ops := harness.LoadOperations(t)
	covered := harness.CoveredOperations(t, ops, dir)
	if len(covered) == 0 {
		t.Fatal("no API requests were recorded; the coverage recorder is broken")
	}
	var missing []string
	for _, op := range ops {
		if !covered[op.ID] {
			missing = append(missing, fmt.Sprintf("%s (%s %s)", op.ID, op.Method, op.Path))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d OpenAPI operations have no covering Playwright test:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

func TestQueryLogBackends(t *testing.T) {
	for _, backend := range []string{"builtin", "opensearch"} {
		t.Run(backend, func(t *testing.T) {
			env := harness.New(t)
			pg := env.StartPostgres()
			ca := env.InitCA()
			opts := harness.MgmtOptions{QueryLogBackend: backend}
			if backend == "opensearch" {
				col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: harness.OpenSearchURL(t), DebugFile: env.Dir + "/otel.jsonl"})
				opts.OpenSearchURL = harness.OpenSearchURL(t)
				opts.OTLPEndpoint = "http://" + col.OTLPGRPC
			}
			mgmt := env.StartMgmt(pg, ca, opts)
			api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
			api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
			fx := env.StartDNSFixture()
			api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
			eng := env.StartManagedEngine("engine-ql-"+backend, []string{mgmt.GRPCURL}, api.CreateJoinToken())
			v := api.LatestVersion()
			api.WaitEngine("engine-ql-"+backend, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

			name := harness.UniqueName("ql-" + backend)
			queryAt := time.Now()
			if r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
				t.Fatalf("query failed: %v", r)
			}
			harness.RunPlaywright(t, []string{"e2e/querylog.spec.ts"}, map[string]string{
				"NEXORA_E2E_BASE_URL":         mgmt.BaseURL,
				"NEXORA_E2E_ADMIN_USER":       "admin",
				"NEXORA_E2E_ADMIN_PASSWORD":   "admin-password-e2e",
				"NEXORA_E2E_QUERY_NAME":       strings.TrimSuffix(name, "."),
				"NEXORA_E2E_QUERY_AT":         strconv.FormatInt(queryAt.UnixMilli(), 10),
				"NEXORA_E2E_QUERYLOG_BACKEND": backend,
			})
		})
	}
}
