package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

const mdnsCollection = 500 * time.Millisecond

// Every transport uses distinct names: TCP must prove a multicast miss of its
// own, rather than merely reading the answer cached by the UDP test.
func TestMdnsGatewayNetLab(t *testing.T) {
	if !harness.InNetLab() {
		harness.RunInNetLab(t)
		return
	}
	e := harness.New(t)
	lab := e.NewNetLab()
	lab.Link("gwA", "lanA0", "lanA", 1)
	lab.Link("gwB", "lanB0", "lanB", 2)
	mdnsIPv6Link(lab, "gwA", "lanA", "lanA0", 1)
	mdnsIPv6Link(lab, "gwB", "lanB", "lanB0", 2)
	// The first phase must prove IPv4 delivery, without IPv6 masking a broken
	// IPv4 path. Link-local IPv6 is restored explicitly for the final phase.
	for _, iface := range []string{"gwA", "gwB"} {
		lab.CommandIn("", "ip", "-6", "addr", "flush", "dev", iface, "scope", "link")
	}
	argsA := []string{"mdns-responder", "--interface", "lanA0"}
	argsB := []string{"mdns-responder", "--interface", "lanB0"}
	for _, transport := range []string{"udp", "tcp"} {
		argsA = append(argsA,
			"--record", transport+".ipv6.local. 120 IN A 10.254.1.6",
			"--record", transport+".ipv6.local. 120 IN AAAA fd00::6",
			"--record", transport+".printer.local. 120 IN A 10.254.1.9",
			"--record", transport+".printer.local. 120 IN AAAA fd00::9",
			"--record", "_"+transport+"._tcp.local. 4500 IN PTR one._ipp._tcp.local.",
			"--record", "_"+transport+"._tcp.local. 4500 IN PTR two._ipp._tcp.local.",
			"--record", transport+".kw.local. 120 IN A 10.254.1.99")
		argsB = append(argsB,
			"--record", "_"+transport+"._tcp.local. 4500 IN PTR two._ipp._tcp.local.",
			"--record", "_"+transport+"._tcp.local. 4500 IN PTR three._ipp._tcp.local.")
	}
	argsA = append(argsA, "--record", "moving.local. 120 IN A 10.254.1.9")
	argsB = append(argsB, "--record", "moving.local. 120 IN A 10.254.2.9")
	responderA := lab.StartIn("lanA", "nexora-fixture", argsA...)
	lab.StartIn("lanB", "nexora-fixture", argsB...)
	upstream := e.StartDNSFixture()
	upstream.SetRecords(t, "udp.kw.local. 60 IN A 192.0.2.53", "tcp.kw.local. 60 IN A 192.0.2.53", "moving.local. 60 IN A 192.0.2.54")
	snap := harness.BaseSnapshot(1, harness.UDPUpstream("ordinary", upstream.UDP))
	snap.Mdns = &controlv1.MdnsConfig{Enabled: true, Interfaces: []string{"gwA", "gwB", "nxmissing0"}, TimeoutMs: uint32(mdnsCollection / time.Millisecond)}
	snap.ForwardZones = []*controlv1.ForwardZone{{Domain: "kw.local.", Addresses: []string{upstream.UDP}}}
	en := e.StartStandaloneEngine(snap, nil)
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			tcp := transport == "tcp"
			name := transport + ".printer.local."
			mdnsAnswer(t, mdnsExchange(t, en, name, dns.TypeA, tcp, false), name+" 10 IN A 10.254.1.9")
			mdnsAnswer(t, mdnsExchange(t, en, name, dns.TypeAAAA, tcp, false), name+" 10 IN AAAA fd00::9")
			ptr := "_" + transport + "._tcp.local."
			mdnsAnswer(t, mdnsExchange(t, en, ptr, dns.TypePTR, tcp, true),
				ptr+" 10 IN PTR one._ipp._tcp.local.", ptr+" 10 IN PTR two._ipp._tcp.local.", ptr+" 10 IN PTR three._ipp._tcp.local.")
			// A repeated NXDOMAIN must still wait and count another miss: no SOA means
			// the absence of an mDNS responder must not become a negative cache entry.
			for i := 0; i < 2; i++ {
				mdnsNoAnswer(t, mdnsExchange(t, en, transport+".nothere.local.", dns.TypeA, tcp, true))
			}
			for _, q := range []struct {
				name string
				typ  uint16
			}{{name, dns.TypeA}, {name, dns.TypeAAAA}, {ptr, dns.TypePTR}, {transport + ".nothere.local.", dns.TypeA}} {
				if n := upstream.Count(t, q.name, q.typ); n != 0 {
					t.Fatalf("enabled mDNS leaked %s/%s to ordinary upstream: %d", q.name, dns.TypeToString[q.typ], n)
				}
			}
			forwardName := transport + ".kw.local."
			r := mdnsExchange(t, en, forwardName, dns.TypeA, tcp, false)
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 || !strings.EqualFold(r.Answer[0].String(), forwardName+"\t60\tIN\tA\t192.0.2.53") {
				t.Fatalf("forward zone lost precedence: %v", r)
			}
			if upstream.Count(t, forwardName, dns.TypeA) != 1 {
				t.Fatal("forward zone did not reach configured upstream exactly once")
			}
			if strings.Contains(mdnsLog(t, responderA), " "+forwardName+" A") {
				t.Fatal("forward-zone query also reached multicast responder")
			}
		})
	}
	if got := en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "answered"}); got != 6 {
		t.Fatalf("answered=%g, want 6 cold multicast queries", got)
	}
	if got := en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "unanswered"}); got != 4 {
		t.Fatalf("unanswered=%g, want 4 uncached misses", got)
	}
	if got := en.Metric(t, "nexora_mdns_interface_missing", map[string]string{"interface": "nxmissing0"}); got != 1 {
		t.Fatalf("missing interface metric=%g", got)
	}

	// Pin each configuration to one LAN. The same cached key must change its
	// answer immediately on interface reconfiguration, disable, and re-enable.
	reload := func(c *controlv1.MdnsConfig) { snap.Version++; snap.Mdns = c; en.Reload(t, snap, nil) }
	gateway := func(iface string) *controlv1.MdnsConfig {
		return &controlv1.MdnsConfig{Enabled: true, Interfaces: []string{iface}, TimeoutMs: uint32(mdnsCollection / time.Millisecond)}
	}
	reload(gateway("gwA"))
	cachedAt := time.Now()
	mdnsAnswer(t, mdnsExchange(t, en, "moving.local.", dns.TypeA, false, false), "moving.local. 10 IN A 10.254.1.9")
	before := en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "answered"})
	mdnsAnswer(t, mdnsExchange(t, en, "moving.local.", dns.TypeA, true, false), "moving.local. 10 IN A 10.254.1.9")
	if got := en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "answered"}); got != before {
		t.Fatalf("warm query ran gateway again: %g -> %g", before, got)
	}
	reload(gateway("gwB"))
	mdnsAnswer(t, mdnsExchange(t, en, "moving.local.", dns.TypeA, true, false), "moving.local. 10 IN A 10.254.2.9")
	if upstream.Count(t, "moving.local.", dns.TypeA) != 0 {
		t.Fatal("interface reconfiguration leaked to upstream")
	}
	reload(&controlv1.MdnsConfig{Enabled: false})
	r := mdnsExchange(t, en, "moving.local.", dns.TypeA, false, false)
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 || !strings.EqualFold(r.Answer[0].String(), "moving.local.\t60\tIN\tA\t192.0.2.54") {
		t.Fatalf("disable retained multicast cache: %v", r)
	}
	if upstream.Count(t, "moving.local.", dns.TypeA) != 1 {
		t.Fatal("disabled gateway did not forward cached name")
	}
	reload(gateway("gwA"))
	mdnsAnswer(t, mdnsExchange(t, en, "moving.local.", dns.TypeA, true, false), "moving.local. 10 IN A 10.254.1.9")
	if upstream.Count(t, "moving.local.", dns.TypeA) != 1 {
		t.Fatal("re-enabled gateway reached upstream")
	}
	if time.Since(cachedAt) >= 10*time.Second {
		t.Fatal("lifecycle checks exceeded the original cache TTL; invalidation was not proven")
	}

	// Force IPv6 transport, not just AAAA RDATA over IPv4. The fixture keeps its
	// IPv4 address for its READY contract; the gateway has only a link-local IPv6.
	lab.CommandIn("", "ip", "-4", "addr", "del", "10.254.1.1/24", "dev", "gwA")
	mdnsIPv6Link(lab, "gwA", "lanA", "lanA0", 1)
	reload(gateway("gwA"))
	for _, transport := range []string{"udp", "tcp"} {
		name := transport + ".ipv6.local."
		mdnsAnswer(t, mdnsExchange(t, en, name, dns.TypeA, transport == "tcp", false), name+" 10 IN A 10.254.1.6")
		mdnsAnswer(t, mdnsExchange(t, en, name, dns.TypeAAAA, transport == "tcp", false), name+" 10 IN AAAA fd00::6")
		for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA} {
			if upstream.Count(t, name, typ) != 0 {
				t.Fatalf("IPv6 multicast query %s/%s reached upstream", name, dns.TypeToString[typ])
			}
		}
	}
	if !strings.Contains(mdnsLog(t, responderA), "GOT [fe80:") {
		t.Fatal("responder did not observe a real IPv6 query")
	}
	reload(gateway("nxmissing0"))
	// With no usable sockets the gateway can report absence immediately.
	mdnsNoAnswer(t, mdnsExchange(t, en, "missing-interface.local.", dns.TypeA, false, false))
	if upstream.Count(t, "missing-interface.local.", dns.TypeA) != 0 {
		t.Fatal("missing interface fell back to upstream")
	}
}

func mdnsExchange(t *testing.T, en *harness.Engine, name string, typ uint16, tcp, collect bool) *dns.Msg {
	t.Helper()
	r, elapsed, err := harness.Query(t, en.DNS, name, typ, harness.QueryOpts{TCP: tcp, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("original query %s %s (TCP=%v): %v", name, dns.TypeToString[typ], tcp, err)
	}
	if len(r.Question) != 1 || r.Question[0].Name != name || r.Question[0].Qtype != typ {
		t.Fatalf("question not preserved: %v", r)
	}
	if collect && elapsed < mdnsCollection {
		t.Fatalf("%s returned after %s, before collection timeout %s: %v", name, elapsed, mdnsCollection, r)
	}
	return r
}

func mdnsAnswer(t *testing.T, r *dns.Msg, records ...string) {
	t.Helper()
	if r.Rcode != dns.RcodeSuccess || r.Authoritative || r.Truncated || len(r.Answer) != len(records) {
		t.Fatalf("mDNS response: %v; want %v", r, records)
	}
	want := map[string]bool{}
	for _, s := range records {
		rr, err := dns.NewRR(s)
		if err != nil {
			t.Fatal(err)
		}
		rr.Header().Ttl = 0
		want[rr.String()] = true
	}
	for _, rr := range r.Answer {
		if rr.Header().Class != dns.ClassINET || rr.Header().Ttl > 10 || rr.Header().Ttl == 0 {
			t.Fatalf("TTL/class not normalized: %v", rr)
		}
		copy := dns.Copy(rr)
		copy.Header().Ttl = 0
		if !want[copy.String()] {
			t.Fatalf("unexpected/duplicate answer %v; want %v", rr, records)
		}
		delete(want, copy.String())
	}
}

func mdnsNoAnswer(t *testing.T, r *dns.Msg) {
	t.Helper()
	if r.Rcode != dns.RcodeNameError || len(r.Answer) != 0 || len(r.Ns) != 0 || r.Authoritative {
		t.Fatalf("want non-authoritative NXDOMAIN without SOA: %v", r)
	}
}

func mdnsLog(t *testing.T, p *harness.Proc) string {
	t.Helper()
	b, err := os.ReadFile(p.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Explicit nodad link-local addresses avoid racing Linux's automatic IPv6 DAD.
// Every command runs inside NetLab; no host interface or route is changed.
func mdnsIPv6Link(lab *harness.NetLab, local, ns, peer string, subnet int) {
	for _, end := range []struct {
		ns, iface string
		host      int
	}{{"", local, 1}, {ns, peer, 2}} {
		lab.CommandIn(end.ns, "ip", "-6", "addr", "flush", "dev", end.iface, "scope", "link")
		lab.CommandIn(end.ns, "ip", "-6", "addr", "add", fmt.Sprintf("fe80::%x:%d/64", subnet, end.host), "dev", end.iface, "nodad")
	}
}
