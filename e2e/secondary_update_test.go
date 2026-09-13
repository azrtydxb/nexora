package e2e

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func TestSecondaryAndDynamicUpdate(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "upd-1")
	api, eng := e.api, e.engines[0]

	t.Run("secondary zone follows NOTIFY from external primary", func(t *testing.T) {
		updSecret := base64.StdEncoding.EncodeToString([]byte("fixture-update-key-for-named"))
		named := e.env.StartNamedConfig(harness.NamedConfig{
			Keys: []harness.NamedKey{{Name: "upd-key.", Algorithm: "hmac-sha256", SecretB64: updSecret}},
			Zones: []harness.NamedZone{{Name: "upstream.test.", Type: "primary", AllowUpdateKey: "upd-key.", AlsoNotify: eng.DNS,
				Text: "$TTL 60\n@ SOA ns.upstream.test. h.upstream.test. 1 3600 600 86400 60\n@ NS ns.upstream.test.\nns A 192.0.2.53\na A 192.0.2.1\n"}},
		})
		var z zoneResp
		api.Must(http.MethodPost, "/zones", map[string]any{"name": "upstream.test.", "kind": "secondary", "primaries": []map[string]any{{"address": named.Addr}}}, &z, http.StatusCreated)
		harness.WaitDNSAnswer(t, eng.DNS, "a.upstream.test.", dns.TypeA, 15*time.Second, func(m *dns.Msg) bool { return m.Authoritative && len(m.Answer) == 1 })

		m := new(dns.Msg)
		m.SetUpdate("upstream.test.")
		m.Insert([]dns.RR{mustRR(t, "b.upstream.test. 60 IN A 192.0.2.77")})
		m.SetTsig("upd-key.", dns.HmacSHA256, 300, time.Now().Unix())
		c := &dns.Client{TsigSecret: map[string]string{"upd-key.": updSecret}}
		if r, _, err := c.Exchange(m, named.Addr); err != nil || r.Rcode != dns.RcodeSuccess {
			t.Fatalf("update external primary: %v %v", r, err)
		}
		// the SOA refresh is 3600 s: only the forwarded NOTIFY makes the change appear within 10 s
		harness.WaitDNSAnswer(t, eng.DNS, "b.upstream.test.", dns.TypeA, 10*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })
		var got struct {
			SecondaryStatus struct {
				LastTrigger string `json:"last_trigger"`
			} `json:"secondary_status"`
		}
		api.Must(http.MethodGet, "/zones/"+z.ID, nil, &got, http.StatusOK)
		if got.SecondaryStatus.LastTrigger != "notify" {
			t.Fatalf("refresh was not triggered by NOTIFY: %q", got.SecondaryStatus.LastTrigger)
		}
		if v := eng.Metric(t, "nexora_auth_notify_received_total", map[string]string{"result": "forwarded"}); v < 1 {
			t.Fatalf("engine did not forward NOTIFY")
		}
	})

	t.Run("TSIG-signed update applies, unsigned is refused", func(t *testing.T) {
		var key tsigKeyResp
		api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "ddns-key.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
		createPrimaryZone(t, api, "dyn.test.", map[string]any{"update": map[string]any{"tsig_key_ids": []string{key.ID}}})
		harness.WaitDNSAnswer(t, eng.DNS, "dyn.test.", dns.TypeSOA, 5*time.Second, func(m *dns.Msg) bool { return m.Authoritative && len(m.Answer) == 1 })
		if eng.Metric(t, "nexora_control_connected", nil) != 1 {
			t.Fatal("engine control stream is not connected")
		}

		signed := new(dns.Msg)
		signed.SetUpdate("dyn.test.")
		signed.Insert([]dns.RR{mustRR(t, "host1.dyn.test. 300 IN A 192.0.2.10")})
		signed.SetTsig("ddns-key.", dns.HmacSHA256, 300, time.Now().Unix())
		c := &dns.Client{Net: "udp", TsigSecret: map[string]string{"ddns-key.": key.Secret}, Timeout: 8 * time.Second}
		harness.Eventually(t, 10*time.Second, func() error { // KeyMaterial with the new key may still be in flight
			r, _, err := c.Exchange(signed.Copy(), eng.DNS)
			if err != nil {
				return err
			}
			if r.Rcode != dns.RcodeSuccess {
				return fmt.Errorf("signed update: rcode %s", dns.RcodeToString[r.Rcode])
			}
			return nil
		})
		harness.WaitDNSAnswer(t, eng.DNS, "host1.dyn.test.", dns.TypeA, 5*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })

		unsigned := new(dns.Msg)
		unsigned.SetUpdate("dyn.test.")
		unsigned.Insert([]dns.RR{mustRR(t, "host2.dyn.test. 300 IN A 192.0.2.11")})
		r, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(unsigned, eng.DNS)
		if err != nil || r.Rcode != dns.RcodeRefused {
			t.Fatalf("unsigned update: rcode=%v err=%v, want REFUSED", r, err)
		}
		waitLatestApplied(t, api, "upd-1")
		if nx := harness.MustQuery(t, eng.DNS, "host2.dyn.test.", dns.TypeA, harness.QueryOpts{TCP: true}); nx.Rcode != dns.RcodeNameError {
			t.Fatalf("unsigned update was applied: %v", nx)
		}
	})
}

func mustRR(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return r
}
