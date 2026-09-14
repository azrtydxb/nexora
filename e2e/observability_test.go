package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func scrape(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestObservabilityMetricsTraces(t *testing.T) {
	env := harness.New(t)
	col := env.StartOtelcol(harness.OtelcolConfig{JaegerOTLP: harness.JaegerOTLPEndpoint(t), DebugFile: env.Dir + "/otel.jsonl"})
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OTLPEndpoint: "http://" + col.OTLPGRPC})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
	fx := env.StartDNSFixture()
	api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	eng := env.StartManagedEngine("engine-obs", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	v := api.LatestVersion()
	api.WaitEngine("engine-obs", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

	name := harness.UniqueName("obs")
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})

	// A custom block list and one blocked query feed the filter index and per-category metrics.
	lists := env.StartHTTPFixture()
	lists.SetList(t, "obs-block", "blocked.obs.test\n")
	var fl map[string]any
	api.Must("POST", "/filter-lists", map[string]any{"name": "obs-block", "kind": "block", "url": lists.URL("obs-block"), "refresh_interval_seconds": 3600, "enabled": true}, &fl, 201)
	api.Must("POST", "/filter-lists/"+fl["id"].(string)+"/refresh", nil, nil, 200)
	waitLatestApplied(t, api, "engine-obs")
	harness.MustQuery(t, eng.DNS, "x.blocked.obs.test.", dns.TypeA, harness.QueryOpts{})

	body := scrape(t, "http://"+eng.Metrics+"/metrics")
	for _, m := range []string{"nexora_queries_total{", "nexora_query_duration_seconds_bucket{", "nexora_cache_hits_total", "nexora_cache_misses_total", `nexora_upstream_up{upstream="fixture"} 1`,
		`nexora_filter_blocked_total{category="custom"} 1`, "nexora_filter_index_entries 1", "nexora_filter_index_bytes ", "nexora_filter_index_max_bytes ",
		"nexora_filter_index_build_seconds ", `nexora_filter_index_decision_seconds{kind="blocked",cpu="`} {
		if !strings.Contains(body, m) {
			t.Errorf("engine /metrics missing %s", m)
		}
	}
	harness.Eventually(t, 30*time.Second, func() error {
		b := scrape(t, mgmt.BaseURL+"/metrics")
		for _, m := range []string{`nexora_fleet_qps{engine="engine-obs"}`, "nexora_fleet_query_duration_seconds_bucket{", `nexora_fleet_cache_hit_ratio{engine="engine-obs"}`, `nexora_fleet_upstream_up{engine="engine-obs",upstream="fixture"} 1`} {
			if !strings.Contains(b, m) {
				return fmt.Errorf("management /metrics missing %s", m)
			}
		}
		return nil
	})

	fx.SetMode(t, "servfail")
	bad := harness.UniqueName("servfail")
	if r := harness.MustQuery(t, eng.DNS, bad, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL, got %s", dns.RcodeToString[r.Rcode])
	}
	jaeger := harness.JaegerQueryURL(t)
	tags, _ := json.Marshal(map[string]string{"dns.question.name": strings.ToLower(bad)})
	harness.Eventually(t, 45*time.Second, func() error {
		u := jaeger + "/api/traces?service=nexora-engine&lookback=1h&limit=20&tags=" + url.QueryEscape(string(tags))
		resp, err := http.Get(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			Data []struct {
				Spans []struct {
					OperationName string `json:"operationName"`
				} `json:"spans"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		for _, tr := range out.Data {
			for _, s := range tr.Spans {
				if s.OperationName == "dns.query" {
					return nil
				}
			}
		}
		return fmt.Errorf("no dns.query trace for %s yet", bad)
	})
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func TestOTelSinkDownNoBackpressure(t *testing.T) {
	env := harness.New(t)
	debug := env.Dir + "/otel.jsonl"
	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: debug})
	fx := env.StartDNSFixture()
	snap := harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP))
	snap.Telemetry.OtlpEndpoint = "http://" + col.OTLPGRPC
	snap.Telemetry.TraceSampleOneIn = 1000
	eng := env.StartStandaloneEngine(snap, nil)

	names := make([]string, 200)
	for i := range names {
		names[i] = fmt.Sprintf("load-%03d.example.", i)
		harness.MustQuery(t, eng.DNS, names[i], dns.TypeA, harness.QueryOpts{})
	}
	harness.Eventually(t, 15*time.Second, func() error {
		if fi, err := os.Stat(debug); err != nil || fi.Size() == 0 {
			return fmt.Errorf("collector has not received logs yet")
		}
		return nil
	})

	var up, down []float64
	for round := 0; round < 3; round++ {
		up = append(up, harness.RunDnsperf(t, eng.DNS, names, 8).QPS)
		dropsBefore := eng.Metric(t, "nexora_export_dropped_total", map[string]string{"signal": "logs"})
		col.Stop()
		down = append(down, harness.RunDnsperf(t, eng.DNS, names, 8).QPS)
		harness.Eventually(t, 10*time.Second, func() error {
			if d := eng.Metric(t, "nexora_export_dropped_total", map[string]string{"signal": "logs"}); d <= dropsBefore {
				return fmt.Errorf("drop counter stayed at %v with the collector down", d)
			}
			return nil
		})
		col.Restart(env)
	}
	base, sinkDown := median(up), median(down)
	t.Logf("median QPS collector up=%.0f down=%.0f", base, sinkDown)
	if base <= 0 {
		t.Fatal("baseline QPS is zero")
	}
	if sinkDown < 0.95*base {
		t.Fatalf("QPS dropped %.1f%% with the collector stopped", 100*(1-sinkDown/base))
	}
}
