package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }

func createUDPUpstream(t *testing.T, api *harness.API, name, addr string) map[string]any {
	var up map[string]any
	api.Must("POST", "/upstreams", map[string]any{"name": name, "protocol": "udp", "address": addr, "timeout_ms": 250, "enabled": true, "position": 0}, &up, 201)
	return up
}

func TestInvalidSnapshotRejected(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	eng := env.StartManagedEngine("engine-invalid", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	good := api.LatestVersion()
	api.WaitEngine("engine-invalid", 15*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == good })

	before := harness.UniqueName("before")
	if r := harness.MustQuery(t, eng.DNS, before, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine not serving before the invalid snapshot: %v", r)
	}

	var current controlv1.ConfigSnapshot
	raw, err := os.ReadFile(filepath.Join(eng.StateDir, "snapshot.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(raw, &current); err != nil || current.Version != good {
		t.Fatalf("persisted snapshot version %d (%v), want %d", current.Version, err, good)
	}
	bad := proto.Clone(&current).(*controlv1.ConfigSnapshot)
	bad.Upstreams[0].TimeoutMs = 10
	badVersion := harness.PublishRawSnapshot(t, pg.URL, bad)

	view := api.WaitEngine("engine-invalid", 10*time.Second, func(v harness.EngineView) bool {
		return v.RejectedVersion != nil && *v.RejectedVersion == badVersion
	})
	if view.Status != "rejected" || !strings.Contains(view.RejectedReason, "timeout") || view.AppliedVersion != good {
		t.Fatalf("API does not report the rejection: %+v", view)
	}
	if v := eng.Metric(t, "nexora_config_version", nil); uint64(v) != good {
		t.Fatalf("engine applied the invalid snapshot: version metric %v", v)
	}
	if r := harness.MustQuery(t, eng.DNS, harness.UniqueName("after"), dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine stopped serving after rejection: %v", r)
	}
	raw, _ = os.ReadFile(filepath.Join(eng.StateDir, "snapshot.binpb"))
	_ = proto.Unmarshal(raw, &current)
	if current.Version != good {
		t.Fatalf("rejected snapshot was persisted: %d", current.Version)
	}

	var ups []map[string]any
	api.Must("GET", "/upstreams", nil, &ups, 200)
	ups[0]["timeout_ms"] = 300
	api.Must("PUT", "/upstreams/"+ups[0]["id"].(string), ups[0], nil, 200)
	next := api.LatestVersion()
	api.WaitEngine("engine-invalid", 10*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == next && v.Status == "current" })
}

func TestMgmtStatelessHA(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	a := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	token := a.SetupToken(t)
	b := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	httpLB := env.StartTCPBalancer(a.HTTPAddr, b.HTTPAddr)
	grpcLB := env.StartTCPBalancer(a.GRPCAddr, b.GRPCAddr)
	api := harness.Bootstrap(t, env, token, "http://"+httpLB.Addr)
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	eng := env.StartManagedEngine("engine-ha", []string{"https://" + grpcLB.Addr}, api.CreateJoinToken())
	v1 := api.LatestVersion()
	api.WaitEngine("engine-ha", 15*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == v1 && v.Connected })
	eng.Proc.WaitLog(regexpMust("control connected to"), 5*time.Second)

	a.Proc.Kill()
	killed := time.Now()

	var ok int
	deadline := killed.Add(10 * time.Second)
	for time.Now().Before(deadline) {
		code, err := api.Do("GET", "/upstreams", nil, nil)
		if err == nil && code == 200 {
			ok++
			if ok >= 3 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ok < 3 {
		t.Fatalf("API through the load balancer did not recover within 10s of killing an instance")
	}
	var up map[string]any
	api.Must("POST", "/upstreams", map[string]any{"name": "after-failover", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 1}, &up, 201)
	v2 := api.LatestVersion()
	api.WaitEngine("engine-ha", time.Until(killed.Add(10*time.Second))+time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == v2 && v.Connected })
	if elapsed := time.Since(killed); elapsed > 11*time.Second {
		t.Fatalf("engine control stream recovered after %v", elapsed)
	}
	if r := harness.MustQuery(t, eng.DNS, harness.UniqueName("ha"), dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine not answering: %v", r)
	}
}
