package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestCanaryRolloutHaltsOnFailure(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation()
	good, bad := env.StartDNSFixture(), env.StartDNSFixture()
	good.SetRecords(t, "healthy.canary.test. 60 IN A 192.0.2.1")
	bad.SetMode(t, "servfail")
	g := api.CreateEngineGroup(map[string]any{"name": "canary", "upstream_mode": "override", "rollout_strategy": "canary", "canary_count": 1,
		"ack_timeout_seconds": 30, "health_window_seconds": 20, "max_servfail_ratio": 0.05, "min_health_queries": 20})
	var up struct {
		ID string `json:"id"`
	}
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "canary-up", "protocol": "udp", "address": good.UDP, "timeout_ms": 250,
		"enabled": true, "position": 0, "engine_group_id": g.ID}, &up, http.StatusCreated)
	names := []string{"canary-1", "canary-2", "canary-3"}
	engines := map[string]*harness.Engine{}
	token := api.CreateJoinTokenFor(g.ID, nil)
	for _, n := range names {
		engines[n] = env.StartManagedEngine(n, []string{mg.GRPCURL}, token)
	}
	api.PatchEngine("canary-3", map[string]any{"labels": map[string]string{"nexora.io/canary": "true"}})
	canaryID := api.EngineByNode("canary-3").ID
	base := api.WaitEngineGroupStable(g.ID, 0, 90*time.Second)
	for _, n := range names {
		api.WaitEngine(n, 30*time.Second, func(e harness.EngineView) bool { return e.Connected && e.AppliedVersion >= base })
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, n := range names {
		addr := engines[n].DNS
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
				_, _, _ = harness.Query(t, addr, fmt.Sprintf("load-%d.canary.test.", i), dns.TypeA, harness.QueryOpts{})
			}
		}()
	}
	defer func() { close(stop); wg.Wait() }()

	// Positive path: a healthy change passes the canary gate and completes everywhere.
	rewrite(t, api, "gate.canary.test", "192.0.2.50", g.ID)
	healthy := api.WaitRollout(g.ID, base+1, 3*time.Minute, "completed")
	if healthy.Strategy != "canary" || !slices.Equal(healthy.CanaryEngineIDs, []string{canaryID}) {
		t.Fatalf("healthy rollout %+v, want canary strategy with canary-3 as canary", healthy)
	}
	for _, n := range names {
		api.WaitEngine(n, 20*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == healthy.Version })
		wantA(t, harness.MustQuery(t, engines[n].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
	}

	var ups []map[string]any
	api.Must(http.MethodGet, "/upstreams", nil, &ups, http.StatusOK)
	for _, u := range ups {
		if u["id"] == up.ID {
			u["address"] = bad.UDP
			delete(u, "id")
			api.Must(http.MethodPut, "/upstreams/"+up.ID, u, nil, http.StatusOK)
		}
	}
	halted := api.WaitRollout(g.ID, healthy.Version+1, 3*time.Minute, "halted")
	if !strings.Contains(halted.HaltReason, "engine canary-3 servfail ratio") || !slices.Equal(halted.CanaryEngineIDs, []string{canaryID}) {
		t.Fatalf("halted rollout %+v", halted)
	}
	if e := api.EngineByNode("canary-3"); e.AppliedVersion != halted.Version {
		t.Fatalf("canary applied %d, want %d", e.AppliedVersion, halted.Version)
	}
	for _, n := range []string{"canary-1", "canary-2"} {
		if e := api.EngineByNode(n); e.AppliedVersion != healthy.Version {
			t.Fatalf("%s applied %d during a halted canary, want %d", n, e.AppliedVersion, healthy.Version)
		}
		wantA(t, harness.MustQuery(t, engines[n].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
	}
	time.Sleep(10 * time.Second)
	if r := api.WaitRollout(g.ID, halted.Version, time.Second, "halted", "pending", "canary", "verifying", "rolling", "completed"); r.State != "halted" || api.EngineByNode("canary-1").AppliedVersion != healthy.Version {
		t.Fatalf("halted rollout progressed on its own: %+v", r)
	}

	var rb harness.RolloutView
	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": healthy.Version}, &rb, http.StatusAccepted)
	api.WaitRollout(g.ID, rb.Version, time.Minute, "completed")
	waitLatestApplied(t, api, names...)
	wantA(t, harness.MustQuery(t, engines["canary-3"].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")

	rewrite(t, api, "held.canary.test", "192.0.2.99", g.ID)
	time.Sleep(5 * time.Second)
	held := api.WaitRollout(g.ID, rb.Version+1, time.Second, "pending", "canary", "verifying", "rolling", "completed", "halted")
	if held.State != "pending" || api.EngineByNode("canary-1").AppliedVersion != rb.Version {
		t.Fatalf("a change after rollback must wait while paused: %+v", held)
	}
}
