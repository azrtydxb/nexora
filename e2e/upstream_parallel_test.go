package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// parallelSetup starts management, an engine and two UDP upstreams: "slow" (position 0, every answer
// delayed 150 ms) and "fast" (position 1).
func parallelSetup(t *testing.T, node string) (*harness.API, *harness.Engine) {
	env := harness.New(t)
	pg := env.StartPostgres()
	mgmt := env.StartMgmt(pg, env.InitCA(), harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	api.DisableForwardedValidation()
	slow, fast := env.StartDNSFixture(), env.StartDNSFixture()
	slow.SetDelay(t, 150*time.Millisecond)
	api.Must("POST", "/upstreams", map[string]any{"name": "slow", "protocol": "udp", "address": slow.UDP, "timeout_ms": 1000, "enabled": true, "position": 0}, nil, 201)
	api.Must("POST", "/upstreams", map[string]any{"name": "fast", "protocol": "udp", "address": fast.UDP, "timeout_ms": 1000, "enabled": true, "position": 1}, nil, 201)
	return api, env.StartManagedEngine(node, []string{mgmt.GRPCURL}, api.CreateJoinToken())
}

func TestUpstreamParallelStrategy(t *testing.T) {
	api, eng := parallelSetup(t, "par-engine")
	var rs map[string]any
	api.Must("GET", "/resolver-settings", nil, &rs, 200)
	rs["strategy"], rs["parallel_max"] = "parallel", 2
	api.Must("PUT", "/resolver-settings", rs, &rs, 200)
	if rs["strategy"] != "parallel" || rs["parallel_max"] != float64(2) {
		t.Fatalf("settings: %v", rs)
	}
	rs["parallel_max"] = 9
	if code, _ := api.ErrorCode("PUT", "/resolver-settings", rs); code != 400 {
		t.Fatalf("parallel_max 9 -> %d", code)
	}
	v := api.LatestVersion()
	api.WaitEngine("par-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	name := harness.UniqueName("race")
	start := time.Now()
	if r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("race query: %v", r)
	}
	if d := time.Since(start); d > 120*time.Millisecond {
		t.Fatalf("parallel waited for the slow upstream: %s", d)
	}
	harness.EventuallyTrue(t, 5*time.Second, func() bool {
		return eng.Metric(t, "nexora_upstream_race_wins_total", map[string]string{"upstream": "fast"}) >= 1
	}, "race win counted for fast")
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		var page struct {
			Records []struct {
				Upstream       string
				UpstreamsRaced int `json:"upstreams_raced"`
			} `json:"records"`
		}
		api.Must("GET", "/query-log?limit=5&name="+strings.TrimSuffix(name, "."), nil, &page, 200)
		return len(page.Records) == 1 && page.Records[0].Upstream == "fast" && page.Records[0].UpstreamsRaced == 2
	}, "query log shows the winner and the raced count")
}

func TestUpstreamParallelLatency(t *testing.T) {
	api, eng := parallelSetup(t, "lat-engine")
	measure := func(strategy string) (p95, p99 time.Duration) {
		var rs map[string]any
		api.Must("GET", "/resolver-settings", nil, &rs, 200)
		rs["strategy"], rs["parallel_max"] = strategy, 0
		api.Must("PUT", "/resolver-settings", rs, nil, 200)
		v := api.LatestVersion()
		api.WaitEngine("lat-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
		var d []time.Duration
		for i := 0; i < 2000; i++ {
			_, rtt, err := harness.Query(t, eng.DNS, harness.UniqueName("lat-"+strategy), dns.TypeA, harness.QueryOpts{Timeout: 3 * time.Second})
			if err != nil {
				t.Fatalf("%s query %d: %v", strategy, i, err)
			}
			d = append(d, rtt)
		}
		slices.Sort(d)
		return d[len(d)*95/100], d[len(d)*99/100]
	}
	o95, o99 := measure("ordered")
	p95, p99 := measure("parallel")
	t.Logf("ordered p95 %s p99 %s; parallel p95 %s p99 %s (uncached, one upstream +150 ms)", o95, o99, p95, p99)
	if p95 >= o95 || p99 >= o99 {
		t.Fatalf("parallel is not faster: ordered p95 %s p99 %s, parallel p95 %s p99 %s", o95, o99, p95, p99)
	}
}
