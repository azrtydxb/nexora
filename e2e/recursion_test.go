package e2e

import (
	"testing"

	"github.com/miekg/dns"
)

func TestRecursionRootHints(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	var upstreams []map[string]any
	r.api.Must("GET", "/upstreams", nil, &upstreams, 200)
	if len(upstreams) != 0 {
		t.Fatalf("test requires no forwarders configured, found %d", len(upstreams))
	}

	t.Run("resolves from root hints", func(t *testing.T) {
		before := r.h.Stats(t).Queries["127.0.53.1"]
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")
		if r.h.Stats(t).Queries["127.0.53.1"] <= before {
			t.Fatal("fake root was never queried")
		}
		if r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "recursive"}) < 1 {
			t.Fatal("nexora_resolutions_total{route=recursive} did not increase")
		}
	})
	t.Run("cname chain", func(t *testing.T) {
		m := query(t, addr, "alias.good.test", dns.TypeA, qopt{})
		wantA(t, m, "192.0.2.10")
		if _, ok := m.Answer[0].(*dns.CNAME); !ok {
			t.Fatalf("first answer is not the CNAME: %v", m.Answer)
		}
	})
	t.Run("glueless delegation", func(t *testing.T) {
		wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	})
	t.Run("tcp fallback on truncated authoritative reply", func(t *testing.T) {
		m := query(t, addr, "big.good.test", dns.TypeTXT, qopt{TCP: true})
		if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 40 {
			t.Fatalf("big TXT over TCP: rcode=%s n=%d", dns.RcodeToString[m.Rcode], len(m.Answer))
		}
		if r.eng.Metric(t, "nexora_recursor_tcp_fallback_total", nil) < 1 {
			t.Fatal("engine did not retry the truncated authoritative reply over TCP")
		}
	})
	t.Run("nxdomain", func(t *testing.T) {
		if m := query(t, addr, "nope.plain.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("out-of-bailiwick glue is not used", func(t *testing.T) {
		wantA(t, query(t, addr, "www.poison.test", dns.TypeA, qopt{}), "192.0.2.30")
		_ = query(t, addr, "www.sub.poison.test", dns.TypeA, qopt{})
		wantA(t, query(t, addr, "ns.good.test", dns.TypeA, qopt{}), "127.0.53.3")
		if n := r.h.Stats(t).Queries["127.0.53.66"]; n != 0 {
			t.Fatalf("engine sent %d queries to the poisoned glue address", n)
		}
	})
	t.Run("fixture servers preserve 0x20 case so no case mismatches occur", func(t *testing.T) {
		if n := r.eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": "case"}); n != 0 {
			t.Fatalf("case mismatches = %v, want 0 against honest servers", n)
		}
		// the counter is live in this run: the spoofing server's lower-cased forgery is counted
		wantA(t, query(t, addr, "www.spoof.test", dns.TypeA, qopt{}), "192.0.2.77")
		if n := r.eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": "case"}); n < 1 {
			t.Fatalf("case mismatches = %v after querying the spoofing server, want >= 1", n)
		}
	})
}

func TestSpoofedReplyRejected(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	// positive path first: the hierarchy and recursion work in this run
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")

	m := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, m, "192.0.2.77")
	for _, v := range aValues(m) {
		if v == "6.6.6.6" {
			t.Fatal("spoofed address served")
		}
	}
	st := r.h.Stats(t)
	if st.SpoofsSent < 4 {
		t.Fatalf("fixture sent %d spoofs, want >= 4", st.SpoofsSent)
	}
	for _, reason := range []string{"id", "question", "case"} {
		if r.eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": reason}) < 1 {
			t.Fatalf("mismatched reply with wrong %s was not counted", reason)
		}
	}
	spoofQueries := st.Queries["127.0.53.7"]
	again := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, again, "192.0.2.77")
	if got := r.h.Stats(t).Queries["127.0.53.7"]; got != spoofQueries {
		t.Fatalf("second query reached the authoritative server (%d -> %d); the cached answer must be the real one", spoofQueries, got)
	}
	if again.Answer[len(again.Answer)-1].Header().Ttl >= m.Answer[len(m.Answer)-1].Header().Ttl+1 {
		t.Fatal("cached TTL not decremented")
	}
}
