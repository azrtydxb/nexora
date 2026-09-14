package api

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientAddrTrustsForwardedForOnlyFromTrustedProxies(t *testing.T) {
	TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}
	defer func() { TrustedProxies = nil }()
	cases := []struct{ peer, xff, want string }{
		{"192.168.10.50:4000", "203.0.113.9", "192.168.10.50"},             // untrusted peer: header ignored
		{"10.42.3.7:4000", "", "10.42.3.7"},                                // trusted peer without header
		{"10.42.3.7:4000", "198.51.100.1, 192.168.10.50", "192.168.10.50"}, // rightmost untrusted hop
		{"10.42.3.7:4000", "192.168.10.50, 10.42.9.9", "192.168.10.50"},    // skips trusted hops
		{"10.42.3.7:4000", "not-an-ip", "10.42.3.7"},                       // garbage: fall back to peer
		{"[::ffff:10.42.3.7]:4000", "192.168.10.60", "192.168.10.60"},      // IPv4-mapped peer
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		r.RemoteAddr = c.peer
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientAddr(r); got != c.want {
			t.Errorf("peer %s xff %q -> %s, want %s", c.peer, c.xff, got, c.want)
		}
	}
}
