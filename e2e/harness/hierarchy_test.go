package harness

import (
	"net"
	"strconv"
	"testing"

	"github.com/miekg/dns"
)

func TestHierarchyHarnessReadsReadyAndStats(t *testing.T) {
	env := New(t)
	h := env.StartHierarchy()
	if h.Ready.Port == 0 || h.Ready.RootDS == "" || len(h.Ready.RootHints) != 1 || h.Ready.RootHints[0].Addresses[0] != "127.0.53.1" {
		t.Fatalf("ready = %+v", h.Ready)
	}
	r := MustQuery(t, net.JoinHostPort("127.0.53.1", strconv.Itoa(h.Ready.Port)), ".", dns.TypeSOA, QueryOpts{})
	if !r.Authoritative || len(r.Answer) != 1 {
		t.Fatalf("root SOA: %v", r)
	}
	if got := h.Stats(t).Queries["127.0.53.1"]; got != 1 {
		t.Fatalf("root server query count = %d, want 1", got)
	}
}
