package e2e

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestForwardCacheTTL(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	cases := []struct {
		name string
		up   *controlv1.Upstream
	}{
		{"udp", harness.UDPUpstream("udp", fx.UDP)},
		{"dot", harness.DoTUpstream("dot", fx)},
		{"doh", harness.DoHUpstream("doh", fx)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, tc.up), nil)
			name := harness.UniqueName("ttl-" + tc.name)
			first := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
			if first.Rcode != dns.RcodeSuccess || len(first.Answer) != 1 {
				t.Fatalf("first answer: %v", first)
			}
			if got := fx.Count(t, name, dns.TypeA); got != 1 {
				t.Fatalf("miss must reach the upstream once, count=%d", got)
			}
			firstTTL := first.Answer[0].Header().Ttl
			time.Sleep(2200 * time.Millisecond)
			second := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
			if got := fx.Count(t, name, dns.TypeA); got != 1 {
				t.Fatalf("second query reached the upstream: count=%d", got)
			}
			if len(second.Answer) != 1 || second.Answer[0].Header().Ttl >= firstTTL {
				t.Fatalf("TTL not decremented: first=%d second=%v", firstTTL, second.Answer)
			}
			if hits := eng.Metric(t, "nexora_cache_hits_total", nil); hits < 1 {
				t.Fatalf("cache hit not counted: %v", hits)
			}
		})
	}
}

func TestUpstreamFailover(t *testing.T) {
	env := harness.New(t)
	primary := env.StartDNSFixture()
	secondary := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1,
		harness.UDPUpstream("primary", primary.UDP), harness.UDPUpstream("secondary", secondary.UDP)), nil)

	warm := harness.UniqueName("warm")
	harness.MustQuery(t, eng.DNS, warm, dns.TypeA, harness.QueryOpts{})
	if primary.Count(t, warm, dns.TypeA) != 1 {
		t.Fatal("ordered strategy must use the primary while it is healthy")
	}

	primary.SetMode(t, "blackhole")
	for i := 0; i < 50; i++ {
		name := harness.UniqueName("failover")
		r, rtt, err := harness.Query(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{Timeout: time.Second})
		if err != nil {
			t.Fatalf("query %d failed within 1s: %v", i, err)
		}
		if r.Rcode != dns.RcodeSuccess {
			t.Fatalf("query %d got %s while secondary is healthy", i, dns.RcodeToString[r.Rcode])
		}
		if rtt > time.Second {
			t.Fatalf("query %d took %v", i, rtt)
		}
	}
	if secondary.Total(t) < 50 {
		t.Fatalf("secondary served %d queries", secondary.Total(t))
	}
	if up := eng.Metric(t, "nexora_upstream_up", map[string]string{"upstream": "primary"}); up != 0 {
		t.Fatalf("primary still reported up: %v", up)
	}
}

func TestDedupAllWaitersAnswered(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	// The per-attempt timeout must exceed the injected delay, or the one upstream query
	// times out by design (architecture: per-attempt timeout = timeout_ms).
	up := harness.UDPUpstream("fx", fx.UDP)
	up.TimeoutMs = 1500
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, up), nil)
	fx.SetDelay(t, 500*time.Millisecond)
	name := harness.UniqueName("herd")

	const clients = 1000
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, _, err := harness.Query(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{Timeout: 3 * time.Second})
			if err != nil {
				errs <- err
				return
			}
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
				errs <- fmt.Errorf("rcode=%s answers=%d", dns.RcodeToString[r.Rcode], len(r.Answer))
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		failed++
		if failed <= 5 {
			t.Logf("client error: %v", err)
		}
	}
	got := fx.Count(t, name, dns.TypeA)
	if got < 1 {
		t.Fatal("upstream never saw the query")
	}
	if failed > 0 {
		t.Fatalf("%d of %d clients were not answered", failed, clients)
	}
	if got != 1 {
		t.Fatalf("upstream queries = %d, want exactly 1", got)
	}
}
