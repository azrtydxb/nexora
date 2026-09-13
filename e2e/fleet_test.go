package e2e

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func rewrite(t *testing.T, api *harness.API, name, value string, engineGroupID any) {
	t.Helper()
	api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": value, "engine_group_id": engineGroupID}, nil, http.StatusCreated)
}

func TestFleetRolloutAndPartition(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	a := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, a.SetupToken(t), a.BaseURL)
	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	lb := env.StartSwitchableBalancer(a.GRPCAddr)
	edge := api.CreateEngineGroup(map[string]any{"name": "fleet-edge"})
	names := []string{"fleet-1", "fleet-2", "fleet-3"}
	groups := map[string]string{"fleet-1": harness.DefaultEngineGroupID, "fleet-2": harness.DefaultEngineGroupID, "fleet-3": edge.ID}
	tokens := map[string]string{harness.DefaultEngineGroupID: api.CreateJoinToken(), edge.ID: api.CreateJoinTokenFor(edge.ID, nil)}
	engines := map[string]*harness.Engine{}
	dirs := map[string]bool{}
	for _, n := range names {
		engines[n] = env.StartManagedEngine(n, []string{"https://" + lb.Addr}, tokens[groups[n]])
		dirs[engines[n].StateDir] = true
	}
	if len(dirs) != 3 {
		t.Fatalf("engines share a state directory: %v", dirs)
	}

	rewrite(t, api, "before.fleet.test", "192.0.2.10", nil)
	waitLatestApplied(t, api, names...)
	v := api.LatestVersion()
	ids := map[string]string{}
	for _, n := range names {
		wantA(t, harness.MustQuery(t, engines[n].DNS, "before.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.10")
		// Engine groups share one version sequence: a fleet-wide change is the same version in every group.
		e := api.EngineByNode(n)
		if e.Status != "current" || e.AppliedVersion != v || e.TargetVersion != v || e.EngineGroupID != groups[n] {
			t.Fatalf("%s: %+v, want current, applied and targeted at %d in engine group %s", n, e, v, groups[n])
		}
		ids[n] = e.ID
	}
	rewrite(t, api, "after.fleet.test", "192.0.2.20", nil)
	waitLatestApplied(t, api, names...)

	// Partition: the only management instance dies; engines keep serving their last snapshot.
	a.Proc.Kill()
	harness.Eventually(t, 30*time.Second, func() error {
		for _, n := range names {
			if v := engines[n].Metric(t, "nexora_control_connected", nil); v != 0 {
				return fmt.Errorf("%s still reports a control stream", n)
			}
		}
		return nil
	})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		for _, n := range names {
			wantA(t, harness.MustQuery(t, engines[n].DNS, "before.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.10")
			wantA(t, harness.MustQuery(t, engines[n].DNS, "after.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.20")
		}
	}

	// A new instance behind the same address: engines return, a restarted engine keeps its identity.
	b := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	lb.SetBackends(b.GRPCAddr)
	api2 := env.NewAPI(b.BaseURL)
	api2.Bearer = api.Bearer
	for _, n := range names {
		api2.WaitEngine(n, 45*time.Second, func(e harness.EngineView) bool { return e.Connected })
	}
	env.RestartEngine(engines["fleet-2"])
	var listed []harness.EngineView
	api2.Must(http.MethodGet, "/engines", nil, &listed, http.StatusOK)
	if len(listed) != 3 || api2.EngineByNode("fleet-2").ID != ids["fleet-2"] {
		t.Fatalf("restarted engine enrolled again: %+v", listed)
	}
	rewrite(t, api2, "later.fleet.test", "192.0.2.30", nil)
	waitLatestApplied(t, api2, names...)
	wantA(t, harness.MustQuery(t, engines["fleet-2"].DNS, "later.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.30")
}

func TestEngineGroupScopedConfig(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation()
	fxA, fxB := env.StartDNSFixture(), env.StartDNSFixture()
	fxA.SetRecords(t, "site.fleet.test. 60 IN A 192.0.2.101")
	fxB.SetRecords(t, "site.fleet.test. 60 IN A 192.0.2.102")
	edge := api.CreateEngineGroup(map[string]any{"name": "edge", "upstream_mode": "override"})
	createUDPUpstream(t, api, "fixture-a", fxA.UDP)
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture-b", "protocol": "udp", "address": fxB.UDP, "timeout_ms": 250,
		"enabled": true, "position": 1, "engine_group_id": edge.ID}, nil, http.StatusCreated)
	rewrite(t, api, "global.scope.test", "192.0.2.1", nil)
	rewrite(t, api, "only-edge.scope.test", "192.0.2.2", edge.ID)
	zone := "edge-only.zone.test."
	api.Must(http.MethodPost, "/zones", map[string]any{"name": zone, "kind": "primary", "default_ttl": 60, "engine_group_id": edge.ID,
		"soa": map[string]any{"mname": "ns1." + zone, "rname": "hostmaster." + zone}, "nameservers": []string{"ns1." + zone}}, nil, http.StatusCreated)

	engDefault := env.StartManagedEngine("scope-default", []string{mg.GRPCURL}, api.CreateJoinToken())
	engEdge := env.StartManagedEngine("scope-edge", []string{mg.GRPCURL}, api.CreateJoinTokenFor(edge.ID, nil))
	waitLatestApplied(t, api, "scope-default", "scope-edge")
	if e := api.EngineByNode("scope-edge"); e.EngineGroupName != "edge" {
		t.Fatalf("scope-edge enrolled into %q", e.EngineGroupName)
	}

	// Positive path first: each engine resolves through its own upstreams and serves the global rewrite.
	wantA(t, harness.MustQuery(t, engDefault.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.101")
	wantA(t, harness.MustQuery(t, engEdge.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.102")
	for _, en := range []*harness.Engine{engDefault, engEdge} {
		wantA(t, harness.MustQuery(t, en.DNS, "global.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
	}
	wantA(t, harness.MustQuery(t, engEdge.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.2")
	if soa := harness.MustQuery(t, engEdge.DNS, zone, dns.TypeSOA, harness.QueryOpts{}); !soa.Authoritative {
		t.Fatalf("scope-edge is not authoritative for %s: %v", zone, soa)
	}
	if got := aValues(harness.MustQuery(t, engDefault.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{})); len(got) > 0 && got[0] == "192.0.2.2" {
		t.Fatal("an engine of the default group served a rewrite scoped to edge")
	}
	if soa := harness.MustQuery(t, engDefault.DNS, zone, dns.TypeSOA, harness.QueryOpts{}); soa.Authoritative {
		t.Fatalf("an engine of the default group serves %s: %v", zone, soa)
	}

	api.PatchEngine("scope-default", map[string]any{"engine_group_id": edge.ID})
	api.WaitEngine("scope-default", 20*time.Second, func(e harness.EngineView) bool {
		return e.EngineGroupID == edge.ID && e.Status == "current"
	})
	wantA(t, harness.MustQuery(t, engDefault.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.2")
	wantA(t, harness.MustQuery(t, engDefault.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.102")
}

func TestJoinTokenGroupAndExpiry(t *testing.T) {
	ctx := context.Background()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	g := api.CreateEngineGroup(map[string]any{"name": "site-x"})

	var once, stale struct {
		Token     string `json:"token"`
		JoinToken struct {
			ID string `json:"id"`
		} `json:"join_token"`
	}
	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "once", "ttl_seconds": 600, "engine_group_id": g.ID, "max_uses": 1,
		"labels": map[string]string{"rack": "r1"}}, &once, http.StatusCreated)
	env.StartManagedEngine("joined", []string{mg.GRPCURL}, once.Token)
	e := api.WaitEngine("joined", 20*time.Second, func(v harness.EngineView) bool { return v.Connected })
	if e.EngineGroupID != g.ID || e.Labels["rack"] != "r1" {
		t.Fatalf("joined engine %+v, want engine group site-x with rack=r1", e)
	}

	second := env.StartManagedEngineWith("second", []string{mg.GRPCURL}, once.Token, harness.EngineOptions{SkipControlWait: true})
	second.Proc.WaitLog(regexp.MustCompile(`join token exhausted`), 30*time.Second)
	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "stale", "ttl_seconds": 600, "engine_group_id": g.ID}, &stale, http.StatusCreated)
	conn, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `update join_tokens set expires_at = now() - interval '1 second' where id = $1`, stale.JoinToken.ID); err != nil {
		t.Fatal(err)
	}
	late := env.StartManagedEngineWith("late", []string{mg.GRPCURL}, stale.Token, harness.EngineOptions{SkipControlWait: true})
	late.Proc.WaitLog(regexp.MustCompile(`join token expired`), 30*time.Second)

	var engines []harness.EngineView
	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
	for _, v := range engines {
		if v.NodeName == "second" || v.NodeName == "late" {
			t.Fatalf("engine %s enrolled with an exhausted or expired token", v.NodeName)
		}
	}
	var listed []map[string]any
	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
	states := map[string]any{}
	for _, jt := range listed {
		states[jt["id"].(string)] = jt["state"]
	}
	if states[once.JoinToken.ID] != "exhausted" || states[stale.JoinToken.ID] != "expired" {
		t.Fatalf("token states %v", states)
	}
}
