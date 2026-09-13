package e2e

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func udpFrom(t *testing.T, localIP, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second, Dialer: &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(localIP)}}}
	m, _, err := c.Exchange(question(name, qtype), server)
	if err != nil {
		t.Fatalf("query %s from %s: %v", name, localIP, err)
	}
	return m
}

func firstA(m *dns.Msg) string {
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String()
		}
	}
	return ""
}

// waitLatestApplied waits until every named engine applied the newest config version: a test
// that made several mutations must not assert on an engine still serving an intermediate one.
func waitLatestApplied(t *testing.T, api *harness.API, nodes ...string) {
	t.Helper()
	v := api.LatestVersion()
	for _, n := range nodes {
		api.WaitEngine(n, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion >= v })
	}
}

func TestPerClientPolicy(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartDNSFixture()
	fx.SetRecords(t, "ads.example.test. 300 IN A 203.0.113.10", "ok.ads.example.test. 300 IN A 203.0.113.11")
	hf := env.StartHTTPFixture()
	hf.SetList(t, "ads.txt", "ads.example.test\n")
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{DNSTLS: true})
	op := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	op.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
	op.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngineWith("policy-1", []string{mg.GRPCURL}, op.CreateJoinToken(), harness.EngineOptions{DoH: true})
	proxied := env.StartManagedEngineWith("policy-2", []string{mg.GRPCURL}, op.CreateJoinToken(), harness.EngineOptions{DoT: true, ProxyProtocolDoT: true, ProxyTrustedCIDRs: []string{"127.0.0.1/32"}})

	// positive path first: before any list exists every client resolves the name
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		m, _, err := new(dns.Client).Exchange(question("ads.example.test.", dns.TypeA), eng.DNS)
		return err == nil && firstA(m) == "203.0.113.10"
	}, "engine forwards to the fixture")
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		if got := firstA(udpFrom(t, ip, eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
			t.Fatalf("baseline from %s = %q", ip, got)
		}
	}

	var list struct {
		ID      string `json:"id"`
		BlobSHA string `json:"current_blob_sha256"`
	}
	if code, _ := op.Do(http.MethodPost, "/filter-lists", map[string]any{"name": "ads", "url": hf.URL("ads.txt"), "kind": "block", "refresh_interval_seconds": 3600, "enabled": false}, &list); code != http.StatusCreated {
		t.Fatalf("create list = %d", code)
	}
	if code, _ := op.Do(http.MethodPost, "/policy-groups", map[string]any{
		"name": "wide", "cidrs": []string{"127.0.0.0/29"}, "filter_list_ids": []string{list.ID}, "allowlist": []string{"ok.ads.example.test"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create wide = %d", code)
	}
	if code, _ := op.Do(http.MethodPost, "/policy-groups", map[string]any{
		"name": "open", "cidrs": []string{"127.0.0.3/32"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create open = %d", code)
	}
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		op.Do(http.MethodGet, "/filter-lists/"+list.ID, nil, &list)
		return list.BlobSHA != ""
	}, "a disabled list referenced by a policy group is fetched")

	// 127.0.0.2 is only in "wide" (list selected): blocked with null_ip
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return firstA(udpFrom(t, "127.0.0.2", eng.DNS, "ads.example.test.", dns.TypeA)) == "0.0.0.0"
	}, "wide group blocks ads.example.test")
	waitLatestApplied(t, op, "policy-1", "policy-2")
	// 127.0.0.3 is in both; the /32 is more specific, and "open" selects no list
	if got := firstA(udpFrom(t, "127.0.0.3", eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
		t.Fatalf("open group (most specific CIDR) = %q, want 203.0.113.10", got)
	}
	// the list is not enabled globally: 127.0.0.9 is in no group and resolves
	if got := firstA(udpFrom(t, "127.0.0.9", eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
		t.Fatalf("global client = %q, want 203.0.113.10", got)
	}
	// group allowlist beats the group's blocklist
	if got := firstA(udpFrom(t, "127.0.0.2", eng.DNS, "ok.ads.example.test.", dns.TypeA)); got != "203.0.113.11" {
		t.Fatalf("allowlisted name for wide = %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t.Run("doh-policy-uses-tcp-peer", func(t *testing.T) {
		for ip, want := range map[string]string{"127.0.0.2": "0.0.0.0", "127.0.0.3": "203.0.113.10"} {
			c := harness.EncryptedClient{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test", LocalIP: net.ParseIP(ip)}
			var m *dns.Msg
			harness.EventuallyTrue(t, 30*time.Second, func() bool {
				var err error
				m, _, err = harness.DoH(ctx, c.HTTPClient(), eng.DoHURL(), http.MethodPost, question("ads.example.test.", dns.TypeA))
				return err == nil
			}, "DoH reachable")
			if got := firstA(m); got != want {
				t.Fatalf("DoH from %s = %q, want %q", ip, got, want)
			}
		}
	})

	t.Run("dot-proxy-protocol-source", func(t *testing.T) {
		query := func(claimed string) string {
			raw, err := net.DialTimeout("tcp", proxied.DoTAddr(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			dst := raw.RemoteAddr().(*net.TCPAddr)
			if err := harness.WriteProxyV2(raw, &net.TCPAddr{IP: net.ParseIP(claimed), Port: 40000}, dst); err != nil {
				t.Fatal(err)
			}
			tc := tls.Client(raw, &tls.Config{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test"})
			conn := &dns.Conn{Conn: tc}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMsg(question("ads.example.test.", dns.TypeA)); err != nil {
				t.Fatal(err)
			}
			r, err := conn.ReadMsg()
			if err != nil {
				t.Fatal(err)
			}
			return firstA(r)
		}
		harness.EventuallyTrue(t, 30*time.Second, func() bool { return query("127.0.0.3") == "203.0.113.10" }, "PROXY source in open group resolves")
		if got := query("127.0.0.2"); got != "0.0.0.0" {
			t.Fatalf("PROXY source 127.0.0.2 = %q, want 0.0.0.0 (policy from PROXY header, not TCP peer 127.0.0.1)", got)
		}
	})
}
