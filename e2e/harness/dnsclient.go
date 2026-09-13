package harness

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// QueryOpts tunes Query. A zero value is a UDP query without EDNS and a 2 s timeout.
type QueryOpts struct {
	TCP      bool
	EDNSSize uint16
	Cookie   []byte
	Timeout  time.Duration
	DO       bool // request DNSSEC records (adds EDNS)
}

// Query sends one recursive query for name/qtype to server and returns the reply and round-trip
// time. It never calls t.Fatal, so it is safe from goroutines.
func Query(t *testing.T, server, name string, qtype uint16, o QueryOpts) (*dns.Msg, time.Duration, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	if o.EDNSSize > 0 || o.Cookie != nil || o.DO {
		size := o.EDNSSize
		if size == 0 {
			size = dns.DefaultMsgSize
		}
		m.SetEdns0(size, o.DO)
		if o.Cookie != nil {
			opt := m.IsEdns0()
			opt.Option = append(opt.Option, &dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: hex.EncodeToString(o.Cookie)})
		}
	}
	c := &dns.Client{Net: "udp", Timeout: o.Timeout, UDPSize: o.EDNSSize}
	if o.TCP {
		c.Net = "tcp"
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Second
	}
	return c.Exchange(m, server)
}

// MustQuery is Query that fails the test on a transport error.
func MustQuery(t *testing.T, server, name string, qtype uint16, o QueryOpts) *dns.Msg {
	t.Helper()
	r, _, err := Query(t, server, name, qtype, o)
	if err != nil {
		t.Fatalf("query %s %s via %s: %v", name, dns.TypeToString[qtype], server, err)
	}
	return r
}

// UniqueName returns `<prefix>-<12 hex>.example.`, a name no other test has queried.
func UniqueName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b) + ".example."
}
