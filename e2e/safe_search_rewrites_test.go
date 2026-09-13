package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func TestSafeSearchRewrites(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartDNSFixture()
	fx.SetRecords(t,
		"www.google.com. 300 IN A 142.250.1.1",
		"forcesafesearch.google.com. 300 IN A 216.239.38.120",
		"www.bing.com. 300 IN A 13.107.21.200",
		"strict.bing.com. 300 IN A 204.79.197.220",
		"duckduckgo.com. 300 IN A 52.142.124.215",
		"safe.duckduckgo.com. 300 IN A 52.142.126.100",
		"www.youtube.com. 300 IN A 142.250.1.2",
		"restrict.youtube.com. 300 IN A 216.239.38.120",
		"restrictmoderate.youtube.com. 300 IN A 216.239.38.119",
	)
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	op := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	op.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngine("ss-1", []string{mg.GRPCURL}, op.CreateJoinToken())
	a := func(ip, name string) string { return firstA(udpFrom(t, ip, eng.DNS, name, dns.TypeA)) }

	// positive path: safe search off returns the provider's normal address
	harness.EventuallyTrue(t, 30*time.Second, func() bool { return a("127.0.0.1", "www.google.com.") == "142.250.1.1" }, "baseline www.google.com")

	var ss struct {
		Revision int64 `json:"revision"`
	}
	op.Do(http.MethodGet, "/safe-search", nil, &ss)
	if code, _ := op.Do(http.MethodPut, "/safe-search", map[string]any{
		"google": true, "bing": true, "duckduckgo": true, "youtube": "strict", "revision": ss.Revision,
	}, &ss); code != http.StatusOK {
		t.Fatalf("enable safe search = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "www.google.com.") == "216.239.38.120" }, "google safe search applied")
	m := udpFrom(t, "127.0.0.1", eng.DNS, "WWW.GOOGLE.COM.", dns.TypeA)
	cname, ok := m.Answer[0].(*dns.CNAME)
	if !ok || cname.Target != "forcesafesearch.google.com." || m.Answer[0].Header().Name != "WWW.GOOGLE.COM." {
		t.Fatalf("google answer = %v", m.Answer)
	}
	for name, want := range map[string]string{
		"www.google.co.uk.": "216.239.38.120", "www.bing.com.": "204.79.197.220",
		"duckduckgo.com.": "52.142.126.100", "www.youtube.com.": "216.239.38.120",
	} {
		if got := a("127.0.0.1", name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if code, _ := op.Do(http.MethodPut, "/safe-search", map[string]any{
		"google": true, "bing": true, "duckduckgo": true, "youtube": "moderate", "revision": ss.Revision,
	}, &ss); code != http.StatusOK {
		t.Fatalf("youtube moderate = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "www.youtube.com.") == "216.239.38.119" }, "youtube moderate applied")

	// custom rewrites: A, AAAA, wildcard CNAME chased to a rewrite, exact beats wildcard
	for _, r := range []map[string]any{
		{"name": "nas.home.test", "type": "A", "value": "192.168.1.50", "ttl": 120},
		{"name": "nas.home.test", "type": "AAAA", "value": "fd00::50", "ttl": 120},
		{"name": "*.lab.home.test", "type": "CNAME", "value": "nas.home.test", "ttl": 60},
		{"name": "special.lab.home.test", "type": "A", "value": "192.168.1.60"},
	} {
		if code, _ := op.Do(http.MethodPost, "/rewrites", r, nil); code != http.StatusCreated {
			t.Fatalf("create rewrite %v = %d", r, code)
		}
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "nas.home.test.") == "192.168.1.50" }, "custom rewrite applied")
	waitLatestApplied(t, op, "ss-1")
	nas := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeA)
	if nas.Answer[0].Header().Ttl != 120 {
		t.Errorf("rewrite ttl = %d, want 120", nas.Answer[0].Header().Ttl)
	}
	aaaa := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeAAAA)
	if len(aaaa.Answer) != 1 || aaaa.Answer[0].(*dns.AAAA).AAAA.String() != "fd00::50" {
		t.Errorf("AAAA rewrite = %v", aaaa.Answer)
	}
	mx := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeMX)
	if mx.Rcode != dns.RcodeSuccess || len(mx.Answer) != 0 {
		t.Errorf("MX on rewritten name = rcode %d answers %v, want NODATA", mx.Rcode, mx.Answer)
	}
	if got := a("127.0.0.1", "x.lab.home.test."); got != "192.168.1.50" {
		t.Errorf("wildcard CNAME chase = %q", got)
	}
	if got := a("127.0.0.1", "special.lab.home.test."); got != "192.168.1.60" {
		t.Errorf("exact beats wildcard = %q", got)
	}

	// rewritten answers reach the query log as filter=rewritten, for both an inline rewrite and a
	// safe-search CNAME chased upstream
	for _, name := range []string{"nas.home.test", "www.google.com"} {
		var page struct {
			Records []struct {
				Name   string `json:"name"`
				Filter string `json:"filter"`
			} `json:"records"`
		}
		harness.EventuallyTrue(t, 30*time.Second, func() bool {
			code, _ := op.Do(http.MethodGet, "/query-log?filter=rewritten&name="+name, nil, &page)
			return code == http.StatusOK && len(page.Records) > 0
		}, "query log lists "+name+" as rewritten")
		for _, r := range page.Records {
			if r.Filter != "rewritten" {
				t.Errorf("query log filter=rewritten returned %+v", r)
			}
		}
	}

	// a group gets only its own safe search and rewrites
	if code, _ := op.Do(http.MethodPost, "/policy-groups", map[string]any{
		"name": "adults", "cidrs": []string{"127.0.0.5/32"},
		"safe_search": map[string]any{"google": false, "bing": false, "duckduckgo": false, "youtube": "off"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create group = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.5", "www.google.com.") == "142.250.1.1" }, "group without safe search")
	if got := a("127.0.0.5", "nas.home.test."); got == "192.168.1.50" {
		t.Errorf("group client got global rewrite %q; a group replaces global rewrites", got)
	}
	if got := a("127.0.0.1", "www.google.com."); got != "216.239.38.120" {
		t.Errorf("global client lost safe search: %q", got)
	}
}
