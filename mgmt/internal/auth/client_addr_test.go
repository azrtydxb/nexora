package auth

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientAddrBoundary(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::10/128")}
	for _, tc := range []struct {
		name, peer string
		headers    []string
		want       string
	}{
		{"default peer", "198.51.100.1:1234", nil, "198.51.100.1"},
		{"mapped peer", "[::ffff:198.51.100.1]:1234", nil, "198.51.100.1"},
		{"unknown IPv6 proxy", "[2001:db8:2::1]:1234", []string{"203.0.113.7"}, "2001:db8:2::1"},
		{"invalid peer", "not-an-address", []string{"203.0.113.7"}, "not-an-address"},
		{"unknown proxy", "198.51.100.1:1234", []string{"203.0.113.7"}, "198.51.100.1"},
		{"IPv4 client", "192.0.2.10:1234", []string{"198.51.100.1"}, "198.51.100.1"},
		{"IPv6 client", "[2001:db8::10]:1234", []string{"2001:db8:1::1"}, "2001:db8:1::1"},
		{"mapped client", "192.0.2.10:1234", []string{"::ffff:198.51.100.1"}, "198.51.100.1"},
		{"spoofed prefix", "192.0.2.10:1234", []string{"203.0.113.7, 198.51.100.1"}, "198.51.100.1"},
		{"multiple trusted hops", "192.0.2.10:1234", []string{"198.51.100.1, 2001:db8::10"}, "198.51.100.1"},
		{"multiple fields", "192.0.2.10:1234", []string{"203.0.113.7", "198.51.100.1, 2001:db8::10"}, "198.51.100.1"},
		{"malformed trusted suffix", "192.0.2.10:1234", []string{"198.51.100.1, unknown"}, "192.0.2.10"},
		{"empty last field", "192.0.2.10:1234", []string{"198.51.100.1", ""}, "192.0.2.10"},
		{"scoped IPv6", "192.0.2.10:1234", []string{"fe80::1%attacker-controlled"}, "192.0.2.10"},
		{"port is not IP", "192.0.2.10:1234", []string{"198.51.100.1:1234"}, "192.0.2.10"},
		{"all trusted", "192.0.2.10:1234", []string{"2001:db8::10"}, "192.0.2.10"},
		{"missing header", "192.0.2.10:1234", nil, "192.0.2.10"},
		{"malformed untrusted prefix ignored", "192.0.2.10:1234", []string{"garbage, 198.51.100.1"}, "198.51.100.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tc.peer
			for _, h := range tc.headers {
				r.Header.Add("X-Forwarded-For", h)
			}
			r.Header.Set("Forwarded", "for=203.0.113.99")
			r.Header.Set("X-Real-IP", "203.0.113.99")
			if got := ClientAddr(r, proxies); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientAddrNoImplicitTrustedNetworks(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:443", "10.0.0.1:443", "192.168.1.1:443", "[::1]:443"} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", "203.0.113.99")
		if got := ClientAddr(r, nil); got == "203.0.113.99" {
			t.Fatalf("implicitly trusted %s", peer)
		}
	}
}
