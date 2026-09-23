package e2e

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

// The real standalone engine must preserve legacy .local forwarding both for
// snapshots from older management and an explicitly disabled M8 gateway.
func TestMdnsOffForwardsLocalNames(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "absent"
		if explicit {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			env := harness.New(t)
			fx := env.StartDNSFixture()
			snap := harness.BaseSnapshot(1, harness.UDPUpstream("fixture", fx.UDP))
			if explicit {
				snap.Mdns = &controlv1.MdnsConfig{Enabled: false}
			}
			eng := env.StartStandaloneEngine(snap, nil)
			qname := "legacy-" + name + ".local."
			for _, network := range []string{"udp", "tcp"} {
				response := harness.MustQuery(t, eng.DNS, qname, dns.TypeA, harness.QueryOpts{TCP: network == "tcp"})
				if response.Rcode != dns.RcodeSuccess || len(response.Answer) != 1 {
					t.Fatalf("%s: %v", network, response)
				}
				if _, ok := response.Answer[0].(*dns.A); !ok {
					t.Fatalf("expected upstream A: %v", response)
				}
			}
			if got := fx.Count(t, qname, dns.TypeA); got != 1 {
				t.Fatalf("upstream count=%d, want one miss then cache hit", got)
			}
		})
	}
}
