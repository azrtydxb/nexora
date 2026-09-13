package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

// TestKwSmokeM4 checks authoritative serving, AXFR over the TCP LoadBalancer and online signing on
// the kw deployment. It deletes its zone when it ends.
func TestKwSmokeM4(t *testing.T) {
	env := loadKwEnv(t)
	api := kwLogin(t, env)
	name := fmt.Sprintf("smoke-%d.nexora-smoke.test.", time.Now().Unix())
	var zone zoneResp
	api.Must(http.MethodPost, "/zones", map[string]any{
		"name": name, "kind": "primary", "default_ttl": 60,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name},
		"nameservers": []string{"ns1." + name},
		"transfer":    map[string]any{"allow_cidrs": []string{"0.0.0.0/0"}},
	}, &zone, http.StatusCreated)
	t.Cleanup(func() {
		var z zoneResp
		api.Must(http.MethodGet, "/zones/"+zone.ID, nil, &z, http.StatusOK)
		api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", zone.ID, z.Revision), nil, nil, http.StatusNoContent)
	})
	api.Must(http.MethodPost, "/zones/"+zone.ID+"/records", map[string]any{"name": "www." + name, "type": "A", "ttl": 60, "data": "192.0.2.10"}, nil, http.StatusCreated)
	kwWaitApplied(t, api, env.engines)

	t.Run("zones", func(t *testing.T) {
		r := harness.WaitDNSAnswer(t, env.dnsAddr, "www."+name, dns.TypeA, 15*time.Second, func(m *dns.Msg) bool {
			return m.Authoritative && len(m.Answer) == 1
		})
		if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "192.0.2.10" {
			t.Fatalf("answer: %v", r.Answer)
		}
	})

	t.Run("axfr", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetAxfr(name)
		ch, err := (&dns.Transfer{DialTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}).In(m, env.dnsAddr)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for e := range ch {
			if e.Error != nil {
				t.Fatal(e.Error)
			}
			n += len(e.RR)
		}
		if n < 4 {
			t.Fatalf("AXFR over the TCP LoadBalancer returned %d records", n)
		}
	})

	t.Run("dnssec", func(t *testing.T) {
		var z zoneResp
		api.Must(http.MethodGet, "/zones/"+zone.ID, nil, &z, http.StatusOK)
		if status, err := api.Do(http.MethodPut, "/zones/"+zone.ID+"/dnssec", map[string]any{"revision": z.Revision, "enabled": true}, nil); status != http.StatusOK {
			t.Fatalf("enable DNSSEC on kw (is the nexora-kek secret mounted?): %d %v", status, err)
		}
		kwWaitApplied(t, api, env.engines)
		harness.Eventually(t, 30*time.Second, func() error {
			r, _, err := harness.Query(t, env.dnsAddr, "www."+name, dns.TypeA, harness.QueryOpts{TCP: true, DO: true, EDNSSize: 4096})
			if err != nil {
				return err
			}
			for _, rr := range r.Answer {
				if _, ok := rr.(*dns.RRSIG); ok {
					return nil
				}
			}
			return fmt.Errorf("no RRSIG in %v", r.Answer)
		})
	})
}
