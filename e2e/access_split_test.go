package e2e

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// tcpFrom sends name/qtype over TCP to server from localIP.
func tcpFrom(t *testing.T, localIP net.IP, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second, Dialer: &net.Dialer{LocalAddr: &net.TCPAddr{IP: localIP}}}
	m, _, err := c.Exchange(question(name, qtype), server)
	if err != nil {
		t.Fatalf("tcp query %s from %s: %v", name, localIP, err)
	}
	return m
}

// splitEnv is a management plane serving DNS TLS certificates, a fixture upstream and one managed
// engine with DoT and DoH listeners.
type splitEnv struct {
	api *harness.API
	mg  *harness.Mgmt
	eng *harness.Engine
}

// encrypted retries exchange (the engine receives its serving certificate after it connects)
// for up to 30 s.
func (a splitEnv) encrypted(t *testing.T, proto string, ip net.IP, exchange func(c harness.EncryptedClient) (*dns.Msg, error)) *dns.Msg {
	t.Helper()
	c := harness.EncryptedClient{RootCAs: a.mg.DNSTLSRoots(), ServerName: "dns.nexora.test", LocalIP: ip}
	var m *dns.Msg
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		var err error
		m, err = exchange(c)
		return err == nil && m != nil
	}, proto+" reachable from "+ip.String())
	return m
}

func dotFrom(t *testing.T, a splitEnv, ip net.IP, name string) *dns.Msg {
	t.Helper()
	return a.encrypted(t, "DoT", ip, func(c harness.EncryptedClient) (*dns.Msg, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m, _, err := c.DoT(ctx, a.eng.DoTAddr(), question(name, dns.TypeA))
		return m, err
	})
}

func dohFrom(t *testing.T, a splitEnv, ip net.IP, name string) *dns.Msg {
	t.Helper()
	return a.encrypted(t, "DoH", ip, func(c harness.EncryptedClient) (*dns.Msg, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m, _, err := harness.DoH(ctx, c.HTTPClient(), a.eng.DoHURL(), http.MethodPost, question(name, dns.TypeA))
		return m, err
	})
}

func TestAuthoritativeAccessSplit(t *testing.T) {
	env := harness.New(t)
	mg := env.StartMgmt(env.StartPostgres(), env.InitCA(), harness.MgmtOptions{DNSTLS: true})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation() // the fixture serves unsigned data under the real root anchor
	fx := env.StartDNSFixture()
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 500, "enabled": true, "position": 0}, nil, http.StatusCreated)
	a := splitEnv{api: api, mg: mg, eng: env.StartManagedEngineWith("acl-engine", []string{mg.GRPCURL}, api.CreateJoinToken(), harness.EngineOptions{DoT: true, DoH: true})}

	z := createPrimaryZone(t, api, "split.test.", map[string]any{})
	addRecord(t, api, z.ID, "www.split.test.", "A", "192.0.2.10")
	// Recursion access: only 127.0.0.2/32; authoritative default: any.
	var ac struct {
		AllowCidrs []string `json:"allow_cidrs"`
		Revision   int64    `json:"revision"`
	}
	api.Must(http.MethodGet, "/access-control", nil, &ac, http.StatusOK)
	api.Must(http.MethodPut, "/access-control", map[string]any{"allow_cidrs": []string{"127.0.0.2/32"}, "authoritative_allow_cidrs": []string{"0.0.0.0/0", "::/0"}, "revision": ac.Revision}, nil, http.StatusOK)
	waitLatestApplied(t, api, "acl-engine")
	inside, outside := net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.3")
	transports := map[string]func(t *testing.T, ip net.IP, name string) *dns.Msg{
		"udp": func(t *testing.T, ip net.IP, n string) *dns.Msg {
			return udpFrom(t, ip.String(), a.eng.DNS, n, dns.TypeA)
		},
		"tcp": func(t *testing.T, ip net.IP, n string) *dns.Msg { return tcpFrom(t, ip, a.eng.DNS, n, dns.TypeA) },
		"dot": func(t *testing.T, ip net.IP, n string) *dns.Msg { return dotFrom(t, a, ip, n) },
		"doh": func(t *testing.T, ip net.IP, n string) *dns.Msg { return dohFrom(t, a, ip, n) },
	}
	// positive path first: the inside client gets hosted answers with RA and recursion
	for name, ask := range transports {
		t.Run(name, func(t *testing.T) {
			if r := ask(t, inside, "www.split.test."); r.Rcode != dns.RcodeSuccess || !r.RecursionAvailable {
				t.Fatalf("inside hosted: %v", r)
			}
			if r := ask(t, inside, harness.UniqueName("rec")); r.Rcode != dns.RcodeSuccess {
				t.Fatalf("inside recursion: %v", r)
			}
			if r := ask(t, outside, "www.split.test."); r.Rcode != dns.RcodeSuccess || r.RecursionAvailable {
				t.Fatalf("outside hosted under any: %v", r)
			}
			if r := ask(t, outside, harness.UniqueName("rec")); r.Rcode != dns.RcodeRefused {
				t.Fatalf("outside recursion must be refused: %v", r)
			}
		})
	}

	var zone zoneResp
	api.Must(http.MethodGet, "/zones/"+z.ID, nil, &zone, http.StatusOK)
	api.Must(http.MethodPatch, "/zones/"+z.ID, map[string]any{"revision": zone.Revision, "allow_query_cidrs": []string{"127.0.0.2/32"}}, nil, http.StatusOK)
	waitLatestApplied(t, api, "acl-engine")
	for name, ask := range transports {
		if r := ask(t, inside, "www.split.test."); r.Rcode != dns.RcodeSuccess {
			t.Fatalf("%s inside after override: %v", name, r)
		}
		if r := ask(t, outside, "www.split.test."); r.Rcode != dns.RcodeRefused {
			t.Fatalf("%s outside after override must be refused: %v", name, r)
		}
	}
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		var page struct {
			Records []struct{ Source, Rule, Client string } `json:"records"`
		}
		api.Must(http.MethodGet, "/query-log?source=acl&limit=100", nil, &page, http.StatusOK)
		kinds := map[string]bool{}
		for _, r := range page.Records {
			kinds[r.Rule] = true
		}
		return kinds["recursion"] && kinds["authoritative"]
	}, "query log records both refusal kinds")
	if v := a.eng.Metric(t, "nexora_acl_refused_total", map[string]string{"acl": "authoritative"}); v < 4 {
		t.Fatalf("nexora_acl_refused_total{acl=authoritative} = %v", v)
	}
}
