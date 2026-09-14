package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func kwQueryA(addr, name string) ([]string, bool, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, addr)
	if err != nil {
		return nil, false, err
	}
	return aValues(r), r.Authoritative, nil
}

func kwEngineGroupByName(t *testing.T, api *harness.API, name string) harness.EngineGroupView {
	t.Helper()
	var groups []harness.EngineGroupView
	api.Must(http.MethodGet, "/engine-groups", nil, &groups, http.StatusOK)
	for _, g := range groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("engine group %s missing (created by deploy/kw/bootstrap.sh)", name)
	return harness.EngineGroupView{}
}

// TestKwFullProduct checks the M5 fleet on kw: the per-node engine topology in two engine groups,
// engine-group-scoped rewrites and zones, identities that survive pod restarts, a canary rollout
// with rollback and resume on edge-b, certificate rotation, and the fleet metrics and alerts.
func TestKwFullProduct(t *testing.T) {
	env := loadKwEnv(t)
	edgeAddr := os.Getenv("NEXORA_KW_EDGE_B_DNS_ADDR")
	var edgeIPs []string
	for _, ip := range strings.Split(os.Getenv("NEXORA_KW_EDGE_B_ENGINE_IPS"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			edgeIPs = append(edgeIPs, ip)
		}
	}
	if edgeAddr == "" || len(edgeIPs) != 2 {
		t.Fatalf("NEXORA_KW_EDGE_B_DNS_ADDR and two NEXORA_KW_EDGE_B_ENGINE_IPS are required (printed by scripts/kw-deploy.sh), got %q %v", edgeAddr, edgeIPs)
	}
	promURL := os.Getenv("NEXORA_KW_PROMETHEUS_URL")
	if promURL == "" {
		promURL = "http://kps-prometheus.monitoring.svc:9090"
	}
	api := kwLogin(t, env)
	edge := kwEngineGroupByName(t, api, "edge-b")
	t.Cleanup(func() {
		g := api.EngineGroup(edge.ID)
		if g.RolloutsPaused {
			api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/resume-rollouts", nil, nil, http.StatusAccepted)
			g = api.EngineGroup(edge.ID)
		}
		api.Must(http.MethodPut, "/engine-groups/"+edge.ID, map[string]any{"name": "edge-b", "revision": g.Revision,
			"description": g.Description, "rollout_strategy": "all_at_once"}, nil, http.StatusOK)
	})

	var engines []harness.EngineView
	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)

	t.Run("topology", func(t *testing.T) {
		nodeName := regexp.MustCompile(`^(edge-b-)?(master|worker)-[0-9]+$`)
		perNode := map[string]int{}
		edgeCount, connected := 0, 0
		for _, e := range engines {
			if !nodeName.MatchString(e.NodeName) {
				t.Errorf("engine %s is not named after its Kubernetes node", e.NodeName)
			}
			perNode[e.NodeName]++
			if e.Connected {
				connected++
			}
			if strings.HasPrefix(e.NodeName, "edge-b-") != (e.EngineGroupID == edge.ID) {
				t.Errorf("engine %s is in engine group %s", e.NodeName, e.EngineGroupName)
			}
			if e.EngineGroupID == edge.ID {
				edgeCount++
			}
		}
		for node, n := range perNode {
			if n != 1 {
				t.Errorf("node %s has %d engine records; a restarted pod enrolled again", node, n)
			}
		}
		if connected != env.engines || edgeCount != 2 {
			t.Fatalf("connected engines %d (want %d), edge-b engines %d (want 2)", connected, env.engines, edgeCount)
		}
	})

	t.Run("engine-group-scoped-rewrite", func(t *testing.T) {
		name := kwUniqueName("kw-fleet")
		var rw struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": "192.0.2.77", "engine_group_id": edge.ID}, &rw, http.StatusCreated)
		t.Cleanup(func() {
			api.Must(http.MethodDelete, fmt.Sprintf("/rewrites/%s?revision=%d", rw.ID, rw.Revision), nil, nil, http.StatusNoContent)
		})
		kwWaitApplied(t, api, env.engines)
		for _, ip := range edgeIPs {
			if got, _, err := kwQueryA(net.JoinHostPort(ip, "53"), name); err != nil || len(got) != 1 || got[0] != "192.0.2.77" {
				t.Fatalf("edge-b engine %s: %v %v", ip, got, err)
			}
		}
		if got, _, err := kwQueryA(edgeAddr, name); err != nil || len(got) != 1 || got[0] != "192.0.2.77" {
			t.Fatalf("edge-b load balancer: %v %v", got, err)
		}
		if got, _, err := kwQueryA(env.dnsAddr, name); err != nil || (len(got) > 0 && got[0] == "192.0.2.77") {
			t.Fatalf("the default group served an edge-b rewrite: %v %v", got, err)
		}
	})

	t.Run("zone-scoped-to-edge-b", func(t *testing.T) {
		zone := kwUniqueName("zone")
		var z struct {
			ID string `json:"id"`
		}
		api.Must(http.MethodPost, "/zones", map[string]any{"name": zone, "kind": "primary", "default_ttl": 60, "engine_group_id": edge.ID,
			"soa": map[string]any{"mname": "ns1." + zone, "rname": "hostmaster." + zone}, "nameservers": []string{"ns1." + zone}}, &z, http.StatusCreated)
		t.Cleanup(func() {
			var cur struct {
				Revision int64 `json:"revision"`
			}
			api.Must(http.MethodGet, "/zones/"+z.ID, nil, &cur, http.StatusOK)
			api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", z.ID, cur.Revision), nil, nil, http.StatusNoContent)
		})
		kwWaitApplied(t, api, env.engines)
		for _, ip := range edgeIPs {
			m := new(dns.Msg)
			m.SetQuestion(zone, dns.TypeSOA)
			r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, net.JoinHostPort(ip, "53"))
			if err != nil || !r.Authoritative {
				t.Fatalf("edge-b engine %s is not authoritative for %s: %v %v", ip, zone, r, err)
			}
		}
		m := new(dns.Msg)
		m.SetQuestion(zone, dns.TypeSOA)
		if r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, env.dnsAddr); err == nil && r.Authoritative {
			t.Fatalf("the default group serves the edge-b zone %s", zone)
		}
	})

	t.Run("canary-rollout-rollback-resume", func(t *testing.T) {
		g := api.EngineGroup(edge.ID)
		api.Must(http.MethodPut, "/engine-groups/"+edge.ID, map[string]any{"name": "edge-b", "revision": g.Revision, "description": g.Description,
			"rollout_strategy": "canary", "canary_count": 1, "health_window_seconds": 20, "ack_timeout_seconds": 60,
			"max_servfail_ratio": 0.05, "min_health_queries": 20}, nil, http.StatusOK)
		first := api.EngineByNode("edge-b-worker-24")
		api.PatchEngine(first.NodeName, map[string]any{"labels": map[string]string{"nexora.io/canary": "true"}})
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for _, ip := range edgeIPs {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					case <-time.After(20 * time.Millisecond):
						_, _, _ = kwQueryA(addr, "example.com")
					}
				}
			}(net.JoinHostPort(ip, "53"))
		}
		defer func() { close(stop); wg.Wait() }()

		name := kwUniqueName("kw-canary")
		var rw struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": "192.0.2.78", "engine_group_id": edge.ID}, &rw, http.StatusCreated)
		var newest []harness.RolloutView
		api.Must(http.MethodGet, "/rollouts?limit=1&engine_group_id="+edge.ID, nil, &newest, http.StatusOK)
		done := api.WaitRollout(edge.ID, newest[0].Version, 4*time.Minute, "completed")
		if done.Strategy != "canary" || len(done.CanaryEngineIDs) != 1 || done.CanaryEngineIDs[0] != first.ID {
			t.Fatalf("rollout %+v, want canary strategy with %s as canary", done, first.NodeName)
		}
		var completed []harness.RolloutView
		api.Must(http.MethodGet, "/rollouts?limit=20&state=completed&engine_group_id="+edge.ID, nil, &completed, http.StatusOK)
		var prev uint64
		for _, r := range completed {
			if r.Version < done.Version {
				prev = r.Version
				break
			}
		}
		if prev == 0 {
			t.Fatalf("no completed edge-b rollout older than %d", done.Version)
		}
		var rb harness.RolloutView
		api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/rollback", map[string]any{"to_version": prev}, &rb, http.StatusAccepted)
		api.WaitRollout(edge.ID, rb.Version, 2*time.Minute, "completed")
		for _, ip := range edgeIPs {
			if got, _, _ := kwQueryA(net.JoinHostPort(ip, "53"), name); len(got) > 0 && got[0] == "192.0.2.78" {
				t.Fatalf("edge-b engine %s still serves the rolled-back rewrite", ip)
			}
		}
		api.Must(http.MethodDelete, fmt.Sprintf("/rewrites/%s?revision=%d", rw.ID, rw.Revision), nil, nil, http.StatusNoContent)
		var resumed harness.RolloutView
		api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
		api.WaitRollout(edge.ID, resumed.Version, 4*time.Minute, "completed")
	})

	t.Run("certificate-rotation", func(t *testing.T) {
		target := api.EngineByNode("edge-b-worker-25")
		api.Must(http.MethodPost, "/engines/"+target.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
		harness.Eventually(t, 90*time.Second, func() error {
			if e := api.EngineByNode(target.NodeName); e.CertificateSerial == target.CertificateSerial || !e.Connected {
				return fmt.Errorf("%s not rotated yet", target.NodeName)
			}
			return nil
		})
	})

	t.Run("fleet-metrics-and-alerts", func(t *testing.T) {
		promQuery := func(q string) (float64, bool) {
			resp, err := http.Get(promURL + "/api/v1/query?query=" + url.QueryEscape(q))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var out struct {
				Data struct {
					Result []struct {
						Value []any `json:"value"`
					} `json:"result"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data.Result) == 0 {
				return 0, false
			}
			var v float64
			_, _ = fmt.Sscan(out.Data.Result[0].Value[1].(string), &v)
			return v, true
		}
		harness.Eventually(t, 3*time.Minute, func() error {
			if v, ok := promQuery(`max(nexora_mgmt_engines_disconnected{namespace="nexora"})`); !ok || v != 0 {
				return fmt.Errorf("nexora_mgmt_engines_disconnected = %v (scraped %v), want 0", v, ok)
			}
			// Every mgmt replica reports the same fleet gauges (read from PostgreSQL), so max, not sum.
			if v, ok := promQuery(`max(nexora_mgmt_engines{namespace="nexora",engine_group="edge-b",status="current"})`); !ok || v != 2 {
				return fmt.Errorf("current edge-b engines = %v (scraped %v), want 2", v, ok)
			}
			if _, firing := promQuery(`ALERTS{alertname="NexoraRolloutHalted",alertstate="firing"}`); firing {
				return fmt.Errorf("NexoraRolloutHalted is firing")
			}
			return nil
		})
		resp, err := http.Get(promURL + "/api/v1/rules")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rules struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil || !strings.Contains(string(rules.Data), "NexoraEngineDisconnected") {
			t.Fatalf("PrometheusRule not loaded: %v", err)
		}
	})
}
