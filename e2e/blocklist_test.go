package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func aRecord(t *testing.T, r *dns.Msg) net.IP {
	t.Helper()
	for _, rr := range r.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A
		}
	}
	t.Fatalf("no A record in %v", r)
	return nil
}

func TestBlocklistSubscription(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	fx := env.StartDNSFixture()
	web := env.StartHTTPFixture()
	web.SetList(t, "hosts", "# hosts\n0.0.0.0 ads-hosts.test\n127.0.0.1 tracker-hosts.test\nthis line is invalid\n")
	web.SetList(t, "adblock", "[Adblock Plus 2.0]\n! c\n||ads-adblock.test^\n")
	web.SetList(t, "domains", "ads-domains.test\ntracker.blocked.test\n")

	api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	ids := map[string]string{}
	for _, name := range []string{"hosts", "adblock", "domains"} {
		var fl map[string]any
		api.Must("POST", "/filter-lists", map[string]any{"name": name, "kind": "block", "url": web.URL(name), "refresh_interval_seconds": 3600, "enabled": true}, &fl, 201)
		ids[name] = fl["id"].(string)
		api.Must("POST", "/filter-lists/"+ids[name]+"/refresh", nil, &fl, 200)
		if fl["entry_count"].(float64) < 1 || fl["last_error"].(string) != "" {
			t.Fatalf("list %s not fetched: %v", name, fl)
		}
		if name == "hosts" && fl["invalid_line_count"].(float64) != 1 {
			t.Fatalf("invalid line not counted: %v", fl)
		}
	}
	var allow map[string]any
	api.Must("GET", "/allowlist", nil, &allow, 200)
	api.Must("PUT", "/allowlist", map[string]any{"domains": []string{"ok.ads-adblock.test"}, "revision": allow["revision"]}, nil, 200)

	eng := env.StartManagedEngine("engine-filter", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	v := api.LatestVersion()
	api.WaitEngine("engine-filter", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "unlisted.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("unlisted name did not resolve normally: %v", ip)
	}
	for _, name := range []string{"ads-hosts.test.", "x.tracker-hosts.test.", "ads-adblock.test.", "deep.sub.ads-domains.test."} {
		if ip := aRecord(t, harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
			t.Fatalf("listed name %s resolved to %v", name, ip)
		}
	}
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "ok.ads-adblock.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("allowlisted name was blocked: %v", ip)
	}
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, harness.UniqueName("cloak"), dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("CNAME-cloaked tracker was not blocked: %v", ip)
	}
	if eng.Metric(t, "nexora_filter_blocked_total", nil) < 4 {
		t.Fatal("blocked counter did not increase")
	}

	web.SetList(t, "domains", "new-ads.test\n")
	web.SetFailing(t, "domains", true)
	var fl map[string]any
	api.Must("POST", "/filter-lists/"+ids["domains"]+"/refresh", nil, &fl, 200)
	if fl["last_error"].(string) == "" || fl["stale"] != true || fl["entry_count"].(float64) != 2 {
		t.Fatalf("failed refresh not surfaced or list emptied: %v", fl)
	}
	time.Sleep(time.Second)
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "again.ads-domains.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("fetch error emptied the list: again.ads-domains.test resolved to %v", ip)
	}

	web.SetFailing(t, "domains", false)
	api.Must("POST", "/filter-lists/"+ids["domains"]+"/refresh", nil, &fl, 200)
	v2 := api.LatestVersion()
	api.WaitEngine("engine-filter", 10*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v2 })
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "new-ads.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("successful refresh not applied: %v", ip)
	}
}
