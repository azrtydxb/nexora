package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func trustedProxy(a netip.Addr, proxies []netip.Prefix) bool {
	for _, p := range proxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// ClientAddr is the client address of r: the client key of the auth failure throttle. It is the
// TCP peer address, unless the peer is a trusted proxy: then it is the rightmost X-Forwarded-For
// entry that is not itself a trusted proxy. A header from an untrusted peer is ignored because any
// client can set it. Only X-Forwarded-For is used; malformed or scoped addresses
// in the trusted suffix fall back to the peer. Configure proxies at startup and
// keep the slice immutable while serving requests.
func ClientAddr(r *http.Request, proxies []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	// One key for IPv4 and its IPv4-mapped representation, with or without a proxy.
	host = peer.Unmap().String()
	if !trustedProxy(peer, proxies) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil || a.Zone() != "" {
			return host
		}
		if !trustedProxy(a, proxies) {
			return a.Unmap().String()
		}
	}
	return host
}
