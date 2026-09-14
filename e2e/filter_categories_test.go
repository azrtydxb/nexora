package e2e

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// The DNS fixture answers every unlisted A query with 192.0.2.1; blocked names get 0.0.0.0 (null_ip).
const unblocked = "192.0.2.1"

type categoryStack struct {
	env   *harness.Env
	mg    *harness.Mgmt
	api   *harness.API
	lists *harness.HTTPFixture
	eng   *harness.Engine
	node  string
}

func startCategoryStack(t *testing.T, node string, opts func(env *harness.Env) harness.MgmtOptions) categoryStack {
	t.Helper()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	lists := env.StartHTTPFixture()
	o := harness.MgmtOptions{}
	if opts != nil {
		o = opts(env)
	}
	o.DNSTLS = true
	o.ExtraEnv = append(o.ExtraEnv, harness.CatalogMirrorEnv(lists))
	mg := env.StartMgmt(pg, ca, o)
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
	fx := env.StartDNSFixture()
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngineWith(node, []string{mg.GRPCURL}, api.CreateJoinToken(), harness.EngineOptions{DoH: true})
	waitLatestApplied(t, api, node)
	return categoryStack{env: env, mg: mg, api: api, lists: lists, eng: eng, node: node}
}

// answer queries name (A) from ip over udp, tcp or doh and returns the first address.
func (s categoryStack) answer(t *testing.T, transport, ip, name string) string {
	t.Helper()
	switch transport {
	case "udp":
		return firstA(udpFrom(t, ip, s.eng.DNS, name, dns.TypeA))
	case "tcp":
		c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second, Dialer: &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}}
		m, _, err := c.Exchange(question(name, dns.TypeA), s.eng.DNS)
		if err != nil {
			t.Fatalf("tcp %s from %s: %v", name, ip, err)
		}
		return firstA(m)
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := harness.EncryptedClient{RootCAs: s.mg.DNSTLSRoots(), ServerName: "dns.nexora.test", LocalIP: net.ParseIP(ip)}
		m, _, err := harness.DoH(ctx, c.HTTPClient(), s.eng.DoHURL(), http.MethodPost, question(name, dns.TypeA))
		if err != nil {
			t.Fatalf("doh %s from %s: %v", name, ip, err)
		}
		return firstA(m)
	}
}

// want waits until UDP answers name from ip with addr, then requires TCP and DoH to agree.
func (s categoryStack) want(t *testing.T, ip, name, addr string) {
	t.Helper()
	harness.EventuallyTrue(t, 20*time.Second, func() bool { return s.answer(t, "udp", ip, name) == addr }, name+" from "+ip+" over udp = "+addr)
	for _, tr := range []string{"tcp", "doh"} {
		if got := s.answer(t, tr, ip, name); got != addr {
			t.Fatalf("%s from %s over %s = %q, want %s", name, ip, tr, got, addr)
		}
	}
}

func TestFilterCategories(t *testing.T) {
	s := startCategoryStack(t, "cat-1", nil)
	s.lists.SetList(t, "hagezi-pro", "ads.cat.test\n")
	s.lists.SetList(t, "oisd-big", "*.oisd.cat.test\n")
	s.lists.SetList(t, "hagezi-gambling", "casino.cat.test\n")

	// Positive path first: with every category off nothing is blocked, for either client.
	for _, ip := range []string{"127.0.0.1", "127.0.0.2"} {
		for _, name := range []string{"ads.cat.test.", "x.oisd.cat.test.", "casino.cat.test."} {
			s.want(t, ip, name, unblocked)
		}
	}

	if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
		t.Fatalf("enable ads-tracking globally -> %d %s", code, reason)
	}
	s.api.RefreshCategory("ads-tracking")
	s.api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "kids", "cidrs": []string{"127.0.0.2/32"}, "category_keys": []string{"gambling"}}, nil, http.StatusCreated)
	s.api.RefreshCategory("gambling")
	waitLatestApplied(t, s.api, s.node)

	// Global clients: the global category blocks, including the OISD wildcard entry; gambling is only the group's.
	s.want(t, "127.0.0.1", "ads.cat.test.", "0.0.0.0")
	s.want(t, "127.0.0.1", "deep.x.oisd.cat.test.", "0.0.0.0")
	s.want(t, "127.0.0.1", "casino.cat.test.", unblocked)
	// The group client: its category blocks and its lists replace the global ones.
	s.want(t, "127.0.0.2", "casino.cat.test.", "0.0.0.0")
	s.want(t, "127.0.0.2", "www.casino.cat.test.", "0.0.0.0")
	s.want(t, "127.0.0.2", "ads.cat.test.", unblocked)
}

func TestFilterCategoryToggles(t *testing.T) {
	s := startCategoryStack(t, "cat-2", nil)
	s.lists.SetList(t, "hagezi-gambling", "casino.toggle.test\n")
	s.lists.SetList(t, "blp-gambling", "bets.toggle.test\n")
	s.lists.SetList(t, "oisd-big", "*.oisd.toggle.test\n")
	s.want(t, "127.0.0.1", "casino.toggle.test.", unblocked)
	s.want(t, "127.0.0.1", "bets.toggle.test.", unblocked)

	if code, reason := s.api.SetFilterCategory("gambling", true, nil, false); code != http.StatusOK {
		t.Fatalf("enable gambling -> %d %s", code, reason)
	}
	s.api.RefreshCategory("gambling")
	waitLatestApplied(t, s.api, s.node)
	s.want(t, "127.0.0.1", "casino.toggle.test.", "0.0.0.0")
	s.want(t, "127.0.0.1", "bets.toggle.test.", "0.0.0.0")

	// One source off: its names resolve again, the other source keeps blocking.
	if code, reason := s.api.SetFilterCategory("gambling", true, map[string]bool{"blp-gambling": false}, false); code != http.StatusOK {
		t.Fatalf("disable blp-gambling -> %d %s", code, reason)
	}
	waitLatestApplied(t, s.api, s.node)
	s.want(t, "127.0.0.1", "bets.toggle.test.", unblocked)
	s.want(t, "127.0.0.1", "casino.toggle.test.", "0.0.0.0")

	// The category off: nothing of it blocks.
	if code, reason := s.api.SetFilterCategory("gambling", false, nil, false); code != http.StatusOK {
		t.Fatalf("disable gambling -> %d %s", code, reason)
	}
	waitLatestApplied(t, s.api, s.node)
	s.want(t, "127.0.0.1", "casino.toggle.test.", unblocked)

	t.Run("oisd-license-guard", func(t *testing.T) {
		code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, false)
		if code != http.StatusUnprocessableEntity || reason != "license_acknowledgement_required" {
			t.Fatalf("OISD without acknowledge_license -> %d %s", code, reason)
		}
		if s.api.FilterCategory("ads-tracking").Enabled {
			t.Fatal("the refused request enabled ads-tracking")
		}
		if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
			t.Fatalf("OISD with acknowledge_license -> %d %s", code, reason)
		}
		s.api.RefreshSource("ads-tracking", "oisd-big")
		waitLatestApplied(t, s.api, s.node)
		s.want(t, "127.0.0.1", "x.oisd.toggle.test.", "0.0.0.0")
		var events []struct {
			Action   string         `json:"action"`
			TargetID string         `json:"target_id"`
			Diff     map[string]any `json:"diff"`
		}
		s.api.Must(http.MethodGet, "/audit?limit=50", nil, &events, http.StatusOK)
		found := false
		for _, e := range events {
			after, _ := e.Diff["after"].(map[string]any)
			acks, _ := after["acknowledged_licenses"].([]any)
			if e.Action == "updateFilterCategory" && e.TargetID == "ads-tracking" && len(acks) == 1 && acks[0] == "oisd-big" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no audit entry records the OISD acknowledgement: %+v", events)
		}
	})

	t.Run("catalog-is-read-only", func(t *testing.T) {
		before := s.api.FilterCategory("ads-tracking")
		code, reason := s.api.ErrorCode(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": before.Revision,
			"sources": []map[string]any{{"key": "my-own-source", "enabled": true}}})
		if code != http.StatusUnprocessableEntity || reason != "unknown_source" {
			t.Fatalf("custom source -> %d %s", code, reason)
		}
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/filter-categories"},
			{http.MethodDelete, "/filter-categories/ads-tracking"},
			{http.MethodPost, "/filter-categories/ads-tracking/sources"},
			{http.MethodPatch, "/filter-categories/ads-tracking"},
		} {
			if code, _ := s.api.Do(c.method, c.path, map[string]any{"key": "mine", "url": "https://evil.test/list.txt"}, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s -> %d, want 404 or 405", c.method, c.path, code)
			}
		}
		managed := before.Sources[0].ListID
		code, reason = s.api.ErrorCode(http.MethodPut, "/filter-lists/"+managed, map[string]any{"name": "hijack", "kind": "block", "url": "https://evil.test/list.txt",
			"refresh_interval_seconds": 3600, "enabled": true, "revision": 1})
		if code != http.StatusUnprocessableEntity || reason != "catalog_managed" {
			t.Fatalf("editing a catalog list -> %d %s", code, reason)
		}
		after := s.api.FilterCategory("ads-tracking")
		if len(after.Sources) != len(before.Sources) {
			t.Fatalf("sources %d -> %d", len(before.Sources), len(after.Sources))
		}
		for i := range after.Sources {
			if after.Sources[i].URL != before.Sources[i].URL || strings.Contains(after.Sources[i].URL, "evil.test") {
				t.Fatalf("source %s url changed to %s", after.Sources[i].Key, after.Sources[i].URL)
			}
		}
	})
}
