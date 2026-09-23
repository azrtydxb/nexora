package ai

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
)

// Resolver resolves a host name to its addresses.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10") // RFC 6598

// CheckEndpoint fails closed with ErrEndpointNotPrivate unless every address of the base URL host is
// loopback, RFC 1918, RFC 6598, IPv6 ULA or link-local, or allowPublic is set. A host that does not
// resolve returns ErrProvider: AI stays on and the call fails.
func CheckEndpoint(ctx context.Context, baseURL string, allowPublic bool, resolve Resolver) error {
	if allowPublic {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: invalid base URL", ErrProvider)
	}
	_, err = endpointAddresses(ctx, u.Hostname(), allowPublic, resolve)
	return err
}

func endpointAddresses(ctx context.Context, host string, allowPublic bool, resolve Resolver) ([]netip.Addr, error) {
	var addrs []netip.Addr
	var err error
	if a, parseErr := netip.ParseAddr(host); parseErr == nil {
		addrs = []netip.Addr{a}
	} else if addrs, err = resolve(ctx, host); err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("%w: resolve %s: %v", ErrProvider, host, err)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if !a.IsValid() || (!allowPublic && !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !sharedAddressSpace.Contains(a)) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrEndpointNotPrivate, host, a)
		}
	}
	return addrs, nil
}
