package e2e

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"golang.org/x/net/ipv6"
)

const mdnsIPv6ProbeEnv = "NEXORA_E2E_MDNS_IPV6_PROBE"

func TestMdnsReflectorNetLab(t *testing.T) {
	// The existing fixture querier supports IPv4 only. Re-exec this test inside
	// a LAN for the IPv6 probe; it sends one real multicast query, without retries.
	if iface := os.Getenv(mdnsIPv6ProbeEnv); iface != "" {
		if !harness.InNetLab() {
			t.Fatal("IPv6 probe outside NetLab")
		}
		mdnsReflectIPv6Probe(t, iface)
		return
	}
	if !harness.InNetLab() {
		harness.RunInNetLab(t)
		return
	}
	e := harness.New(t)
	lab := e.NewNetLab()
	for i, segment := range []string{"A", "B", "C"} {
		lab.Link("gw"+segment, "lan"+segment+"0", "lan"+segment, i+1)
		mdnsIPv6Link(lab, "gw"+segment, "lan"+segment, "lan"+segment+"0", i+1)
	}
	responder := lab.StartIn("lanA", "nexora-fixture", "mdns-responder", "--interface", "lanA0", "--record", "printer.local. 120 IN A 10.254.1.9")
	upstream := e.StartDNSFixture()
	snap := harness.BaseSnapshot(1, harness.UDPUpstream("ordinary", upstream.UDP))
	snap.Mdns = &controlv1.MdnsConfig{Reflect: true, ReflectInterfaces: []string{"gwA", "gwB"}}
	en := e.StartStandaloneEngine(snap, nil)
	waitGroups := func(iface string, joined bool) {
		harness.Eventually(t, 5*time.Second, func() error {
			out := lab.CommandIn("", "ip", "maddr", "show", "dev", iface)
			v4 := mdnsHasMembership(out, "inet", "224.0.0.251")
			v6 := mdnsHasMembership(out, "inet6", "ff02::fb")
			if v4 != joined || v6 != joined {
				return fmt.Errorf("%s memberships IPv4=%v IPv6=%v, want joined=%v: %s", iface, v4, v6, joined, out)
			}
			return nil
		})
	}
	probe := func(segment string, ipv6Transport bool, want int) {
		t.Helper()
		var out string
		if ipv6Transport {
			out = lab.CommandIn("lan"+segment, "env", mdnsIPv6ProbeEnv+"=lan"+segment+"0", os.Args[0], "-test.run=^TestMdnsReflectorNetLab$", "-test.count=1", "-test.timeout=6s")
		} else {
			out = lab.CommandIn("lan"+segment, e.Bin("nexora-fixture"), "mdns-query", "--interface", "lan"+segment+"0", "--name", "printer.local.", "--type", "A", "--wait", "2s")
		}
		mdnsProbeOutput(t, out, want)
	}
	waitGroups("gwA", true)
	waitGroups("gwB", true)
	waitGroups("gwC", false)
	// Each 2-second observation must contain exactly one answer (not PACKETS 10),
	// and the responder must not see its own reflected response on either family.
	probe("B", false, 1)
	probe("B", true, 1)
	for _, pair := range [][2]string{{"gwB", "gwA"}, {"gwA", "gwB"}} {
		if n := en.Metric(t, "nexora_mdns_reflected_packets_total", map[string]string{"from": pair[0], "to": pair[1]}); n != 2 {
			t.Fatalf("%s -> %s reflected %g packets, want one per family", pair[0], pair[1], n)
		}
	}
	mdnsNoEcho(t, responder, 2)
	// C is physically connected but not configured. Neither family may cross it.
	probe("C", false, 0)
	probe("C", true, 0)
	mdnsNoEcho(t, responder, 2)

	// A snapshot update must close B's memberships and move reflection to C.
	snap.Version++
	snap.Mdns.ReflectInterfaces = []string{"gwA", "gwC"}
	en.Reload(t, snap, nil)
	waitGroups("gwB", false)
	waitGroups("gwA", true)
	waitGroups("gwC", true)
	probe("B", false, 0)
	probe("B", true, 0)
	mdnsNoEcho(t, responder, 2)
	probe("C", false, 1)
	probe("C", true, 1)
	mdnsNoEcho(t, responder, 4)
	for _, pair := range [][2]string{{"gwC", "gwA"}, {"gwA", "gwC"}} {
		if n := en.Metric(t, "nexora_mdns_reflected_packets_total", map[string]string{"from": pair[0], "to": pair[1]}); n != 2 {
			t.Fatalf("reconfigured %s -> %s reflected %g packets, want 2", pair[0], pair[1], n)
		}
	}
	snap.Version++
	snap.Mdns = nil
	en.Reload(t, snap, nil)
	waitGroups("gwA", false)
	waitGroups("gwC", false)
	probe("C", false, 0)
	probe("C", true, 0)
	mdnsNoEcho(t, responder, 4)
	if n := upstream.Total(t); n != 0 {
		t.Fatalf("reflection generated %d ordinary upstream queries", n)
	}
}

func mdnsHasMembership(out, family, address string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == family && fields[1] == address {
			return true
		}
	}
	return false
}

func TestMdnsMembershipOutputParsing(t *testing.T) {
	for _, out := range []string{"inet 224.0.0.251", "\tinet  224.0.0.251\n", "inet\t224.0.0.251 users 2"} {
		if !mdnsHasMembership(out, "inet", "224.0.0.251") {
			t.Fatalf("missed exact IPv4 membership in %q", out)
		}
	}
	for _, out := range []string{"inet 224.0.0.2510", "inet6 ff02::fb", "link 01:00:5e:00:00:fb", ""} {
		if mdnsHasMembership(out, "inet", "224.0.0.251") {
			t.Fatalf("accepted non-membership in %q", out)
		}
	}
}

func mdnsProbeOutput(t *testing.T, out string, want int) {
	t.Helper()
	var packets, answers int
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "PACKETS ") {
			packets++
			if line != fmt.Sprintf("PACKETS %d", want) {
				t.Fatalf("reflected packet count: want %d\n%s", want, out)
			}
		}
		if strings.HasPrefix(line, "ANSWER ") {
			answers++
			if line != "ANSWER printer.local.\t120\tIN\tA\t10.254.1.9" {
				t.Fatalf("reflector changed answer payload:\n%s", out)
			}
		}
	}
	if packets != 1 || answers != want {
		t.Fatalf("want one count line and %d exact answers:\n%s", want, out)
	}
}

func mdnsNoEcho(t *testing.T, p *harness.Proc, wantQueries int) {
	t.Helper()
	log := mdnsLog(t, p)
	if strings.Contains(log, "ECHO ") || strings.Contains(log, "send to ") {
		t.Fatalf("responder echo/send failure:\n%s", log)
	}
	if got := strings.Count(log, " printer.local. A\n"); got != wantQueries {
		t.Fatalf("responder saw %d queries, want %d (loop or cross-interface leak):\n%s", got, wantQueries, log)
	}
}

// A real port-5353 IPv6 mDNS client. This complements (and does not replace)
// nexora-fixture mdns-query, whose existing CLI only sends IPv4. The fixture
// responder remains the source of all answers in both address families.
func mdnsReflectIPv6Probe(t *testing.T, ifName string) {
	t.Helper()
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenPacket("udp6", "[::]:5353")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := ipv6.NewPacketConn(c)
	group := &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353, Zone: ifName}
	for _, err := range []error{p.JoinGroup(iface, group), p.SetMulticastInterface(iface), p.SetMulticastLoopback(false), p.SetMulticastHopLimit(255), p.SetControlMessage(ipv6.FlagInterface|ipv6.FlagHopLimit, true)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	q := new(dns.Msg)
	q.SetQuestion("printer.local.", dns.TypeA)
	q.Id = 0
	q.RecursionDesired = false
	wire, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.WriteTo(wire, &ipv6.ControlMessage{IfIndex: iface.Index}, group); err != nil {
		t.Fatal(err)
	}
	if err = p.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 9000)
	packets := 0
	for {
		n, cm, src, err := p.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			t.Fatal(err)
		}
		if cm == nil || cm.IfIndex != iface.Index {
			t.Fatalf("reply on wrong interface: %+v", cm)
		}
		r := new(dns.Msg)
		if err := r.Unpack(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if !r.Response {
			continue
		}
		if cm.HopLimit != 255 || src.(*net.UDPAddr).Port != 5353 || r.Id != 0 || len(r.Question) != 0 || r.Rcode != dns.RcodeSuccess || !r.Authoritative || len(r.Answer) != 1 {
			t.Fatalf("invalid reflected multicast response from %s (hop=%d): %v", src, cm.HopLimit, r)
		}
		rr := r.Answer[0]
		if rr.Header().Class != dns.ClassINET|0x8000 {
			t.Fatalf("reflector changed cache-flush class: %v", rr)
		}
		rr.Header().Class &^= 0x8000
		fmt.Println("ANSWER " + rr.String())
		packets++
	}
	fmt.Printf("PACKETS %d\n", packets)
}
