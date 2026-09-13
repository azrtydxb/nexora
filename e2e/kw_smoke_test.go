package e2e

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// kwBlockedName is listed by the in-cluster static block list that deploy/kw/bootstrap.sh
// subscribes to (deploy/kw/blocklist.yaml).
const kwBlockedName = "ads.nexora-smoke.test."

// TestKwSmoke checks the kw deployment from inside the cluster network: management health and GUI,
// three connected engines, forwarding over UDP and TCP through the DNS LoadBalancer, and blocking.
func TestKwSmoke(t *testing.T) {
	dnsAddr, apiURL := os.Getenv("NEXORA_KW_DNS_ADDR"), strings.TrimSuffix(os.Getenv("NEXORA_KW_API_URL"), "/")
	if dnsAddr == "" || apiURL == "" {
		t.Skip("NEXORA_KW_DNS_ADDR and NEXORA_KW_API_URL are not set")
	}
	hc := &http.Client{Timeout: 10 * time.Second}

	resp, err := hc.Get(apiURL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != 200 || health["status"] != "ok" || health["database"] != "ok" {
		t.Fatalf("health: %d %v", resp.StatusCode, health)
	}

	resp, err = hc.Get(apiURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), `id="root"`) {
		t.Fatalf("GUI not served: %d", resp.StatusCode)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err = hc.Get(apiURL + "/metrics")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "\nnexora_fleet_engines_connected 3\n") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("three engines are not connected to the management plane")
		}
		time.Sleep(2 * time.Second)
	}

	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network, Timeout: 3 * time.Second}
		m := new(dns.Msg)
		m.SetQuestion("example.com.", dns.TypeA)
		first, _, err := c.Exchange(m, dnsAddr)
		if err != nil || first.Rcode != dns.RcodeSuccess || len(first.Answer) == 0 {
			t.Fatalf("%s query: %v %v", network, first, err)
		}
		time.Sleep(1100 * time.Millisecond)
		second, _, err := c.Exchange(m, dnsAddr)
		if err != nil || second.Rcode != dns.RcodeSuccess || len(second.Answer) == 0 {
			t.Fatalf("%s second query: %v %v", network, second, err)
		}
		if second.Answer[0].Header().Ttl > first.Answer[0].Header().Ttl {
			t.Fatalf("%s TTL grew between queries: %d -> %d", network, first.Answer[0].Header().Ttl, second.Answer[0].Header().Ttl)
		}

		b := new(dns.Msg)
		b.SetQuestion(kwBlockedName, dns.TypeA)
		blocked, _, err := c.Exchange(b, dnsAddr)
		if err != nil || blocked.Rcode != dns.RcodeSuccess || len(blocked.Answer) == 0 {
			t.Fatalf("%s blocked query: %v %v", network, blocked, err)
		}
		if a, ok := blocked.Answer[0].(*dns.A); !ok || !a.A.Equal(net.IPv4zero) {
			t.Fatalf("%s %s was not blocked: %v", network, kwBlockedName, blocked.Answer)
		}
	}
}
