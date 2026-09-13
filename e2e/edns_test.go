package e2e

import (
	"bytes"
	"testing"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func cookieOf(t *testing.T, m *dns.Msg) string {
	t.Helper()
	if o := m.IsEdns0(); o != nil {
		for _, opt := range o.Option {
			if c, ok := opt.(*dns.EDNS0_COOKIE); ok {
				return c.Cookie
			}
		}
	}
	return ""
}

func TestEDNSTruncationTCP(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP)), nil)

	// Positive path first: a small answer over UDP is complete.
	small := harness.MustQuery(t, eng.DNS, harness.UniqueName("small"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232})
	if small.Truncated || len(small.Answer) != 1 {
		t.Fatalf("small answer: %v", small)
	}

	big := harness.UniqueName("big")
	udp := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{EDNSSize: 1232})
	if !udp.Truncated || len(udp.Answer) != 0 {
		t.Fatalf("oversized UDP answer must be TC=1 with no answers: tc=%v answers=%d", udp.Truncated, len(udp.Answer))
	}
	tcp := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{TCP: true, EDNSSize: 1232})
	if tcp.Truncated || len(tcp.Answer) != 100 {
		t.Fatalf("TCP answer: tc=%v answers=%d", tcp.Truncated, len(tcp.Answer))
	}
	noEDNS := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{})
	if !noEDNS.Truncated {
		t.Fatal("answer over 512 bytes without EDNS served without TC")
	}
	if fx.Count(t, big, dns.TypeA) != 1 {
		t.Fatalf("big answer fetched %d times; the cached full answer must serve TCP and UDP", fx.Count(t, big, dns.TypeA))
	}

	// Upstream truncation: engine retries over TCP and does not cache the truncated reply.
	tc := harness.UniqueName("tc")
	viaTCP := harness.MustQuery(t, eng.DNS, tc, dns.TypeA, harness.QueryOpts{TCP: true})
	if len(viaTCP.Answer) != 100 {
		t.Fatalf("engine did not retry a truncated upstream reply over TCP: %d answers", len(viaTCP.Answer))
	}
	again := harness.MustQuery(t, eng.DNS, tc, dns.TypeA, harness.QueryOpts{TCP: true})
	if len(again.Answer) != 100 {
		t.Fatalf("a truncated upstream reply was cached: %d answers", len(again.Answer))
	}

	// DNS cookies are echoed.
	client := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	r := harness.MustQuery(t, eng.DNS, harness.UniqueName("cookie"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232, Cookie: client})
	c := cookieOf(t, r)
	if len(c) != 48 || c[:16] != "0102030405060708" {
		t.Fatalf("cookie not echoed with a server cookie: %q", c)
	}
	full, _ := hexDecode(c)
	r2 := harness.MustQuery(t, eng.DNS, harness.UniqueName("cookie2"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232, Cookie: full})
	if c2 := cookieOf(t, r2); len(c2) != 48 || !bytes.Equal([]byte(c2[:16]), []byte(c[:16])) {
		t.Fatalf("second cookie exchange: %q", c2)
	}
}
