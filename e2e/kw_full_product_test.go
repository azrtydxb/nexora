package e2e

import (
	"encoding/json"
	"fmt"
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

// kwQueryLogEngines returns the engine ids the query log records for name (without the trailing dot),
// once at least want records are listed.
func kwQueryLogEngines(t *testing.T, api *harness.API, name string, want int) map[string]int {
	t.Helper()
	var page struct {
		Records []struct {
			EngineID string `json:"engine_id"`
		} `json:"records"`
	}
	harness.EventuallyTrue(t, 90*time.Second, func() bool {
		code, _ := api.Do(http.MethodGet, "/query-log?name="+url.QueryEscape(strings.TrimSuffix(name, ".")), nil, &page)
		return code == http.StatusOK && len(page.Records) >= want
	}, "query log lists "+name)
	ids := map[string]int{}
	for _, r := range page.Records {
		ids[r.EngineID]++
	}
	return ids
}

// TestKwFullProduct checks the M5 fleet on kw: two engines of the default group, each named after its
// Kubernetes node and the only engine behind one DNS address (.136, .139); certificate rotation
// while that engine keeps answering; and the fleet metrics and alerts. It changes no configuration
// clients see: kw serves real clients, so it neither pauses rollouts nor pushes test configuration.
// Engine-group-scoped configuration and canary rollouts with rollback and resume are proven by the
// local e2e tests TestEngineGroupScopedConfig and TestFleetRolloutAndPartition (e2e/fleet_test.go),
// TestCanaryRolloutHaltsOnFailure (e2e/fleet_canary_test.go) and TestFleetAPI (e2e/fleet_api_test.go);
// kw ran them live on the engine group edge-b until it was removed (2026-09-14).
func TestKwFullProduct(t *testing.T) {
	env := loadKwEnv(t)
	promURL := os.Getenv("NEXORA_KW_PROMETHEUS_URL")
	if promURL == "" {
		promURL = "http://kps-prometheus.monitoring.svc:9090"
	}
	api := kwLogin(t, env)

	var engines []harness.EngineView
	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
	connectedIDs := map[string]harness.EngineView{}
	for _, e := range engines {
		if e.Connected {
			connectedIDs[e.ID] = e
		}
	}

	t.Run("topology", func(t *testing.T) {
		nodeName := regexp.MustCompile(`^(master|worker)-[0-9]+$`)
		perNode := map[string]int{}
		for _, e := range engines {
			if !nodeName.MatchString(e.NodeName) {
				t.Errorf("engine %s is not named after its Kubernetes node", e.NodeName)
			}
			if e.EngineGroupName != "default" {
				t.Errorf("engine %s is in engine group %s, want default", e.NodeName, e.EngineGroupName)
			}
			perNode[e.NodeName]++
		}
		for node, n := range perNode {
			if n != 1 {
				t.Errorf("node %s has %d engine records; a restarted pod enrolled again", node, n)
			}
		}
		// deploy/kw/bootstrap.sh prunes engines that no longer run, so every record is a running engine.
		if len(connectedIDs) != env.engines || len(engines) != env.engines {
			t.Fatalf("%d engine records, %d connected, want %d (the scheduled engine pods)", len(engines), len(connectedIDs), env.engines)
		}
	})

	t.Run("each-dns-address-has-its-own-engine", func(t *testing.T) {
		if env.secondDNSAddr == "" {
			t.Fatal("NEXORA_KW_DNS_ADDR_2 is required (printed by scripts/kw-deploy.sh)")
		}
		const queries = 4
		byAddr := map[string]string{}
		for _, addr := range []string{env.dnsAddr, env.secondDNSAddr} {
			name := kwUniqueName("kw-address")
			for range queries {
				if _, _, err := kwQueryA(addr, name); err != nil {
					t.Fatalf("query %s via %s: %v", name, addr, err)
				}
			}
			ids := kwQueryLogEngines(t, api, name, queries)
			if len(ids) != 1 {
				t.Fatalf("%s was answered by engines %v, want exactly one engine behind the address", addr, ids)
			}
			for id := range ids {
				if _, ok := connectedIDs[id]; !ok {
					t.Fatalf("%s was answered by engine %s, not a connected engine", addr, id)
				}
				byAddr[addr] = id
			}
		}
		if byAddr[env.dnsAddr] == byAddr[env.secondDNSAddr] {
			t.Fatalf("%s and %s are both served by engine %s, want different engines", env.dnsAddr, env.secondDNSAddr, byAddr[env.dnsAddr])
		}
	})

	t.Run("certificate-rotation-keeps-answering", func(t *testing.T) {
		// The engine behind NEXORA_KW_ENGINE_ADDR (the instance-a pod).
		probe := kwUniqueName("kw-rotation")
		if _, _, err := kwQueryA(env.engineAddr, probe); err != nil {
			t.Fatal(err)
		}
		var target harness.EngineView
		for id := range kwQueryLogEngines(t, api, probe, 1) {
			target = connectedIDs[id]
		}
		if target.ID == "" {
			t.Fatalf("the engine at %s is not a connected engine", env.engineAddr)
		}
		// The authoritative demo zone answers without any upstream, so a failure is the engine's own.
		const name = "www.nexora-demo.kw."
		if got, _, err := kwQueryA(env.engineAddr, name); err != nil || len(got) == 0 {
			t.Fatalf("%s before rotation: %v %v", name, got, err)
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		sent, failed := 0, []string{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
				got, _, err := kwQueryA(env.engineAddr, name)
				mu.Lock()
				sent++
				if err != nil || len(got) == 0 {
					failed = append(failed, fmt.Sprintf("%v %v", got, err))
				}
				mu.Unlock()
			}
		}()
		api.Must(http.MethodPost, "/engines/"+target.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
		harness.Eventually(t, 90*time.Second, func() error {
			if e := api.EngineByNode(target.NodeName); e.CertificateSerial == target.CertificateSerial || !e.Connected {
				return fmt.Errorf("%s not rotated yet", target.NodeName)
			}
			return nil
		})
		time.Sleep(2 * time.Second) // a little past the reconnect
		close(stop)
		wg.Wait()
		if sent < 20 || len(failed) > 0 {
			t.Fatalf("during the rotation of %s, %d of %d queries failed: %v", target.NodeName, len(failed), sent, failed)
		}
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
			if v, ok := promQuery(`max(nexora_mgmt_engines{namespace="nexora",engine_group="default",status="current"})`); !ok || v != float64(env.engines) {
				return fmt.Errorf("current default engines = %v (scraped %v), want %d", v, ok, env.engines)
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
